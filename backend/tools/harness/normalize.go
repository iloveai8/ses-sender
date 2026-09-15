package main

import "regexp"

// 归一化：可再生字段替换为占位符，录制与比对共用同一规则（两侧等价替换 → 不影响一致性判定）
var reTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`)
var reJWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var reBatch = regexp.MustCompile(`batch-[0-9a-f]{12}`)
var reHex16 = regexp.MustCompile(`\b[0-9a-f]{16,64}\b`)

// Normalize 对响应体做归一化（顺序敏感：JWT 先于十六进制串，避免误切）
func Normalize(s string) string {
	s = reTime.ReplaceAllString(s, "<TS>")
	s = reJWT.ReplaceAllString(s, "<JWT>")
	s = reBatch.ReplaceAllString(s, "<BATCH>")
	s = reHex16.ReplaceAllString(s, "<UUID>")
	return s
}
