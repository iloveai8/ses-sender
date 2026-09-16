package httpx

import (
	"bytes"
	"encoding/json"

	"github.com/gin-gonic/gin"
)

// MarshalNoEscape 序列化不转义 HTML（<>& 原样输出）且无尾随换行——
// 与 Python/FastAPI 字节级一致的唯一序列化出口。
// 契约依据：golden 中 html_body 为 "<p>seed</p>" 而非 "<p>..."；
// encoding/json 默认 SetEscapeHTML(true) + Encoder 追加 \n，两者都须关掉。
func MarshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return b, nil
}

// WriteJSON 统一 JSON 渲染出口（全项目禁止直接用 c.JSON——转义行为会破契约）
func WriteJSON(c *gin.Context, status int, v any) {
	b, err := MarshalNoEscape(v)
	if err != nil {
		c.Data(500, "application/json; charset=utf-8", []byte(`{"detail":"响应序列化失败"}`))
		return
	}
	c.Data(status, "application/json; charset=utf-8", b)
}
