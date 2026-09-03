package repomap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Cache stores repository maps between runs.
//
// A map is a pure function of the tracked file list at a revision, so it can be
// reused across every walkthrough of the same commit. Only immutable revisions
// are cached: the working tree changes under us, and a stale map is worse than
// a recomputed one.
type Cache struct {
	dir string
	ttl time.Duration
}

// NewCache opens a cache directory. An empty dir uses the user cache directory.
func NewCache(dir string, ttl time.Duration) *Cache {
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(base, "codewalk", "repomap")
	}
	if ttl <= 0 {
		ttl = 14 * 24 * time.Hour
	}
	return &Cache{dir: dir, ttl: ttl}
}

// key derives a filename from the repository root and revision. The root is
// hashed rather than stored, so the cache does not scatter local filesystem
// paths across the disk.
func (c *Cache) key(root, rev string) string {
	sum := sha256.Sum256([]byte(root + "\x00" + rev))
	return hex.EncodeToString(sum[:12]) + ".json"
}

type cacheEntry struct {
	StoredAt time.Time `json:"stored_at"`
	Map      *Map      `json:"map"`
}

// Load returns a cached map, or nil when there is no usable entry. Cache
// problems are never fatal: the caller simply recomputes.
func (c *Cache) Load(root, rev string) *Map {
	if c == nil || rev == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(c.dir, c.key(root, rev)))
	if err != nil {
		return nil
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.Map == nil {
		return nil
	}
	if time.Since(entry.StoredAt) > c.ttl {
		return nil
	}
	return entry.Map
}

// Store writes a map for an immutable revision.
func (c *Cache) Store(root, rev string, m *Map) {
	if c == nil || rev == "" || m == nil {
		return
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(cacheEntry{StoredAt: time.Now().UTC(), Map: m})
	if err != nil {
		return
	}
	path := filepath.Join(c.dir, c.key(root, rev))
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}
