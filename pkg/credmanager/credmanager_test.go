package credmanager

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testCache struct {
	mu    sync.Mutex
	items map[string]any
}

func newTestCache(items map[string]any) *testCache {
	if items == nil {
		items = map[string]any{}
	}

	return &testCache{items: items}
}

func (c *testCache) Set(key string, value any, ttl int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = value
	return nil
}

func (c *testCache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.items[key]
	return value, ok
}

func (c *testCache) Gets(keys []string, prefix string) (map[string]any, []string) {
	return nil, nil
}

func (c *testCache) Sets(values map[string]any, prefix string) error {
	return nil
}

func (c *testCache) Delete(prefix string, keys ...string) error {
	return nil
}

func (c *testCache) Persist(path string) error {
	return nil
}

func (c *testCache) Restore(path string) error {
	return nil
}

func (c *testCache) DeleteAll() error {
	return nil
}

type testCredential struct {
	key          string
	expiry       time.Time
	refreshCount *atomic.Int32
	refreshDelay time.Duration
}

func (c testCredential) String() string {
	return c.key
}

func (c testCredential) Refresh(ctx context.Context) (Credential, error) {
	if c.refreshCount != nil {
		c.refreshCount.Add(1)
	}
	if c.refreshDelay > 0 {
		time.Sleep(c.refreshDelay)
	}

	return testCredential{
		key:          c.key,
		expiry:       time.Now().Add(time.Hour),
		refreshCount: c.refreshCount,
	}, nil
}

func (c testCredential) Key() string {
	return c.key
}

func (c testCredential) Expiry() time.Time {
	return c.expiry
}

func (c testCredential) RefreshedAt() *time.Time {
	return nil
}

func TestObtainRefreshesCredentialOncePerKey(t *testing.T) {
	var refreshCount atomic.Int32
	kv := newTestCache(map[string]any{
		"token": testCredential{
			key:          "token",
			expiry:       time.Now().Add(-time.Minute),
			refreshCount: &refreshCount,
			refreshDelay: 50 * time.Millisecond,
		},
	})
	manager := New(kv)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := manager.Obtain(context.Background(), "token")
			errs <- err
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("expected obtain to succeed, got error: %v", err)
		}
	}

	if got := refreshCount.Load(); got != 1 {
		t.Fatalf("expected one refresh, got %d", got)
	}
}

func TestObtainRejectsInvalidCachedCredential(t *testing.T) {
	manager := New(newTestCache(map[string]any{
		"token": "bad-cache-entry",
	}))

	_, err := manager.Obtain(context.Background(), "token")
	if err == nil {
		t.Fatal("expected invalid cache entry error")
	}
	if !strings.Contains(err.Error(), "invalid credential cache entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}
