package account

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 登录限速参数（与 Python 版一致）：5 分钟窗口内失败 5 次 → 锁 10 分钟
const (
	windowSeconds = 300
	maxFailures   = 5
	lockSeconds   = 600
)

// RateLimiter 进程内存限速（单实例前提；多副本部署时需换共享存储——见 docs/v2 01-REQUIREMENTS A2）
type RateLimiter struct {
	mu          sync.Mutex
	failures    map[string][]time.Time // ip → 窗口内失败时间戳
	lockedUntil map[string]time.Time   // ip → 锁定截止
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{failures: map[string][]time.Time{}, lockedUntil: map[string]time.Time{}}
}

// ClientIP 取真实客户端 IP：X-Forwarded-For 最后一项（ALB 追加的可信直连 IP）；
// 前提=前方有 ALB（生产形态），裸部署下 XFF 可伪造属部署问题（同 Python 版结论）
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	if r.RemoteAddr != "" {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			return host
		}
	}
	return r.RemoteAddr
}

// IsLocked 剩余锁定秒数（0=未锁）
func (rl *RateLimiter) IsLocked(ip string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if until, ok := rl.lockedUntil[ip]; ok {
		if d := time.Until(until); d > 0 {
			return int(d.Seconds())
		}
		delete(rl.lockedUntil, ip)
	}
	return 0
}

// RecordFailure 记一次失败；返回本次触发的锁定秒数（0=未触发）
func (rl *RateLimiter) RecordFailure(ip string) int {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	q := rl.failures[ip]
	for len(q) > 0 && now.Sub(q[0]) > windowSeconds*time.Second {
		q = q[1:]
	}
	q = append(q, now)
	rl.failures[ip] = q
	if len(q) >= maxFailures {
		rl.lockedUntil[ip] = now.Add(lockSeconds * time.Second)
		delete(rl.failures, ip)
		rl.cleanup(now)
		return lockSeconds
	}
	rl.cleanup(now)
	return 0
}

// RecordSuccess 登录成功清零该 IP 计数与锁定
func (rl *RateLimiter) RecordSuccess(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.failures, ip)
	delete(rl.lockedUntil, ip)
}

// cleanup 清过期条目（每次记录顺带执行，防不同 IP 喷射堆积——毛边修复项已含）
func (rl *RateLimiter) cleanup(now time.Time) {
	for ip, until := range rl.lockedUntil {
		if !until.After(now) {
			delete(rl.lockedUntil, ip)
		}
	}
	for ip, q := range rl.failures {
		if len(q) == 0 || now.Sub(q[len(q)-1]) > windowSeconds*time.Second {
			delete(rl.failures, ip)
		}
	}
}
