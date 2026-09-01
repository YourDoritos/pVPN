package vpn

// F-17 regression guard: the VPN server endpoint must get a `throw` route
// in the VPN table, so strict reverse-path filtering (which pVPN itself
// switches on in sysctl.go) cannot drop the inbound WireGuard handshake
// reply — while every other destination stays on the tunnel, keeping the
// F-13 TunnelVision mitigation intact.
//
// Unlike the rest of routes_test.go this touches real netlink, so it is
// gated twice: it needs root AND an explicit opt-in, because running it
// in the host's namespace would install live ip rules on the machine.
//
//	sudo ip netns add pvpntest
//	sudo ip netns exec pvpntest env PVPN_NETNS_TEST=1 go test -run NetNS ./internal/vpn/
//	sudo ip netns del pvpntest

import (
	"net"
	"os"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestNetNS_ThrowRouteForServerEndpoint(t *testing.T) {
	if os.Getenv("PVPN_NETNS_TEST") != "1" {
		t.Skip("set PVPN_NETNS_TEST=1 inside a throwaway netns to run this")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}

	la := netlink.NewLinkAttrs()
	la.Name = "pvpn0"
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: la}); err != nil {
		t.Fatalf("create dummy pvpn0: %v", err)
	}
	link, err := netlink.LinkByName("pvpn0")
	if err != nil {
		t.Fatalf("lookup pvpn0: %v", err)
	}
	addr, err := netlink.ParseAddr("10.2.0.2/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		t.Fatalf("addr add: %v", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("link up: %v", err)
	}

	// A public endpoint, i.e. one the LAN snapshot cannot cover. Using an
	// on-subnet address here would mask the bug entirely.
	serverIP := net.ParseIP("203.0.113.7")

	rm := NewRouteManager(link, serverIP)
	if err := rm.Up(); err != nil {
		t.Fatalf("RouteManager.Up: %v", err)
	}
	defer rm.Down()

	// The throw route must be present, /32, and RTN_THROW.
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4,
		&netlink.Route{Table: RouteTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatalf("list table %d: %v", RouteTable, err)
	}
	var throw *netlink.Route
	for i := range routes {
		if routes[i].Dst != nil && routes[i].Dst.IP.Equal(serverIP.To4()) {
			throw = &routes[i]
		}
	}
	if throw == nil {
		t.Fatalf("no route for %s in table %d (table: %v)", serverIP, RouteTable, routes)
	}
	if throw.Type != unix.RTN_THROW {
		t.Errorf("route type = %d, want RTN_THROW (%d)", throw.Type, unix.RTN_THROW)
	}
	if ones, _ := throw.Dst.Mask.Size(); ones != 32 {
		t.Errorf("throw route is /%d, want /32 — a wider mask would pull traffic off the tunnel", ones)
	}

	// The server must resolve OFF the tunnel, or the reverse path stays
	// wrong and strict rp_filter keeps dropping the handshake reply.
	srv, err := netlink.RouteGet(serverIP)
	if err != nil {
		t.Fatalf("route get %s: %v", serverIP, err)
	}
	for _, r := range srv {
		if r.LinkIndex == link.Attrs().Index {
			t.Errorf("server %s still resolves via pvpn0 — throw route is not taking effect", serverIP)
		}
	}

	// F-13 guard: everything else must still resolve INTO the tunnel,
	// including a neighbour of the server IP.
	for _, dst := range []string{"1.1.1.1", "8.8.8.8", "203.0.113.8"} {
		res, err := netlink.RouteGet(net.ParseIP(dst))
		if err != nil {
			t.Fatalf("route get %s: %v", dst, err)
		}
		onTunnel := false
		for _, r := range res {
			if r.LinkIndex == link.Attrs().Index {
				onTunnel = true
			}
		}
		if !onTunnel {
			t.Errorf("LEAK: %s no longer resolves via pvpn0", dst)
		}
	}

	// Teardown must take the throw route with it.
	if err := rm.Down(); err != nil {
		t.Logf("Down: %v", err)
	}
	after, err := netlink.RouteListFiltered(netlink.FAMILY_V4,
		&netlink.Route{Table: RouteTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatalf("list table after Down: %v", err)
	}
	for _, r := range after {
		if r.Dst != nil && r.Dst.IP.Equal(serverIP.To4()) {
			t.Errorf("throw route survived Down(): %v", r)
		}
	}
}
