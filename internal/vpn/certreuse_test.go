package vpn

import (
	"testing"
	"time"

	"github.com/YourDoritos/pvpn/internal/api"
)

func connWithCert(expiresIn time.Duration) *Connection {
	c := &Connection{wakeCh: make(chan struct{}, 1)}
	c.lastKeyPair = &api.KeyPair{WireGuardPrivateKey: "k"}
	c.lastCert = &api.CertificateResponse{
		Certificate:    "cert",
		ExpirationTime: time.Now().Add(expiresIn).Unix(),
	}
	return c
}

// A certificate with plenty of life left is reused, so a reconnect after
// a wake or link flap costs no API call at all.
func TestReusableCredentials_ReusesValidCert(t *testing.T) {
	c := connWithCert(6 * 24 * time.Hour)
	kp, cert := c.reusableCredentials()
	if kp == nil || cert == nil {
		t.Fatal("a certificate valid for 6 days was not reused")
	}
}

// Near expiry we mint a fresh one rather than race the clock.
func TestReusableCredentials_RefusesNearExpiry(t *testing.T) {
	c := connWithCert(certReuseMargin / 2)
	if kp, _ := c.reusableCredentials(); kp != nil {
		t.Errorf("certificate within %v of expiry was reused", certReuseMargin)
	}
}

func TestReusableCredentials_RefusesExpired(t *testing.T) {
	c := connWithCert(-time.Hour)
	if kp, _ := c.reusableCredentials(); kp != nil {
		t.Error("expired certificate was reused")
	}
}

func TestReusableCredentials_NoneStored(t *testing.T) {
	c := &Connection{wakeCh: make(chan struct{}, 1)}
	if kp, _ := c.reusableCredentials(); kp != nil {
		t.Error("reused credentials that were never stored")
	}
}

// A cert with no expiry field (malformed response) is never reused.
func TestReusableCredentials_RefusesZeroExpiry(t *testing.T) {
	c := connWithCert(time.Hour)
	c.lastCert.ExpirationTime = 0
	if kp, _ := c.reusableCredentials(); kp != nil {
		t.Error("certificate with no expiration was reused")
	}
}

// If the server rejects a reused certificate, it must be dropped so the
// next attempt mints a fresh one instead of replaying a bad credential.
func TestInvalidateCredentials(t *testing.T) {
	c := connWithCert(6 * 24 * time.Hour)
	c.invalidateCredentials()
	if kp, cert := c.reusableCredentials(); kp != nil || cert != nil {
		t.Error("credentials survived invalidation")
	}
}
