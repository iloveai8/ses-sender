// harness 对拍工具：录制 Python 版响应为金标准（golden），再对 Go 版重放比对。
//
// 用法：
//
//	harness -mode record -base http://localhost:8010   # 录：打 Python，写 contracts/golden/
//	harness -mode diff   -base http://localhost:8011   # 比：打 Go，与 golden 逐字段对比
//
// 语料：contracts/corpus.yaml（端点 × 正例/负例/权限矩阵）
// 归一化：时间戳/JWT/批次号/十六进制串 → 占位符（可再生字段不参与比对）
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Case struct {
	Name    string            `yaml:"name"`
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Body    any               `yaml:"body,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Auth    string            `yaml:"auth,omitempty"` // 空|user|admin：自动登录取 token
	Skip    bool              `yaml:"skip,omitempty"` // diff 时跳过（如写操作的二次执行）
}

type Corpus struct {
	Defaults struct {
		AdminUser string `yaml:"admin_user"`
		AdminPass string `yaml:"admin_pass"`
		UserName  string `yaml:"user_name"`
		UserPass  string `yaml:"user_pass"`
	} `yaml:"defaults"`
	Cases []Case `yaml:"cases"`
}

type Golden struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"` // 已归一化
}

var (
	mode   = flag.String("mode", "diff", "record | diff")
	base   = flag.String("base", "http://localhost:8010", "目标服务基址")
	corpus = flag.String("corpus", "contracts/corpus.yaml", "语料文件")
	golden = flag.String("golden", "contracts/golden", "golden 目录")
	client = &http.Client{Timeout: 15 * time.Second}
)

func main() {
	flag.Parse()
	var c Corpus
	raw, err := os.ReadFile(*corpus)
	fatal(err, "读语料")
	fatal(yaml.Unmarshal(raw, &c), "解析语料")
	if len(c.Cases) == 0 {
		fatal(fmt.Errorf("语料为空"), "")
	}

	// 按需登录换 token（admin/user 各一次）
	tokens := map[string]string{}
	for _, role := range []string{"admin", "user"} {
		if needRole(c.Cases, role) {
			tokens[role] = login(c, role)
		}
	}

	pass, fail := 0, 0
	for _, cs := range c.Cases {
		if *mode == "diff" && cs.Skip {
			fmt.Printf("SKIP  %s\n", cs.Name)
			continue
		}
		resp, err := exec(cs, tokens)
		if err != nil {
			fmt.Printf("ERROR %s: %v\n", cs.Name, err)
			fail++
			continue
		}
		if *mode == "record" {
			fatal(saveGolden(cs.Name, resp), "写 golden")
			fmt.Printf("REC   %s → %d\n", cs.Name, resp.Status)
			continue
		}
		if ok, why := diffGolden(cs.Name, resp); ok {
			fmt.Printf("PASS  %s\n", cs.Name)
			pass++
		} else {
			fmt.Printf("FAIL  %s: %s\n", cs.Name, why)
			fail++
		}
	}
	if *mode == "record" {
		fmt.Printf("已录制 %d 条 golden\n", len(c.Cases))
		return
	}
	fmt.Printf("\n合计 %d：PASS %d，FAIL %d\n", pass+fail, pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}

func needRole(cases []Case, role string) bool {
	for _, cs := range cases {
		if cs.Auth == role {
			return true
		}
	}
	return false
}

func login(c Corpus, role string) string {
	user, pass := c.Defaults.AdminUser, c.Defaults.AdminPass
	if role == "user" {
		user, pass = c.Defaults.UserName, c.Defaults.UserPass
	}
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := client.Post(*base+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("[harness] 警告：登录 %s 请求失败：%v（相关用例将以无 token 状态执行）\n", role, err)
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		fmt.Printf("[harness] 警告：登录 %s 未取得 token（HTTP %d）——相关用例将以无 token 状态执行\n", role, resp.StatusCode)
		return ""
	}
	return out.AccessToken
}

func exec(cs Case, tokens map[string]string) (*Golden, error) {
	var rd io.Reader
	if cs.Body != nil {
		b, err := json.Marshal(cs.Body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(strings.ToUpper(cs.Method), *base+cs.Path, rd)
	if err != nil {
		return nil, err
	}
	if cs.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range cs.Headers {
		req.Header.Set(k, v)
	}
	if cs.Auth != "" && tokens[cs.Auth] != "" {
		req.Header.Set("Authorization", "Bearer "+tokens[cs.Auth])
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return &Golden{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        Normalize(string(b)),
	}, nil
}

func saveGolden(name string, g *Golden) error {
	if err := os.MkdirAll(*golden, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*golden, name+".json"), append(b, '\n'), 0o644)
}

func diffGolden(name string, g *Golden) (bool, string) {
	b, err := os.ReadFile(filepath.Join(*golden, name+".json"))
	if err != nil {
		return false, "golden 缺失: " + name
	}
	var want Golden
	if err := json.Unmarshal(b, &want); err != nil {
		return false, "golden 损坏: " + err.Error()
	}
	if want.Status != g.Status {
		return false, fmt.Sprintf("状态码 golden=%d 实测=%d", want.Status, g.Status)
	}
	if want.ContentType != g.ContentType && !ctLooseEqual(want.ContentType, g.ContentType) {
		return false, fmt.Sprintf("Content-Type golden=%q 实测=%q", want.ContentType, g.ContentType)
	}
	if want.Body != g.Body {
		return false, "响应体不一致\n  golden: " + want.Body + "\n  实测: " + g.Body
	}
	return true, ""
}

// ctLooseEqual：应用/json 场景下 charset 差异视为等价（uvicorn 带 charset=utf-8，gin 不带）
func ctLooseEqual(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(s)
		if i := strings.Index(s, ";"); i >= 0 {
			s = s[:i]
		}
		return strings.TrimSpace(s)
	}
	return norm(a) == norm(b)
}

func fatal(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "[harness] %s: %v\n", what, err)
		os.Exit(2)
	}
}
