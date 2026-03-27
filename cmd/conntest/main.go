package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/grennboy527/pvpn/internal/api"
	"github.com/grennboy527/pvpn/internal/config"
	"github.com/grennboy527/pvpn/internal/network"
	"github.com/grennboy527/pvpn/internal/vpn"
)

func main() {
	if os.Geteuid() != 0 {
		fmt.Println("This test requires root. Run with sudo.")
		os.Exit(1)
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Load session
	store, err := api.NewSessionStore(config.SessionFile())
	if err != nil {
		return fmt.Errorf("session store: %w", err)
	}
	session, err := store.Load()
	if err != nil || session == nil {
		return fmt.Errorf("no saved session (run pvpn to login first): %v", err)
	}

	client := api.NewClient(session)

	// Verify session
	fmt.Println("Verifying session...")
	vpnInfo, err := client.GetVPNInfo(ctx)
	if err != nil {
		return fmt.Errorf("session invalid: %w", err)
	}
	fmt.Printf("Account: %s (tier %d)\n", vpnInfo.VPN.PlanTitle, vpnInfo.VPN.MaxTier)

	// Get current location
	fmt.Println("\nChecking current IP...")
	loc, err := client.GetLocation(ctx)
	if err != nil {
		return fmt.Errorf("get location: %w", err)
	}
	fmt.Printf("Current IP: %s (%s, %s)\n", loc.IP, loc.Country, loc.ISP)
	originalIP := loc.IP

	// Find a fast NL server
	fmt.Println("\nFinding best server...")
	servers, err := client.GetServers(ctx)
	if err != nil {
		return fmt.Errorf("get servers: %w", err)
	}

	server := api.FindFastestServer(servers.LogicalServers, api.ServerFilter{
		Country:    "NL",
		OnlineOnly: true,
	}, vpnInfo.VPN.MaxTier)
	if server == nil {
		return fmt.Errorf("no suitable server found")
	}

	ps := server.BestServer()
	if ps == nil {
		return fmt.Errorf("no online physical server")
	}
	fmt.Printf("Selected: %s (%s, load: %d%%, IP: %s)\n", server.Name, server.ExitCountry, server.Load, ps.EntryIP)

	// Generate keys and cert
	fmt.Println("\nGenerating keys and requesting certificate...")
	kp, err := api.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("keygen: %w", err)
	}

	cert, err := client.RequestCert(ctx, kp, api.CertificateFeatures{SplitTCP: true})
	if err != nil {
		return fmt.Errorf("certificate: %w", err)
	}
	fmt.Printf("Certificate obtained (expires: %s)\n", cert.ExpiresAt().Format(time.RFC822))

	// Detect DNS backend
	fmt.Println("\nDetecting network backend...")
	dnsBackend, err := network.DetectBackend()
	if err != nil {
		return fmt.Errorf("detect backend: %w", err)
	}
	fmt.Printf("DNS backend: %s\n", dnsBackend.Name())

	// Connect
	fmt.Println("\nConnecting to VPN...")
	conn := vpn.NewConnection(client, dnsBackend)
	conn.OnStateChange(func(state vpn.State) {
		fmt.Printf("  State: %s\n", state)
	})
	conn.OnLog(func(msg string) {
		fmt.Printf("  [log] %s\n", msg)
	})

	// Set up signal-based cleanup BEFORE connecting so it catches interrupts during connect
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	certFeatures := api.CertificateFeatures{SplitTCP: true}
	// Kill switch disabled during testing to avoid locking out SSH/network
	// Use stealth if --stealth flag or config says so
	protocol := "wireguard"
	for _, arg := range os.Args[1:] {
		if arg == "--stealth" || arg == "-stealth" {
			protocol = "stealth"
		}
	}
	fmt.Printf("Protocol: %s\n", protocol)

	if err := conn.Connect(ctx, server, kp, cert, certFeatures, false, protocol, nil); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	// Ensure cleanup on any exit
	defer func() {
		fmt.Println("\nDisconnecting...")
		if err := conn.Disconnect(); err != nil {
			fmt.Fprintf(os.Stderr, "disconnect error: %v\n", err)
		}
		fmt.Println("Disconnected.")
	}()

	time.Sleep(2 * time.Second)

	// Debug state
	fmt.Println("\n--- Network State ---")
	debugCmd("ip", "rule", "list")
	debugCmd("ip", "route", "show", "table", "51820")
	debugCmd("ip", "link", "show", "pvpn0")

	// WireGuard stats
	stats, err := conn.Stats()
	if err != nil {
		fmt.Printf("Warning: no stats: %v\n", err)
	} else {
		fmt.Printf("WireGuard: TX=%d RX=%d handshake=%s\n",
			stats.TxBytes, stats.RxBytes, stats.LastHandshake.Format(time.RFC822))
	}

	// Test tunnel connectivity by reaching Proton DNS directly (no resolv.conf needed)
	fmt.Println("\nTesting tunnel connectivity to 10.2.0.1 (Proton DNS)...")
	if testUDP("10.2.0.1:53") {
		fmt.Println("Tunnel is working! Proton DNS reachable.")
	} else {
		fmt.Println("WARNING: Cannot reach Proton DNS through tunnel.")
	}

	// Check public IP using a custom resolver through the tunnel
	fmt.Println("\nChecking VPN IP...")
	newIP, err := checkPublicIPViaResolver("10.2.0.1:53")
	if err != nil {
		fmt.Printf("Could not verify IP change: %v\n", err)
		// Try direct IP-based check
		newIP, err = checkPublicIPDirect()
		if err != nil {
			fmt.Printf("Direct IP check also failed: %v\n", err)
		}
	}
	if newIP != "" {
		fmt.Printf("VPN IP: %s\n", newIP)
		if newIP != originalIP {
			fmt.Println("IP CHANGED — VPN is working!")
		} else {
			fmt.Println("WARNING: IP did not change!")
		}
	}

	// Wait for signal
	fmt.Println("\nVPN connected. Press Ctrl+C to disconnect...")
	<-sigCh

	return nil
}

func debugCmd(name string, args ...string) {
	out, err := exec.Command(name, args...).CombinedOutput()
	fmt.Printf("$ %s %s\n%s", name, strings.Join(args, " "), string(out))
	if err != nil {
		fmt.Printf("  (error: %v)\n", err)
	}
}

// testUDP tests if a UDP endpoint is reachable by sending a DNS query.
func testUDP(addr string) bool {
	conn, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()

	// Send a minimal DNS query for example.com
	query := []byte{
		0x00, 0x01, // ID
		0x01, 0x00, // Flags: standard query
		0x00, 0x01, // Questions: 1
		0x00, 0x00, // Answers: 0
		0x00, 0x00, // Authority: 0
		0x00, 0x00, // Additional: 0
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x03, 'c', 'o', 'm',
		0x00,       // Root
		0x00, 0x01, // Type: A
		0x00, 0x01, // Class: IN
	}

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(query); err != nil {
		return false
	}

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	return err == nil && n > 0
}

// checkPublicIPViaResolver resolves api.ipify.org via a specific DNS server,
// then fetches the IP. This avoids depending on the system's resolv.conf.
func checkPublicIPViaResolver(dnsAddr string) (string, error) {
	// Resolve api.ipify.org using the VPN's DNS
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, netw, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", dnsAddr)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ips, err := resolver.LookupIPAddr(ctx, "api.ipify.org")
	if err != nil {
		return "", fmt.Errorf("DNS lookup via %s: %w", dnsAddr, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no IPs returned for api.ipify.org")
	}

	// Fetch public IP
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/", ips[0].IP.String()))
	if err != nil {
		// Try HTTPS with Host header
		req, _ := http.NewRequest("GET", fmt.Sprintf("https://%s/", ips[0].IP.String()), nil)
		req.Host = "api.ipify.org"
		resp, err = client.Do(req)
		if err != nil {
			return "", fmt.Errorf("fetch IP: %w", err)
		}
	}
	defer resp.Body.Close()

	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	return strings.TrimSpace(string(buf[:n])), nil
}

// checkPublicIPDirect uses a known IP for ifconfig.me to check public IP.
func checkPublicIPDirect() (string, error) {
	// ifconfig.me resolves to well-known IPs
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "http://34.117.59.81/", nil)
	req.Host = "ifconfig.me"
	req.Header.Set("User-Agent", "curl/8.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	return strings.TrimSpace(string(buf[:n])), nil
}
