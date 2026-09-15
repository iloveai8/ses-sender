package account

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken 令牌无效/过期/签名不符的哨兵错误（HTTP 层映射为 401 无效的认证信息）
var ErrInvalidToken = errors.New("invalid token")

// tokenTTL 登录令牌有效期（与 Python 版一致：24 小时，写死项）
const tokenTTL = 24 * time.Hour

// TokenSigner JWT 签发/解析（字节兼容点：payload 恰 {sub, exp} 两个 claim）
type TokenSigner struct{ secret []byte }

func NewTokenSigner(secret string) *TokenSigner { return &TokenSigner{secret: []byte(secret)} }

// Create 签发：MapClaims 恰两个键（sub=用户名，exp=当前+24h 整秒）——多一个 claim 都是契约违背
func (t *TokenSigner) Create(sub string) (string, error) {
	claims := jwt.MapClaims{
		"sub": sub,
		"exp": time.Now().Add(tokenTTL).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// ParseSub 解析并验签：只认 HS256；失败/过期/缺 sub 统一返回 ErrInvalidToken
func (t *TokenSigner) ParseSub(token string) (string, error) {
	parsed, err := jwt.Parse(token, func(tk *jwt.Token) (any, error) {
		if _, ok := tk.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return t.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return "", ErrInvalidToken
	}
	sub, ok := parsed.Claims.(jwt.MapClaims)["sub"].(string)
	if !ok || sub == "" {
		return "", ErrInvalidToken
	}
	return sub, nil
}
