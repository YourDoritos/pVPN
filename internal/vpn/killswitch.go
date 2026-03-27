package vpn

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

const (
	tableName = "pvpn_killswitch"
)

// KillSwitch manages nftables rules that block all non-VPN traffic.
type KillSwitch struct {
	enabled bool
}

// NewKillSwitch creates a kill switch manager.
func NewKillSwitch() (*KillSwitch, error) {
	// Verify nft is available
	if _, err := exec.LookPath("nft"); err != nil {
		return nil, fmt.Errorf("nft not found: %w (is nftables installed?)", err)
	}
	return &KillSwitch{}, nil
}

// Enable activates the kill switch, allowing only VPN and LAN traffic.
func (ks *KillSwitch) Enable(serverIP net.IP) error {
	// Remove any existing rules first (idempotent)
	ks.Disable()

	rules := buildRules(serverIP)
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft apply failed: %w: %s", err, string(output))
	}

	ks.enabled = true
	return nil
}

// Disable removes the kill switch rules.
func (ks *KillSwitch) Disable() error {
	// Delete the table (removes all chains and rules within it)
	cmd := exec.Command("nft", "delete", "table", "inet", tableName)
	cmd.CombinedOutput() // Ignore error (table might not exist)
	ks.enabled = false
	return nil
}

// IsEnabled returns whether the kill switch is active.
func (ks *KillSwitch) IsEnabled() bool {
	return ks.enabled
}

func buildRules(serverIP net.IP) string {
	return fmt.Sprintf(`table inet %s {
    chain output {
        type filter hook output priority 0; policy drop;

        # Allow loopback
        oif "lo" accept

        # Allow VPN interface
        oif "%s" accept

        # Allow traffic to VPN server (so we can reach it)
        ip daddr %s accept

        # Allow LAN traffic
        ip daddr 10.0.0.0/8 accept
        ip daddr 172.16.0.0/12 accept
        ip daddr 192.168.0.0/16 accept

        # Allow DHCP lease renewal
        udp dport { 67, 68 } accept
        ip6 daddr fe80::/10 udp dport { 546, 547 } accept

        # Allow established connections (for existing sessions)
        ct state established,related accept

        # Drop everything else
        drop
    }
}
`, tableName, InterfaceName, serverIP.String())
}
