package api

import (
	"context"
	"fmt"
	"time"

	"github.com/ProtonVPN/go-vpn-lib/ed25519"
)

// KeyPair holds the generated Ed25519/X25519 keys for VPN use.
type KeyPair struct {
	// Ed25519 key pair (used for API cert requests and Local Agent mTLS)
	Ed25519 *ed25519.KeyPair

	// X25519 private key in base64 (used as WireGuard private key)
	WireGuardPrivateKey string

	// Ed25519 public key in PEM format (sent to API)
	PublicKeyPEM string
}

// GenerateKeyPair creates a new Ed25519 key pair and derives the X25519
// WireGuard private key from it.
func GenerateKeyPair() (*KeyPair, error) {
	kp, err := ed25519.NewKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 keypair: %w", err)
	}

	wgPrivKey := kp.ToX25519Base64()
	pubKeyPEM, err := kp.PublicKeyPKIXPem()
	if err != nil {
		return nil, fmt.Errorf("encode public key PEM: %w", err)
	}

	return &KeyPair{
		Ed25519:             kp,
		WireGuardPrivateKey: wgPrivKey,
		PublicKeyPEM:        pubKeyPEM,
	}, nil
}

// RequestCert requests a VPN certificate from the API using the given key pair.
func (c *Client) RequestCert(ctx context.Context, kp *KeyPair, features CertificateFeatures) (*CertificateResponse, error) {
	req := &CertificateRequest{
		ClientPublicKey:     kp.PublicKeyPEM,
		ClientPublicKeyMode: "EC",
		Mode:                "persistent",
		DeviceName:          fmt.Sprintf("pvpn-%d", time.Now().Unix()),
		Duration:            "10080 min", // 7 days
		Features:            features,
	}

	return c.RequestCertificate(ctx, req)
}

// CertRefresher manages automatic certificate rotation in the background.
type CertRefresher struct {
	client   *Client
	keyPair  *KeyPair
	features CertificateFeatures
	cert     *CertificateResponse
	stopCh   chan struct{}
	doneCh   chan struct{}

	// Called when a new certificate is obtained.
	OnCertRefresh func(cert *CertificateResponse)
}

// NewCertRefresher creates a new certificate refresher.
func NewCertRefresher(client *Client, kp *KeyPair, features CertificateFeatures) *CertRefresher {
	return &CertRefresher{
		client:   client,
		keyPair:  kp,
		features: features,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start begins the certificate refresh loop. It immediately requests a cert,
// then refreshes at the API-specified RefreshTime.
func (r *CertRefresher) Start(ctx context.Context) error {
	// Get initial certificate
	cert, err := r.client.RequestCert(ctx, r.keyPair, r.features)
	if err != nil {
		return fmt.Errorf("initial certificate request: %w", err)
	}
	r.cert = cert
	if r.OnCertRefresh != nil {
		r.OnCertRefresh(cert)
	}

	// Start background refresh loop
	go r.refreshLoop(ctx)
	return nil
}

// Stop halts the refresh loop.
func (r *CertRefresher) Stop() {
	close(r.stopCh)
	<-r.doneCh
}

// CurrentCert returns the current certificate.
func (r *CertRefresher) CurrentCert() *CertificateResponse {
	return r.cert
}

func (r *CertRefresher) refreshLoop(ctx context.Context) {
	defer close(r.doneCh)

	for {
		// Calculate time until next refresh
		refreshAt := r.cert.RefreshAt()
		delay := time.Until(refreshAt)

		// Minimum validity check
		if delay < 300*time.Second {
			delay = 300 * time.Second
		}

		select {
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Refresh certificate
		cert, err := r.client.RequestCert(ctx, r.keyPair, r.features)
		if err != nil {
			// On failure, retry with exponential backoff
			r.handleRefreshError(ctx)
			continue
		}

		r.cert = cert
		if r.OnCertRefresh != nil {
			r.OnCertRefresh(cert)
		}
	}
}

func (r *CertRefresher) handleRefreshError(ctx context.Context) {
	backoff := 30 * time.Second
	maxBackoff := 7 * 24 * time.Hour // REFRESH_INTERVAL

	for attempt := 0; ; attempt++ {
		select {
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		cert, err := r.client.RequestCert(ctx, r.keyPair, r.features)
		if err == nil {
			r.cert = cert
			if r.OnCertRefresh != nil {
				r.OnCertRefresh(cert)
			}
			return
		}

		// Exponential backoff: 30s, 60s, 120s, ...
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
