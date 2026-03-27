package network

import "net"

// DNSBackend is the interface for managing DNS configuration.
// Implementations exist for NetworkManager, systemd-resolved, and direct resolv.conf.
type DNSBackend interface {
	// Name returns a human-readable name for this backend.
	Name() string

	// SetDNS configures DNS servers on the VPN interface.
	SetDNS(ifIndex int, servers []net.IP) error

	// RevertDNS restores the original DNS configuration.
	RevertDNS(ifIndex int) error
}
