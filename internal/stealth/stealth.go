package stealth

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

const (
	InterfaceName = "pvpn0"
	// Stealth connects on TCP 443 to look like HTTPS
	DefaultStealthPort = 443
	// FWMark must match the value in vpn/wireguard.go
	FWMark     = 51820
	RouteTable = 51820
	DefaultMTU = 1320 // Lower than normal WG (1420) to account for TLS + TCP overhead
)

// StealthConfig holds everything needed for a stealth WireGuard-over-TLS connection.
type StealthConfig struct {
	PrivateKey string // Base64 X25519 private key
	PublicKey  string // Base64 X25519 server public key
	ServerIP   string // Server entry IP
	Port       int    // TCP port (default 443)
	SNI        string // TLS SNI (Server Name Indication)
	Address    string // VPN interface address (e.g., "10.2.0.2/32")
}

// StealthManager manages a WireGuard-over-TLS tunnel using wireguard-go userspace.
type StealthManager struct {
	tunDev  tun.Device
	wgDev   *device.Device
	bind    *TLSBind
	link    netlink.Link
	ifIndex int
	OnLog   func(string) // optional debug logger
}

func (m *StealthManager) log(format string, args ...interface{}) {
	if m.OnLog != nil {
		m.OnLog(fmt.Sprintf(format, args...))
	}
}

// NewStealthManager creates a new stealth tunnel manager.
func NewStealthManager() *StealthManager {
	return &StealthManager{}
}

// Up creates the TUN device, establishes TLS, and starts wireguard-go.
func (m *StealthManager) Up(cfg *StealthConfig) error {
	port := cfg.Port
	if port == 0 {
		port = DefaultStealthPort
	}

	sni := cfg.SNI
	if sni == "" {
		sni = generateSNI(cfg.ServerIP)
	}

	// Step 1: Create TUN device
	tunDev, err := tun.CreateTUN(InterfaceName, DefaultMTU)
	if err != nil {
		return fmt.Errorf("create TUN device: %w", err)
	}
	m.tunDev = tunDev

	// Get the netlink handle for the TUN interface (needed for routes/DNS)
	link, err := netlink.LinkByName(InterfaceName)
	if err != nil {
		tunDev.Close()
		return fmt.Errorf("get TUN link: %w", err)
	}
	m.link = link
	m.ifIndex = link.Attrs().Index

	// Assign IP address to TUN
	addr, err := netlink.ParseAddr(cfg.Address)
	if err != nil {
		m.cleanup()
		return fmt.Errorf("parse address %s: %w", cfg.Address, err)
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		m.cleanup()
		return fmt.Errorf("add address: %w", err)
	}

	// Bring TUN interface up
	if err := netlink.LinkSetUp(link); err != nil {
		m.cleanup()
		return fmt.Errorf("bring up TUN: %w", err)
	}

	// Step 2: Create TLS bind (WireGuard packets go over TLS instead of UDP)
	m.log("connecting TLS to %s:%d (SNI: %s)", cfg.ServerIP, port, sni)
	bind := NewTLSBind(cfg.ServerIP, port, sni)
	m.bind = bind

	// Step 3: Create wireguard-go device with our TLS bind
	// Silent logger — verbose logs corrupt bubbletea TUI on stderr
	logger := device.NewLogger(device.LogLevelError, "wg: ")
	wgDev := device.NewDevice(tunDev, bind, logger)
	m.wgDev = wgDev

	// Step 4: Configure wireguard-go via IPC (private key, peer, etc.)
	ipcConf := buildIpcConfig(cfg)
	if err := wgDev.IpcSet(ipcConf); err != nil {
		m.cleanup()
		return fmt.Errorf("configure wireguard-go: %w", err)
	}

	// Step 5: Bring up the WireGuard device
	if err := wgDev.Up(); err != nil {
		m.cleanup()
		return fmt.Errorf("bring up wireguard-go device: %w", err)
	}

	return nil
}

// Down tears down the stealth tunnel.
func (m *StealthManager) Down() error {
	return m.cleanup()
}

// IfIndex returns the TUN interface index.
func (m *StealthManager) IfIndex() int {
	return m.ifIndex
}

// Link returns the netlink.Link for the TUN interface.
func (m *StealthManager) Link() netlink.Link {
	return m.link
}

// Stats returns WireGuard peer stats. For stealth, we read from wireguard-go's IPC.
func (m *StealthManager) Stats() (rxBytes, txBytes int64, lastHandshake time.Time, err error) {
	if m.wgDev == nil {
		return 0, 0, time.Time{}, fmt.Errorf("not connected")
	}

	// Read device state via IPC get
	var buf strings.Builder
	if err := m.wgDev.IpcGetOperation(&buf); err != nil {
		return 0, 0, time.Time{}, fmt.Errorf("ipc get: %w", err)
	}

	// Parse the IPC output for peer stats
	for _, line := range strings.Split(buf.String(), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "rx_bytes":
			fmt.Sscanf(parts[1], "%d", &rxBytes)
		case "tx_bytes":
			fmt.Sscanf(parts[1], "%d", &txBytes)
		case "last_handshake_time_sec":
			var sec int64
			fmt.Sscanf(parts[1], "%d", &sec)
			if sec > 0 {
				lastHandshake = time.Unix(sec, 0)
			}
		}
	}

	return rxBytes, txBytes, lastHandshake, nil
}

// Close releases resources.
func (m *StealthManager) Close() error {
	return m.cleanup()
}

func (m *StealthManager) cleanup() error {
	var firstErr error

	// Close bind FIRST to unblock any readers stuck in io.ReadFull on TLS.
	// This must happen before wgDev.Close() which waits for workers to stop.
	if m.bind != nil {
		m.bind.Close()
		m.bind = nil
	}

	if m.wgDev != nil {
		// wgDev.Close() waits for all goroutines — use a timeout to prevent hang
		done := make(chan struct{})
		go func() {
			m.wgDev.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			// Device close timed out — workers may be stuck
		}
		m.wgDev = nil
	}

	if m.tunDev != nil {
		if err := m.tunDev.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		m.tunDev = nil
	}

	// Delete the interface if it still exists
	if m.link != nil {
		if err := netlink.LinkDel(m.link); err != nil && firstErr == nil {
			if !isNoSuchDevice(err) {
				firstErr = err
			}
		}
		m.link = nil
		m.ifIndex = 0
	}

	return firstErr
}

// buildIpcConfig generates the wireguard-go UAPI configuration string.
func buildIpcConfig(cfg *StealthConfig) string {
	// Decode base64 keys to hex (wireguard-go IPC uses hex)
	privKeyHex := base64ToHex(cfg.PrivateKey)
	pubKeyHex := base64ToHex(cfg.PublicKey)

	port := cfg.Port
	if port == 0 {
		port = DefaultStealthPort
	}

	// The endpoint for wireguard-go is the server IP:port.
	// The TLS bind will handle the actual connection.
	endpoint := fmt.Sprintf("%s:%d", cfg.ServerIP, port)

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", privKeyHex)
	fmt.Fprintf(&b, "fwmark=%d\n", FWMark)
	fmt.Fprintf(&b, "public_key=%s\n", pubKeyHex)
	fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
	fmt.Fprintf(&b, "allowed_ip=0.0.0.0/0\n")
	fmt.Fprintf(&b, "persistent_keepalive_interval=25\n")

	return b.String()
}

// base64ToHex converts a base64-encoded key to hex (as expected by wireguard-go IPC).
func base64ToHex(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}
	hex := fmt.Sprintf("%x", raw)
	return hex
}

// Proton's SNI domain list — real domains that look like normal HTTPS traffic.
// Sourced from ProtonVPN/wireguard-go conn/server_name_utils/server_name_utils.go
var sniDomains = []string{
	"accounts.google.com",
	"android.googleapis.com",
	"api.amazon.com",
	"api.ipify.org",
	"cdn.ampproject.org",
	"cdn.cookielaw.org",
	"client.wns.windows.com",
	"cloudflare.com",
	"cloudflare-dns.com",
	"connectivitycheck.gstatic.com",
	"dl.google.com",
	"dns.google",
	"edge.microsoft.com",
	"fonts.googleapis.com",
	"fonts.gstatic.com",
	"github.com",
	"graph.microsoft.com",
	"login.live.com",
	"login.microsoftonline.com",
	"outlook.office365.com",
	"play.googleapis.com",
	"raw.githubusercontent.com",
	"s3.amazonaws.com",
	"safebrowsing.googleapis.com",
	"ssl.gstatic.com",
	"update.googleapis.com",
	"www.googleapis.com",
	"www.gstatic.com",
	"www.msftconnecttest.com",
}

// generateSNI picks an SNI from Proton's domain list based on the server IP.
// Deterministic per server so reconnects use the same SNI.
func generateSNI(serverIP string) string {
	ip := net.ParseIP(serverIP)
	if ip == nil {
		return sniDomains[0]
	}
	v4 := ip.To4()
	if v4 == nil {
		return sniDomains[0]
	}
	// Use last two octets for more spread
	idx := (int(v4[2])*256 + int(v4[3])) % len(sniDomains)
	return sniDomains[idx]
}

func isNoSuchDevice(err error) bool {
	return strings.Contains(err.Error(), "no such device") ||
		strings.Contains(err.Error(), "not found")
}
