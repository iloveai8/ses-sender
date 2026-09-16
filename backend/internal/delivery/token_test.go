package delivery

import "testing"

// Python 现场生成的 token 夹具（secret=harness-secret-key-2026，2026-09-16）——跨语言字节兼容锚点
const (
	testSecret = "harness-secret-key-2026"
	pyValid    = "c2VlZDFAaGFybmVzcy5sb2NhbHxhZG1pbkBzZWVkLmxvY2FsfGRhMTJhN2FkNjYwZmJhYzY="
	pyAlready  = "dW5zdWItc2VlZEBoYXJuZXNzLmxvY2FsfGFkbWluQHNlZWQubG9jYWx8NDk4MTA5ZWE4MzVmYWY4MA=="
	pyBadSig   = "c2VlZDFAaGFybmVzcy5sb2NhbHxhZG1pbkBzZWVkLmxvY2FsfDAwMDAwMDAwMDAwMDAwMDA="
)

func TestVerifyPythonToken(t *testing.T) {
	email, source, ok := VerifyUnsubToken(testSecret, pyValid)
	if !ok || email != "seed1@harness.local" || source != "admin@seed.local" {
		t.Fatalf("Go 应能验 Python token: ok=%v email=%q source=%q", ok, email, source)
	}
	if _, _, ok := VerifyUnsubToken(testSecret, pyAlready); !ok {
		t.Fatal("already 夹具同样应验签通过")
	}
}

func TestVerifyBadSig(t *testing.T) {
	if _, _, ok := VerifyUnsubToken(testSecret, pyBadSig); ok {
		t.Fatal("坏签名应被拒")
	}
}

func TestTokenRoundTripByteEqual(t *testing.T) {
	// Go 生成的 token 必须与 Python 产出逐字节一致（存量邮件链接命脉）
	got := GenerateUnsubToken(testSecret, "seed1@harness.local", "admin@seed.local")
	if got != pyValid {
		t.Fatalf("生成不一致:\n got  %s\n want %s", got, pyValid)
	}
}

func TestVerifyMalformed(t *testing.T) {
	for _, tok := range []string{"", "not-base64!!", "AAAAAAAA", "YQ|B|C"} {
		if _, _, ok := VerifyUnsubToken(testSecret, tok); ok {
			t.Fatalf("畸形 token %q 应被拒", tok)
		}
	}
}
