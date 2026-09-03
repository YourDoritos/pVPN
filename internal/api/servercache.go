package api

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/YourDoritos/pvpn/internal/config"
)

// ServerCacheTTL is how long a cached server list is considered fresh
// enough to use without contacting the API at all.
//
// The list is ~18k logical servers and several megabytes, and
// /vpn/v1/logicals is the most aggressively rate-limited call the client
// makes. Before caching, every daemon start — every reboot, every
// `systemctl restart`, every failed-then-recovered bootstrap — refetched
// the whole thing. Load percentages drift over a few hours, but they only
// influence "fastest server" ordering; a slightly stale ordering is
// vastly better than being locked out of the API.
const ServerCacheTTL = 3 * time.Hour

// ServerCache persists the logical server list to disk.
type ServerCache struct {
	path string
}

type cachedServerList struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Servers   []LogicalServer `json:"servers"`
}

// NewServerCache returns a cache backed by the given file path.
func NewServerCache(path string) *ServerCache {
	return &ServerCache{path: path}
}

// Load reads the cached server list and reports its age. Returns a nil
// slice with no error when there is no usable cache — a missing or
// corrupt cache is a normal cold-start condition, not a failure.
func (c *ServerCache) Load() (servers []LogicalServer, age time.Duration, err error) {
	data, err := os.ReadFile(c.path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read server cache: %w", err)
	}

	var cached cachedServerList
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, 0, fmt.Errorf("parse server cache: %w", err)
	}
	if len(cached.Servers) == 0 {
		return nil, 0, nil
	}

	age = time.Since(cached.FetchedAt)
	if age < 0 {
		// Clock went backwards (NTP step at boot is common on machines
		// without an RTC). Treat as stale rather than infinitely fresh.
		age = ServerCacheTTL + time.Hour
	}
	return cached.Servers, age, nil
}

// Save atomically writes the server list to disk. A failure here is not
// fatal to the caller — it only means the next start refetches.
func (c *ServerCache) Save(servers []LogicalServer) error {
	if len(servers) == 0 {
		return nil
	}

	data, err := json.Marshal(cachedServerList{
		FetchedAt: time.Now(),
		Servers:   servers,
	})
	if err != nil {
		return fmt.Errorf("marshal server cache: %w", err)
	}

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0640); err != nil {
		return fmt.Errorf("write server cache: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename server cache: %w", err)
	}

	config.FixFileOwnership(c.path)
	return nil
}

// Fresh reports whether an entry of the given age may be used without
// refetching.
func Fresh(age time.Duration) bool {
	return age > 0 && age < ServerCacheTTL
}
