package httpx

import (
	"bytes"
	"encoding/json"
)

// Obj 有序 JSON 对象：键按插入顺序序列化（Go map 会按字母序——settings 等键序敏感契约专用）
type Obj []Pair

// Pair 一个键值对
type Pair struct {
	Key string
	Val any
}

// Set 追加键值对（链式）
func (o Obj) Set(key string, val any) Obj {
	return append(o, Pair{Key: key, Val: val})
}

// MarshalJSON 按插入顺序输出 {"k":v,...}（空对象输出 {}）
func (o Obj) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(p.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		v, err := MarshalNoEscape(p.Val)
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
