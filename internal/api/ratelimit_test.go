package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func rateLimitedServer(t *testing.T, hits *int64, retryAfter string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(429)
		w.Write([]byte(`{"Code":2028,"Error":"too many requests"}`))
	}))
}

// A 429 must cost exactly one request. Retrying while rate-limited pushes
// Proton's window further out, so the old behaviour (4 requests per call,
// Retry-After ignored) made the limit unrecoverable.
func TestRateLimit_NoRetryOn429(t *testing.T) {
	var hits int64
	srv := rateLimitedServer(t, &hits, "120")
	defer srv.Close()

	c := NewClient(&Session{UID: "u", AccessToken: "a", RefreshToken: "r"})
	c.SetBaseURL(srv.URL)

	_, err := c.GetServers(context.Background())
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("429 cost %d requests, want 1", got)
	}
	if !IsRateLimit(err) {
		t.Errorf("IsRateLimit(%v) = false, want true", err)
	}
	if got := RetryAfter(err); got != 120*time.Second {
		t.Errorf("RetryAfter = %v, want 2m0s (from the header)", got)
	}
}

// After a 429, the client must park locally: further requests are not
// sent at all until the cooldown expires. This is what lets a limit
// actually decay instead of being continuously refreshed.
func TestRateLimit_CooldownSuppressesLaterRequests(t *testing.T) {
	var hits int64
	srv := rateLimitedServer(t, &hits, "300")
	defer srv.Close()

	c := NewClient(&Session{UID: "u", AccessToken: "a", RefreshToken: "r"})
	c.SetBaseURL(srv.URL)

	c.GetServers(context.Background())
	for i := 0; i < 25; i++ {
		if _, err := c.GetVPNInfo(context.Background()); !IsRateLimit(err) {
			t.Fatalf("call %d: expected suppression, got %v", i, err)
		}
	}

	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("26 calls produced %d requests, want 1 (25 suppressed locally)", got)
	}
	if d := c.RateLimitedFor(); d < 4*time.Minute {
		t.Errorf("RateLimitedFor = %v, want ~5m from Retry-After", d)
	}
}

// A cooldown with no Retry-After header still parks the client.
func TestRateLimit_CooldownWithoutHeader(t *testing.T) {
	var hits int64
	srv := rateLimitedServer(t, &hits, "")
	defer srv.Close()

	c := NewClient(&Session{UID: "u", AccessToken: "a", RefreshToken: "r"})
	c.SetBaseURL(srv.URL)

	c.GetServers(context.Background())
	if d := c.RateLimitedFor(); d <= 0 || d > defaultRateLimitCooldown {
		t.Errorf("RateLimitedFor = %v, want (0, %v]", d, defaultRateLimitCooldown)
	}
}

// Proton's refresh tokens are single-use and rotate on every refresh.
// Concurrent refreshes make the losers replay an already-rotated token,
// which Proton reads as token reuse. Exactly one refresh must be sent.
func TestRefresh_SingleFlightUnderConcurrent401s(t *testing.T) {
	var refreshes int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/refresh" {
			if n := atomic.AddInt64(&refreshes, 1); n > 1 {
				// Replay of a rotated token — what we must never send.
				w.WriteHeader(422)
				w.Write([]byte(`{"Code":10013,"Error":"Invalid refresh token"}`))
				return
			}
			w.Write([]byte(`{"AccessToken":"new","RefreshToken":"rotated","UID":"u"}`))
			return
		}
		w.WriteHeader(401)
		w.Write([]byte(`{"Code":401,"Error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := NewClient(&Session{UID: "u", AccessToken: "a", RefreshToken: "r"})
	c.SetBaseURL(srv.URL)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.GetVPNInfo(context.Background()) }()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&refreshes); got != 1 {
		t.Errorf("8 concurrent 401s produced %d refreshes, want 1 (%d were token reuse)", got, got-1)
	}
}

// Retry-After accepts an HTTP-date as well as delta-seconds.
func TestRateLimit_RetryAfterHTTPDate(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(90*time.Second).UTC().Format(http.TimeFormat))
	if d := parseRetryAfter(h); d < 80*time.Second || d > 90*time.Second {
		t.Errorf("parseRetryAfter(http-date) = %v, want ~90s", d)
	}

	h.Set("Retry-After", "garbage")
	if d := parseRetryAfter(h); d != 0 {
		t.Errorf("parseRetryAfter(garbage) = %v, want 0", d)
	}
}
