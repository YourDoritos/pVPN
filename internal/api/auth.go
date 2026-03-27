package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/ProtonMail/go-srp"
)

// Login authenticates with the Proton API using SRP.
// Returns the auth response. If 2FA is required, the caller must call Submit2FA.
func (c *Client) Login(ctx context.Context, username, password string) (*AuthResponse, error) {
	// Step 1: Get auth info (SRP parameters)
	info, err := c.getAuthInfo(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("get auth info: %w", err)
	}

	// Step 2: Generate SRP proofs
	proofs, err := srp.NewAuth(
		info.Version,
		username,
		[]byte(password),
		info.Salt,
		info.Modulus,
		info.ServerEphemeral,
	)
	if err != nil {
		return nil, fmt.Errorf("SRP auth init: %w", err)
	}

	clientProofs, err := proofs.GenerateProofs(2048)
	if err != nil {
		return nil, fmt.Errorf("SRP generate proofs: %w", err)
	}

	// Step 3: Submit auth request
	authReq := AuthRequest{
		Username:        username,
		ClientEphemeral: base64.StdEncoding.EncodeToString(clientProofs.ClientEphemeral),
		ClientProof:     base64.StdEncoding.EncodeToString(clientProofs.ClientProof),
		SRPSession:      info.SRPSession,
	}

	var authResp AuthResponse
	if err := c.doSingleRequest(ctx, http.MethodPost, "/core/v4/auth", authReq, &authResp); err != nil {
		return nil, fmt.Errorf("auth request: %w", err)
	}

	// Step 4: Verify server proof
	expectedProof := base64.StdEncoding.EncodeToString(clientProofs.ExpectedServerProof)
	if authResp.ServerProof != expectedProof {
		return nil, fmt.Errorf("server proof verification failed")
	}

	// Step 5: Store session
	c.SetSession(authResp.UID, authResp.AccessToken, authResp.RefreshToken)
	c.mu.Lock()
	c.loginEmail = username
	c.mu.Unlock()

	return &authResp, nil
}

// Needs2FA checks if the auth response requires 2FA to proceed.
func Needs2FA(auth *AuthResponse) bool {
	// Check if 2FA is enabled
	if auth.TwoFA.Enabled == 0 {
		return false
	}

	// Check if VPN scope is already granted
	for _, scope := range auth.Scopes {
		if scope == "vpn" {
			return false
		}
	}

	// Also check the space-separated Scope field
	if strings.Contains(auth.Scope, "vpn") {
		return false
	}

	return true
}

// Submit2FA submits a TOTP 2FA code to complete authentication.
func (c *Client) Submit2FA(ctx context.Context, code string) error {
	req := Auth2FARequest{
		TwoFactorCode: code,
	}

	var resp Auth2FAResponse
	if err := c.doRequest(ctx, http.MethodPost, "/core/v4/auth/2fa", req, &resp); err != nil {
		return fmt.Errorf("2FA submission: %w", err)
	}

	return nil
}

// Logout invalidates the current session.
func (c *Client) Logout(ctx context.Context) error {
	err := c.doRequest(ctx, http.MethodDelete, "/auth/v4", nil, nil)
	c.SetSession("", "", "")
	return err
}

func (c *Client) getAuthInfo(ctx context.Context, username string) (*AuthInfoResponse, error) {
	req := AuthInfoRequest{
		Username: username,
		Intent:   "Proton",
	}

	var info AuthInfoResponse
	if err := c.doSingleRequest(ctx, http.MethodPost, "/core/v4/auth/info", req, &info); err != nil {
		return nil, err
	}

	return &info, nil
}
