package httpx

import (
	"encoding/json"
	"testing"
)

// TestMarshalNoEscape 契约锚定：HTML 不转义、无尾随换行（FastAPI 字节级一致）
func TestMarshalNoEscape(t *testing.T) {
	b, err := MarshalNoEscape(map[string]string{"html_body": "<p>seed</p>"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"html_body":"<p>seed</p>"}`
	if string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
}
