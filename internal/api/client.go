package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://vpn-api.proton.me"
	AppVersion     = "linux-vpn@4.13.1"
	UserAgent      = "ProtonVPN/4.13.1 (Linux; go-pvpn)"
	DefaultTimeout = 30 * time.Second
	MaxRetries     = 3

	// When the API answers 429 we park every request this client would
	// make until the cooldown expires. Proton's gateway limiter counts
	// requests received *while* you are limited, so continuing to send
	// extends the ban instead of shortening it. The cooldown is honored
	// locally — no packet leaves the box — which is the only way to let
	// a limit actually expire.
	//
	// defaultRateLimitCooldown applies when the response carries no
	// usable Retry-After header.
	defaultRateLimitCooldown = 2 * time.Minute
	maxRateLimitCooldown     = 30 * time.Minute

	// minRefreshInterval is the shortest gap between two /auth/refresh
	// calls. A refresh rotates the single-use refresh token, so a burst
	// of them looks like token reuse to Proton and counts against the
	// per-account auth limit. If a request 401s within this window of a
	// successful refresh, the token is as fresh as it can be and the
	// session is dead — refreshing again cannot help.
	minRefreshInterval = 10 * time.Second
)

// Client is the Proton VPN API client. It handles authenticated requests,
// automatic token refresh, and retry logic.
type Client struct {
	httpClient *http.Client
	transport  *http.Transport // kept so pinned hosts can be mutated after construction
	baseURL    string

	mu           sync.RWMutex
	uid          string
	accessToken  string
	refreshToken string
	loginEmail   string

	// pinnedHosts maps a hostname to a list of IPs that should be used
	// in place of DNS resolution. Used at early boot when DNS is blocked
	// by the pre-boot kill switch (see vpn/preboot.go). Only the
	// vpn-api.proton.me hostname is ever pinned in practice.
	pinnedHosts map[string][]string

	// lastRefresh is when /auth/refresh last succeeded. Guarded by mu.
	lastRefresh time.Time

	// rateLimitedUntil is the wall-clock time before which this client
	// refuses to send anything. Set from a 429 response's Retry-After.
	// Guarded by mu.
	rateLimitedUntil time.Time

	// refreshMu single-flights /auth/refresh. Proton's refresh tokens are
	// single-use and rotate on every successful refresh, so two concurrent
	// refreshes race: the loser replays an already-rotated token, which
	// Proton treats as token reuse (a session-compromise signal) and which
	// counts hard against the per-account auth limits.
	refreshMu sync.Mutex

	// Called when tokens are rotated so the session can be persisted.
	OnTokenRefresh func(uid, accessToken, refreshToken string)
}

// NewClient creates a new API client. If session is non-nil, the client
// is initialized with existing auth tokens.
func NewClient(session *Session) *Client {
	c := &Client{
		baseURL:     DefaultBaseURL,
		pinnedHosts: make(map[string][]string),
	}

	// Custom transport whose DialContext consults the client's pinnedHosts
	// map before falling back to the system resolver. This lets the daemon
	// reach the Proton API at boot even while the kill switch blocks DNS
	// egress — the pinned IPs get written to disk by pvpnd on every
	// successful connect and re-loaded at startup.
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	c.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return dialer.DialContext(ctx, network, addr)
			}
			c.mu.RLock()
			ips := c.pinnedHosts[host]
			c.mu.RUnlock()
			if len(ips) == 0 {
				return dialer.DialContext(ctx, network, addr)
			}
			// Try each pinned IP in order — first one that connects wins.
			var lastErr error
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, fmt.Errorf("all pinned IPs for %s unreachable: %w", host, lastErr)
		},
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	c.httpClient = &http.Client{
		Timeout:   DefaultTimeout,
		Transport: c.transport,
	}

	if session != nil {
		c.uid = session.UID
		c.accessToken = session.AccessToken
		c.refreshToken = session.RefreshToken
		c.loginEmail = session.LoginEmail
	}
	return c
}

// SetPinnedAPIIPs installs a list of pre-resolved IP addresses for the
// Proton API hostname. Called by the daemon at startup after loading the
// cached IPs from disk, so the first API request after boot does not
// require DNS — which would be blocked by the pre-boot kill switch.
// Safe to call with an empty list (clears pinning).
func (c *Client) SetPinnedAPIIPs(ips []string) {
	host := hostFromURL(c.baseURL)
	if host == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ips) == 0 {
		delete(c.pinnedHosts, host)
		return
	}
	// Copy so the caller can mutate their slice without affecting us.
	copied := make([]string, len(ips))
	copy(copied, ips)
	c.pinnedHosts[host] = copied
}

// PinnedAPIIPs returns a snapshot of the currently pinned IPs for the API
// hostname. Order matches the order given to SetPinnedAPIIPs.
func (c *Client) PinnedAPIIPs() []string {
	host := hostFromURL(c.baseURL)
	c.mu.RLock()
	defer c.mu.RUnlock()
	ips := c.pinnedHosts[host]
	if len(ips) == 0 {
		return nil
	}
	out := make([]string, len(ips))
	copy(out, ips)
	return out
}

// hostFromURL extracts the hostname from a URL like "https://host/path".
// Returns "" if the URL cannot be parsed.
func hostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// SetBaseURL overrides the API base URL. Used by tests and by anyone
// pointing the client at a proxy; production always uses DefaultBaseURL.
func (c *Client) SetBaseURL(u string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = u
}

// SetSession updates the client's auth tokens.
func (c *Client) SetSession(uid, accessToken, refreshToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.uid = uid
	c.accessToken = accessToken
	c.refreshToken = refreshToken
}

// GetSession returns the current session tokens.
func (c *Client) GetSession() Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Session{
		UID:          c.uid,
		AccessToken:  c.accessToken,
		RefreshToken: c.refreshToken,
		LoginEmail:   c.loginEmail,
	}
}

// LoginEmail returns the email the user logged in with.
func (c *Client) LoginEmail() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loginEmail
}

// BaseURL returns the API base URL (needed for kill switch pinhole during reconnection).
func (c *Client) BaseURL() string {
	return c.baseURL
}

// IsAuthenticated returns true if the client has auth tokens.
func (c *Client) IsAuthenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.accessToken != ""
}

// RequestError represents an API error response.
type RequestError struct {
	HTTPStatus int
	Code       int
	Message    string

	// RetryAfter is how long the server (or our own cooldown) says to
	// wait before trying again. Zero when unknown.
	RetryAfter time.Duration

	// Local is true when this error was produced by the client's own
	// rate-limit cooldown without contacting the server at all.
	Local bool
}

func (e *RequestError) Error() string {
	if e.Local {
		return fmt.Sprintf("rate-limit cooldown active, request not sent (%s remaining)",
			e.RetryAfter.Round(time.Second))
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("API error %d (HTTP %d): %s (retry after %s)",
			e.Code, e.HTTPStatus, e.Message, e.RetryAfter.Round(time.Second))
	}
	return fmt.Sprintf("API error %d (HTTP %d): %s", e.Code, e.HTTPStatus, e.Message)
}

// IsRateLimit reports whether err is a 429 / Proton "too many requests"
// error, including one raised locally by the client's own cooldown.
func IsRateLimit(err error) bool {
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.HTTPStatus == 429 || reqErr.Code == 2028
}

// RetryAfter returns how long to wait before retrying, or 0 if err is not
// a rate-limit error or carries no hint.
func RetryAfter(err error) time.Duration {
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		return 0
	}
	return reqErr.RetryAfter
}

// RateLimitedFor returns how long this client will refuse to send
// requests, or 0 if it is not currently in a cooldown.
func (c *Client) RateLimitedFor() time.Duration {
	c.mu.RLock()
	until := c.rateLimitedUntil
	c.mu.RUnlock()
	if d := time.Until(until); d > 0 {
		return d
	}
	return 0
}

// enterCooldown parks all outgoing requests for d (clamped), unless a
// longer cooldown is already in effect.
func (c *Client) enterCooldown(d time.Duration) {
	if d <= 0 {
		d = defaultRateLimitCooldown
	}
	if d > maxRateLimitCooldown {
		d = maxRateLimitCooldown
	}
	until := time.Now().Add(d)
	c.mu.Lock()
	if until.After(c.rateLimitedUntil) {
		c.rateLimitedUntil = until
	}
	c.mu.Unlock()
}

// parseRetryAfter reads the Retry-After header in either supported form
// (delta-seconds or HTTP-date). Returns 0 when absent or unparseable.
func parseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// IsAuthError returns true if this error indicates the session is permanently
// dead and the user must re-login (e.g. refresh token revoked, account
// disabled). Transient errors (network, timeout, 5xx) return false.
func (e *RequestError) IsAuthError() bool {
	switch e.Code {
	case 10013: // Refresh token invalid — must re-authenticate
		return true
	case 10002: // Account deleted
		return true
	case 10003: // Account disabled
		return true
	}
	// Any 401 that isn't handled by token refresh is a dead session
	return e.HTTPStatus == 401
}

// IsAuthError checks whether an error represents a permanent auth failure.
// Returns false for network errors, timeouts, and other transient issues.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	if reqErr, ok := err.(*RequestError); ok {
		return reqErr.IsAuthError()
	}
	return false
}

// doRequest executes an HTTP request with auth headers and retry logic.
// It automatically refreshes tokens on 401.
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var lastErr error

	// A single logical call refreshes at most once. Every refresh spends
	// the single-use refresh token and rotates it; if the retry that
	// follows a *successful* refresh still gets a 401, the session is
	// genuinely dead and refreshing again just burns tokens against the
	// per-account auth rate limit.
	refreshed := false

	for attempt := 0; attempt <= MaxRetries; attempt++ {
		if attempt > 0 {
			// Backoff with jitter. Without jitter, several components
			// that failed together (session init, cert refresh, the
			// reconnect loop) retry in lockstep and arrive at the API
			// as a burst, which is what a limiter reacts to.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoffWithJitter(attempt)):
			}
		}

		// Token used for this attempt, so a 401 handler can tell whether
		// someone else already rotated it while we were in flight.
		c.mu.RLock()
		usedToken := c.accessToken
		c.mu.RUnlock()

		err := c.doSingleRequest(ctx, method, path, body, result)
		if err == nil {
			return nil
		}

		lastErr = err

		reqErr, ok := err.(*RequestError)
		if !ok {
			continue // Network error, retry
		}

		switch reqErr.HTTPStatus {
		case 401:
			if refreshed {
				// We already refreshed once for this call and still got
				// a 401 — the session is dead, not stale.
				return err
			}
			refreshed = true
			// Try to refresh tokens
			if refreshErr := c.refreshTokens(ctx, usedToken); refreshErr != nil {
				// Check if the refresh itself got a permanent auth error
				// (e.g. error 10013 = refresh token revoked)
				if IsAuthError(refreshErr) {
					return refreshErr
				}
				return fmt.Errorf("token refresh failed: %w (original: %w)", refreshErr, err)
			}
			continue // Retry with new tokens

		case 429:
			// Rate limited. Do NOT retry: the cooldown is already armed
			// by doSingleRequest, and every further request we send
			// while limited pushes Proton's window further out. Hand the
			// error (with its Retry-After) to the caller and let it
			// decide when to come back.
			return err

		case 503:
			// Service unavailable — retry
			continue

		default:
			// Non-retryable error
			return err
		}
	}

	return fmt.Errorf("max retries exceeded: %w", lastErr)
}

// backoffWithJitter returns the pause before retry attempt n (1-based):
// roughly n seconds, spread over a +/-40% window.
func backoffWithJitter(attempt int) time.Duration {
	base := time.Duration(attempt) * time.Second
	jitter := time.Duration(rand.Int63n(int64(base*4/5+1))) - base*2/5
	return base + jitter
}

func (c *Client) doSingleRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	// Local rate-limit gate. Once Proton has told us to back off, nothing
	// this client would send is worth sending: the request cannot succeed
	// and its arrival extends the limit. Fail fast without touching the
	// network so the window can actually close.
	if remaining := c.RateLimitedFor(); remaining > 0 {
		return &RequestError{
			HTTPStatus: 429,
			Code:       2028,
			RetryAfter: remaining,
			Local:      true,
		}
	}

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-pm-appversion", AppVersion)
	req.Header.Set("User-Agent", UserAgent)

	c.mu.RLock()
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	if c.uid != "" {
		req.Header.Set("x-pm-uid", c.uid)
	}
	c.mu.RUnlock()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	// Check for API-level error
	if resp.StatusCode >= 400 {
		var retryAfter time.Duration
		if resp.StatusCode == 429 {
			retryAfter = parseRetryAfter(resp.Header)
			// Arm the cooldown before returning so every other caller on
			// this client stops sending immediately, not just this one.
			c.enterCooldown(retryAfter)
			if retryAfter == 0 {
				retryAfter = defaultRateLimitCooldown
			}
		}
		var apiErr APIError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Code != 0 {
			return &RequestError{
				HTTPStatus: resp.StatusCode,
				Code:       apiErr.Code,
				Message:    apiErr.Error,
				RetryAfter: retryAfter,
			}
		}
		return &RequestError{
			HTTPStatus: resp.StatusCode,
			Code:       0,
			Message:    string(respBody),
			RetryAfter: retryAfter,
		}
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}

// refreshTokens attempts to refresh the access token using the refresh
// token. staleToken is the access token the caller was using when it got
// its 401; if another goroutine has already rotated past it by the time
// we get the lock, we return success without spending a second refresh.
//
// Proton's refresh tokens are single-use and rotate on every successful
// refresh. Two concurrent refreshes mean the loser replays an already
// rotated token, which Proton treats as token reuse — it can invalidate
// the whole session and counts against per-account auth rate limits.
// This mutex makes refresh single-flight per client.
func (c *Client) refreshTokens(ctx context.Context, staleToken string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.mu.RLock()
	refreshToken := c.refreshToken
	currentAccess := c.accessToken
	lastRefresh := c.lastRefresh
	c.mu.RUnlock()

	// Someone else refreshed while we waited for the lock — the caller
	// should just retry with the token that is now installed.
	if staleToken != "" && currentAccess != staleToken {
		return nil
	}

	// A refresh just happened. Whoever is still getting 401s is not
	// suffering from a stale token, and rotating again would only spend
	// another single-use token against the auth rate limit.
	if !lastRefresh.IsZero() && time.Since(lastRefresh) < minRefreshInterval {
		return nil
	}

	if refreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	reqBody := RefreshRequest{
		ResponseType: "token",
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		RedirectURI:  "http://protonmail.ch",
	}

	var result RefreshResponse
	if err := c.doSingleRequest(ctx, http.MethodPost, "/auth/refresh", reqBody, &result); err != nil {
		return err
	}

	c.mu.Lock()
	c.accessToken = result.AccessToken
	c.refreshToken = result.RefreshToken
	c.lastRefresh = time.Now()
	c.mu.Unlock()

	if c.OnTokenRefresh != nil {
		c.OnTokenRefresh(c.uid, result.AccessToken, result.RefreshToken)
	}

	return nil
}

// VPN API methods

// GetVPNInfo returns the VPN account info.
func (c *Client) GetVPNInfo(ctx context.Context) (*VPNInfoResponse, error) {
	var result VPNInfoResponse
	err := c.doRequest(ctx, http.MethodGet, "/vpn/v2", nil, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetServers returns the full server list.
func (c *Client) GetServers(ctx context.Context) (*LogicalsResponse, error) {
	var result LogicalsResponse
	err := c.doRequest(ctx, http.MethodGet, "/vpn/v1/logicals?SecureCoreFilter=all", nil, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetClientConfig returns the client configuration.
func (c *Client) GetClientConfig(ctx context.Context) (*ClientConfigResponse, error) {
	var result ClientConfigResponse
	err := c.doRequest(ctx, http.MethodGet, "/vpn/v2/clientconfig", nil, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetLocation returns the client's current IP and location.
func (c *Client) GetLocation(ctx context.Context) (*LocationResponse, error) {
	var result LocationResponse
	err := c.doRequest(ctx, http.MethodGet, "/vpn/v1/location", nil, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetSessions returns active VPN sessions.
func (c *Client) GetSessions(ctx context.Context) (*SessionsResponse, error) {
	var result SessionsResponse
	err := c.doRequest(ctx, http.MethodGet, "/vpn/v1/sessions", nil, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// RequestCertificate requests a new VPN certificate.
func (c *Client) RequestCertificate(ctx context.Context, req *CertificateRequest) (*CertificateResponse, error) {
	var result CertificateResponse
	err := c.doRequest(ctx, http.MethodPost, "/vpn/v1/certificate", req, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetServerLoads fetches just the server loads (lighter than full server list).
func (c *Client) GetServerLoads(ctx context.Context) (*LogicalsResponse, error) {
	var result LogicalsResponse
	err := c.doRequest(ctx, http.MethodGet, "/vpn/v1/loads", nil, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}
