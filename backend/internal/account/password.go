package account

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost 与 Python passlib 默认一致（rounds=12）
const bcryptCost = 12

// HashPassword 生成新哈希：x/crypto 产出 $2a$ 前缀，改写为 $2b$（与 Python bcrypt 4.x 产出一致；算法语义相同）
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return strings.Replace(string(b), "$2a$", "$2b$", 1), nil
}

// VerifyPassword 验证存量哈希：$2a$/$2b$/$2y$ 通验（x/crypto 兼容全部主流前缀）
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
