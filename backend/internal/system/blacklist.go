package system

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// BlacklistCache 进程内黑名单缓存（Python 语义：启动同步加载 + 60s 刷新；增删即时同步；
// 查询侧小写化+去空白。单实例前提，多副本部署需换共享存储）
type BlacklistCache struct {
	store *Store
	mu    sync.RWMutex
	set   map[string]struct{}
}

func NewBlacklistCache(store *Store) *BlacklistCache {
	return &BlacklistCache{store: store, set: map[string]struct{}{}}
}

// Start 启动：同步全量加载（失败仅告警，不阻塞——Python 行为）+ 每 60s 刷新 goroutine
func (b *BlacklistCache) Start(ctx context.Context) {
	if err := b.reload(ctx); err != nil {
		slog.Warn("黑名单缓存初始加载失败", "err", err)
	}
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := b.reload(context.Background()); err != nil {
					slog.Warn("黑名单缓存刷新失败", "err", err)
				}
			}
		}
	}()
}

func (b *BlacklistCache) reload(ctx context.Context) error {
	all, err := b.store.LoadBlacklistAll(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]struct{}, len(all))
	for _, e := range all {
		next[normalizeEmail(e)] = struct{}{}
	}
	b.mu.Lock()
	b.set = next
	b.mu.Unlock()
	return nil
}

// IsBlacklisted 查询（大小写不敏感/去空白——发送引擎的 O(1) 拦截入口）
func (b *BlacklistCache) IsBlacklisted(email string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.set[normalizeEmail(email)]
	return ok
}

// Add 增删即时同步缓存（DB 写成功后由 handler 调用）
func (b *BlacklistCache) Add(email string) {
	b.mu.Lock()
	b.set[normalizeEmail(email)] = struct{}{}
	b.mu.Unlock()
}

func (b *BlacklistCache) Remove(email string) {
	b.mu.Lock()
	delete(b.set, normalizeEmail(email))
	b.mu.Unlock()
}

// ReloadAll 批量导入后手动全量重载
func (b *BlacklistCache) ReloadAll(ctx context.Context) {
	_ = b.reload(ctx)
}

// Count 数量（/admin/blacklist/count 读缓存——Python 口径）
func (b *BlacklistCache) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.set)
}

func normalizeEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}
