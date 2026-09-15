package account

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// pySignedToken 由 Python python-jose 用同一密钥现场签发（2026-09-15，exp=2100 年）——
// 跨语言字节兼容锚点：存量/过渡期 Python 签的登录态必须能被 Go 验证
const pySignedToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJhZG1pbiIsImV4cCI6NDEwMjQ0NDgwMH0.Q4ItqblRpaZD1jH8sFv0aB9EEiYuzB1Tjbc1BBdAzC0"

const testSecret = "harness-secret-key-2026"

func TestParsePythonSignedToken(t *testing.T) {
	sub, err := NewTokenSigner(testSecret).ParseSub(pySignedToken)
	if err != nil {
		t.Fatalf("Go 应能验 Python 签的 token: %v", err)
	}
	if sub != "admin" {
		t.Fatalf("sub 应为 admin，实际 %q", sub)
	}
}

func TestTokenRoundTrip(t *testing.T) {
	s := NewTokenSigner(testSecret)
	tok, err := s.Create("chenhu")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.ParseSub(tok)
	if err != nil || sub != "chenhu" {
		t.Fatalf("往返失败: sub=%q err=%v", sub, err)
	}
	// claims 恰两个键（sub/exp）——多一个都是契约违背（字节兼容点 §2）
	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte(testSecret), nil })
	if err != nil {
		t.Fatal(err)
	}
	if n := len(parsed.Claims.(jwt.MapClaims)); n != 2 {
		t.Fatalf("claims 应恰 2 个键，实际 %d 个", n)
	}
}

func TestTokenWrongSecret(t *testing.T) {
	tok, _ := NewTokenSigner("right-secret").Create("u")
	if _, err := NewTokenSigner("wrong-secret").ParseSub(tok); err == nil {
		t.Fatal("错密钥应验签失败")
	}
}

func TestTokenExpired(t *testing.T) {
	claims := jwt.MapClaims{"sub": "u", "exp": time.Now().Add(-time.Hour).Unix()}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if _, err := NewTokenSigner(testSecret).ParseSub(tok); err == nil {
		t.Fatal("过期 token 应被拒")
	}
}
