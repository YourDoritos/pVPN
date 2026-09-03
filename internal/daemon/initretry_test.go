package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YourDoritos/pvpn/internal/api"
	"github.com/YourDoritos/pvpn/internal/config"
)

// newStuckDaemon returns a daemon that is authenticated but has no server
// list — the state every boot passes through, because initSession fires
// before the network is up — talking to an API that rate-limits the
// (large, expensive) logicals fetch.
func newStuckDaemon(t *testing.T, vpnInfoHits, logicalsHits *int64) (*Daemon, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vpn/v2":
			atomic.AddInt64(vpnInfoHits, 1)
			w.Write([]byte(`{"VPN":{"MaxTier":2,"PlanTitle":"Proton Unlimited"}}`))
		case "/vpn/v1/logicals":
			atomic.AddInt64(logicalsHits, 1)
			w.Header().Set("Retry-After", "300")
			w.WriteHeader(429)
			w.Write([]byte(`{"Code":2028,"Error":"too many requests"}`))
		default:
			w.WriteHeader(404)
		}
	}))

	client := api.NewClient(&api.Session{UID: "u", AccessToken: "a", RefreshToken: "r"})
	client.SetBaseURL(srv.URL)

	d := New(&config.Config{}, client, nil)
	return d, func() { d.cancel(); srv.Close() }
}

// Netlink events are not evidence that anything changed at Proton's end.
// On a laptop they fire continuously (WiFi roaming, powersave carrier
// flaps, DHCP renewals, IPv6 privacy-address rotation, container veth
// churn). Before the pacing gate, each one re-ran a full session init
// while the server list stayed empty — and a rate-limited GetServers
// guarantees it stays empty, so a single 429 became permanent.
func TestInitRetry_NetlinkChurnDoesNotHammerAPI(t *testing.T) {
	var vpnInfo, logicals int64
	d, cleanup := newStuckDaemon(t, &vpnInfo, &logicals)
	defer cleanup()

	const events = 40
	for i := 0; i < events; i++ {
		d.onNetworkChange()
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // drain in-flight

	total := atomic.LoadInt64(&vpnInfo) + atomic.LoadInt64(&logicals)
	if total > 2 {
		t.Errorf("%d netlink events produced %d API requests, want <=2 "+
			"(one init attempt, then paced out)", events, total)
	}
	t.Logf("%d netlink events => %d API requests", events, total)
}

// After a rate-limited init, the next attempt must be pushed out by at
// least what the API asked for (Retry-After: 300), not retried on the
// next netlink event.
func TestInitRetry_HonorsServerCooldown(t *testing.T) {
	var vpnInfo, logicals int64
	d, cleanup := newStuckDaemon(t, &vpnInfo, &logicals)
	defer cleanup()

	d.onNetworkChange()
	time.Sleep(500 * time.Millisecond)

	d.mu.RLock()
	next := d.initNextAttempt
	d.mu.RUnlock()

	if wait := time.Until(next); wait < 4*time.Minute {
		t.Errorf("next init attempt in %v, want >=4m (server asked for 300s)", wait)
	}
}

// A failing init must back off exponentially even without a Retry-After,
// rather than retrying at netlink speed forever.
func TestInitRetry_BacksOffExponentially(t *testing.T) {
	d := New(&config.Config{}, api.NewClient(nil), nil)
	defer d.cancel()

	err := &api.RequestError{HTTPStatus: 500, Message: "boom"}
	var seen []time.Duration
	for i := 0; i < 8; i++ {
		d.noteInitResult(err)
		d.mu.RLock()
		seen = append(seen, d.initBackoff)
		d.mu.RUnlock()
	}

	if seen[0] != initRetryMin {
		t.Errorf("first backoff = %v, want %v", seen[0], initRetryMin)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Errorf("backoff shrank: %v -> %v at step %d", seen[i-1], seen[i], i)
		}
	}
	if last := seen[len(seen)-1]; last != initRetryMax {
		t.Errorf("backoff capped at %v, want %v", last, initRetryMax)
	}

	// Success clears the pacing so a recovered network reconnects at once.
	d.noteInitResult(nil)
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.initBackoff != 0 || !d.initNextAttempt.IsZero() {
		t.Errorf("success left backoff=%v next=%v, want reset", d.initBackoff, d.initNextAttempt)
	}
}

// With a cache on disk, a rate-limited server refresh must not leave the
// daemon empty-handed: the stale list is kept, which both keeps the
// daemon connectable and stops retryInitSessionIfStuck from firing on
// every netlink event for the rest of the session.
func TestInitRetry_StaleCacheSurvivesRateLimit(t *testing.T) {
	var vpnInfo, logicals int64
	d, cleanup := newStuckDaemon(t, &vpnInfo, &logicals)
	defer cleanup()

	cachePath := filepath.Join(t.TempDir(), "servers.json")
	cache := api.NewServerCache(cachePath)
	if err := cache.Save([]api.LogicalServer{
		{Name: "DE#1", ExitCountry: "DE", Status: 1, Tier: 2},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	d.serverCache = cache

	// Simulate the boot-time seed that Run() performs.
	servers, _, err := cache.Load()
	if err != nil {
		t.Fatalf("load cache: %v", err)
	}
	d.mu.Lock()
	d.serverList = servers
	d.mu.Unlock()

	// Force a refresh by aging the cache past the TTL.
	ageCache(t, cachePath, api.ServerCacheTTL+time.Hour)

	if err := d.initSession(); err != nil {
		t.Errorf("initSession returned %v; a stale cache should make this a success", err)
	}

	d.mu.RLock()
	got := len(d.serverList)
	d.mu.RUnlock()
	if got == 0 {
		t.Error("server list was emptied by a rate-limited refresh")
	}

	// And with a list in hand, netlink churn must be inert.
	before := atomic.LoadInt64(&vpnInfo) + atomic.LoadInt64(&logicals)
	for i := 0; i < 20; i++ {
		d.onNetworkChange()
	}
	time.Sleep(300 * time.Millisecond)
	if after := atomic.LoadInt64(&vpnInfo) + atomic.LoadInt64(&logicals); after != before {
		t.Errorf("netlink churn made %d API requests with a list already loaded", after-before)
	}
}

// A fresh cache must skip the multi-megabyte logicals fetch entirely.
func TestInitRetry_FreshCacheSkipsFetch(t *testing.T) {
	var vpnInfo, logicals int64
	d, cleanup := newStuckDaemon(t, &vpnInfo, &logicals)
	defer cleanup()

	cache := api.NewServerCache(filepath.Join(t.TempDir(), "servers.json"))
	if err := cache.Save([]api.LogicalServer{{Name: "DE#1", ExitCountry: "DE", Status: 1}}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	d.serverCache = cache

	if err := d.initSession(); err != nil {
		t.Fatalf("initSession: %v", err)
	}
	if got := atomic.LoadInt64(&logicals); got != 0 {
		t.Errorf("fresh cache still triggered %d logicals fetches, want 0", got)
	}
}

func ageCache(t *testing.T, path string, age time.Duration) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal cache: %v", err)
	}
	stamp, err := json.Marshal(time.Now().Add(-age))
	if err != nil {
		t.Fatalf("marshal stamp: %v", err)
	}
	raw["fetched_at"] = stamp
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	if err := os.WriteFile(path, out, 0640); err != nil {
		t.Fatalf("write cache: %v", err)
	}
}
