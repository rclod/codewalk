package repomap

import (
	"testing"
	"time"
)

func TestCacheRoundTripsImmutableRevisions(t *testing.T) {
	cache := NewCache(t.TempDir(), time.Hour)
	m := FromFileList("orders", "abc123", []string{"main.go"})

	cache.Store("/home/user/projects/example-app", "abc123", m)
	got := cache.Load("/home/user/projects/example-app", "abc123")
	if got == nil || got.Repository != "orders" || got.FileCount != 1 {
		t.Fatalf("cached map did not round trip: %+v", got)
	}
	if other := cache.Load("/home/user/projects/other-app", "abc123"); other != nil {
		t.Error("cache entries must be scoped to a repository")
	}
	if other := cache.Load("/home/user/projects/example-app", "def456"); other != nil {
		t.Error("cache entries must be scoped to a revision")
	}
}

func TestWorkingTreeIsNeverCached(t *testing.T) {
	// The working tree changes under us, so a stale map would be worse than
	// recomputing one.
	cache := NewCache(t.TempDir(), time.Hour)
	cache.Store("/home/user/projects/example-app", "", FromFileList("orders", "", []string{"main.go"}))
	if cache.Load("/home/user/projects/example-app", "") != nil {
		t.Error("the working tree must not be served from cache")
	}
}

func TestExpiredEntriesAreIgnored(t *testing.T) {
	cache := NewCache(t.TempDir(), time.Nanosecond)
	cache.Store("/home/user/projects/example-app", "abc123", FromFileList("orders", "abc123", []string{"main.go"}))
	time.Sleep(time.Millisecond)
	if cache.Load("/home/user/projects/example-app", "abc123") != nil {
		t.Error("entries past their TTL should be ignored")
	}
}

func TestCacheFailuresAreNotFatal(t *testing.T) {
	// An unreadable cache directory must degrade to recomputation, never to an
	// error the user sees.
	cache := NewCache("/proc/definitely-not-writable/codewalk", time.Hour)
	cache.Store("/home/user/projects/example-app", "abc123", FromFileList("orders", "abc123", nil))
	if cache.Load("/home/user/projects/example-app", "abc123") != nil {
		t.Error("a failed store should simply produce a cache miss")
	}
	var nilCache *Cache
	nilCache.Store("/x", "abc123", nil)
	if nilCache.Load("/x", "abc123") != nil {
		t.Error("a nil cache should behave as a permanent miss")
	}
}
