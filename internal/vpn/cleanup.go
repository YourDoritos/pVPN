package vpn

import (
	"net"
	"os/exec"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/grennboy527/pvpn/internal/network"
)

// CleanupIfNoTunnel checks if pvpn0 is still alive. If it is (kernel WG),
// the VPN is working fine without us — leave it alone. If pvpn0 is gone
// (stealth userspace WG died with the process), clean up leftover rules.
func CleanupIfNoTunnel() {
	if _, err := netlink.LinkByName(InterfaceName); err == nil {
		// Interface exists — kernel WG VPN is still running, don't touch it
		return
	}
	// Interface is gone but rules may linger — clean up
	ForceCleanup()
}

// ForceCleanup removes any leftover VPN state (interface, routes, rules, nftables, DNS).
// This is a safety net to ensure the host network is never left broken.
func ForceCleanup() {
	// Always revert DNS — even if the interface is already gone.
	// The DirectBackend checks for the backup file on disk, so this works
	// after a hard kill where in-memory state is lost.
	if backend, err := network.DetectBackend(); err == nil {
		backend.RevertDNS(0) // ifIndex doesn't matter for DirectBackend
	}

	// Remove pvpn0 interface if it still exists
	if link, err := netlink.LinkByName(InterfaceName); err == nil {
		netlink.LinkDel(link)
	}

	// Remove ip rules referencing our routing table or fwmark
	cleanupRules()

	// Remove routes from our custom table
	cleanupRoutes()

	// Remove nftables kill switch table
	exec.Command("nft", "delete", "table", "inet", "pvpn_killswitch").Run()
}

func cleanupRules() {
	rules, err := netlink.RuleList(unix.AF_INET)
	if err != nil {
		return
	}
	for _, r := range rules {
		if r.Table == RouteTable || r.Mark == FWMark {
			netlink.RuleDel(&r)
		}
	}

	// IPv6 rules too
	rules6, err := netlink.RuleList(unix.AF_INET6)
	if err != nil {
		return
	}
	for _, r := range rules6 {
		if r.Table == RouteTable || r.Mark == FWMark {
			netlink.RuleDel(&r)
		}
	}
}

func cleanupRoutes() {
	_, allIPv4, _ := net.ParseCIDR("0.0.0.0/0")
	netlink.RouteDel(&netlink.Route{
		Dst:   allIPv4,
		Table: RouteTable,
	})

	_, allIPv6, _ := net.ParseCIDR("::/0")
	netlink.RouteDel(&netlink.Route{
		Dst:   allIPv6,
		Table: RouteTable,
		Type:  unix.RTN_BLACKHOLE,
	})
}
