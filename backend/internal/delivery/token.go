// Package delivery 发送引擎域：退订闭环（token/页面/one-click）+ SES 事件回写 + SQS 轮询。
package delivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// GenerateUnsubToken 退订 token（字节兼容点 #1，与 Python core/unsubscribe.py 逐字节一致）：
// payload=email|source → HMAC-SHA256(secret) hex 截前 16 字符 → 整串 email|source|sig
// 做【带 padding 的 URL-safe base64】（URLEncoding，不是 RawURLEncoding）。无过期。
func GenerateUnsubToken(secret, email, source string) string {
	payload := email + "|" + source
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]
	return base64.URLEncoding.EncodeToString([]byte(payload + "|" + sig))
}

// VerifyUnsubToken 验签：解码失败/段数≠3/签名不符 → ok=false（恒时比较）
func VerifyUnsubToken(secret, token string) (email, source string, ok bool) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return "", "", false
	}
	email, source, sig := parts[0], parts[1], parts[2]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(email + "|" + source))
	expect := hex.EncodeToString(mac.Sum(nil))[:16]
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expect)) != 1 {
		return "", "", false
	}
	return email, source, true
}
