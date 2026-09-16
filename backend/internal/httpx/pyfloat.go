package httpx

import (
	"bytes"
	"strconv"
)

// PyFloat Python 风格浮点序列化：至少一位小数（100.0 而非 100；33.3 保持 33.3）。
// 契约依据：golden 中 delivery_rate/open_rate/progress 均为 100.0 形态。
type PyFloat float64

func (f PyFloat) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 64))
	if !bytes.ContainsAny(buf.Bytes(), ".eE") {
		buf.WriteString(".0")
	}
	return buf.Bytes(), nil
}

// Round1 Python round(x, 1) 等价（四舍五入到 1 位小数）
func Round1(v float64) PyFloat {
	return PyFloat(float64(int64(v*10+0.5)) / 10) // 非负场景（比率/进度恒 ≥0）
}
