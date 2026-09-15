package account

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 与 Python tests/test_ratelimit.py 对齐的锚定测试（登录限速语义）
func TestClientIPTakesLastXFFHop(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8") // 最左可伪造，取最后一跳
	if got := ClientIP(req); got != "5.6.7.8" {
		t.Fatalf("XFF 末位应生效，实际 %q", got)
	}
}

func TestClientIPFallbackRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil) // RemoteAddr=192.0.2.1:1234
	if got := ClientIP(req); got == "" || got == "5.6.7.8" {
		t.Fatalf("应回落 RemoteAddr，实际 %q", got)
	}
}

func TestFiveFailuresTriggerLock(t *testing.T) {
	rl := NewRateLimiter()
	ip := "9.9.9.9"
	for i := 0; i < 4; i++ {
		if locked := rl.RecordFailure(ip); locked != 0 {
			t.Fatalf("前 4 次不应触发锁定，第 %d 次返回 %d", i+1, locked)
		}
	}
	if locked := rl.RecordFailure(ip); locked != lockSeconds {
		t.Fatalf("第 5 次应触发 %d 秒锁定，实际 %d", lockSeconds, locked)
	}
	if remain := rl.IsLocked(ip); remain <= 0 || remain > lockSeconds {
		t.Fatalf("应处于锁定期，剩余 %d", remain)
	}
}

func TestSuccessResetsCounter(t *testing.T) {
	rl := NewRateLimiter()
	ip := "8.8.8.8"
	for i := 0; i < 4; i++ {
		rl.RecordFailure(ip)
	}
	rl.RecordSuccess(ip)
	if locked := rl.RecordFailure(ip); locked != 0 {
		t.Fatal("成功清零后，单次失败不应触发锁定")
	}
	if rl.IsLocked(ip) != 0 {
		t.Fatal("不应有锁")
	}
}

// http.StatusOK 引用占位（保持 net/http 导入用于未来请求级测试）
var _ = http.StatusOK
