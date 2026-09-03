package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerCache_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	c := NewServerCache(path)

	want := testServers()
	if err := c.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, age, err := c.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("loaded %d servers, want %d", len(got), len(want))
	}
	if got[0].Name != want[0].Name || got[0].ExitCountry != want[0].ExitCountry {
		t.Errorf("first entry = %+v, want %+v", got[0], want[0])
	}
	if age > time.Minute {
		t.Errorf("age = %v, want ~0", age)
	}
	if !Fresh(age) {
		t.Errorf("a just-written cache should be fresh")
	}
}

// A missing cache is a normal cold start, not an error.
func TestServerCache_MissingIsNotAnError(t *testing.T) {
	c := NewServerCache(filepath.Join(t.TempDir(), "absent.json"))
	got, _, err := c.Load()
	if err != nil {
		t.Errorf("Load on missing file: %v, want nil", err)
	}
	if got != nil {
		t.Errorf("got %d servers, want nil", len(got))
	}
}

func TestServerCache_CorruptIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	os.WriteFile(path, []byte("{not json"), 0640)

	if _, _, err := NewServerCache(path).Load(); err == nil {
		t.Error("Load on corrupt cache returned nil error")
	}
}

// A cache older than the TTL must not be treated as fresh — but it is
// still returned, because a stale list beats no list when the API is
// unreachable or rate-limiting us.
func TestServerCache_StaleIsReturnedButNotFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	writeCacheAged(t, path, ServerCacheTTL+time.Hour)

	got, age, err := NewServerCache(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("stale cache returned no servers; it should still be usable")
	}
	if Fresh(age) {
		t.Errorf("age %v reported fresh, want stale (TTL %v)", age, ServerCacheTTL)
	}
}

// Machines without an RTC boot with a clock in the past and get stepped
// forward by NTP, which would otherwise make a cache look infinitely
// fresh (negative age).
func TestServerCache_FutureTimestampTreatedAsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	writeCacheAged(t, path, -24*time.Hour) // written "in the future"

	_, age, err := NewServerCache(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if Fresh(age) {
		t.Errorf("future-dated cache reported fresh (age %v)", age)
	}
}

func TestServerCache_SaveEmptyIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	if err := NewServerCache(path).Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Save(nil) created a cache file; it should be a no-op")
	}
}

func writeCacheAged(t *testing.T, path string, age time.Duration) {
	t.Helper()
	c := NewServerCache(path)
	if err := c.Save(testServers()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Rewrite with a doctored timestamp.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var cached cachedServerList
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cached.FetchedAt = time.Now().Add(-age)
	out, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, out, 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
