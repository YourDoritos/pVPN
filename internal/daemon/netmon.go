package daemon

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// networkMonitor watches for link state and address changes via
// netlink and triggers an immediate reconnect check when the physical
// network topology changes under the active VPN tunnel.
//
// Why this exists (pVPN F-12): before v0.2.1 the only network-change
// trigger the daemon had was systemd-logind's PrepareForSleep D-Bus
// signal. That meant if a user pulled their ethernet cable, switched
// WiFi networks, or if NetworkManager reconfigured the NIC while
// awake, the daemon would not notice until the WireGuard monitor's
// 10-second handshake poll eventually decided the tunnel was stale —
// which in the worst case was ~2 minutes 41 seconds (measured on the
// Arch VM during F-12 verification). During that window, app traffic
// is routed at the kernel level to a tunnel interface that exists but
// has no working peer, so packets are dropped silently. Not a leak,
// but a user-visible "my VPN is still on but nothing works" state.
//
// netlink gives us sub-second notification of RTM_NEWLINK (carrier
// state changes) and RTM_NEWADDR / RTM_DELADDR (IP lease changes).
// We debounce all of these onto a single onChange callback so a flurry
// of simultaneous events from NetworkManager doesn't hammer the
// connection layer with reconnect requests.
type networkMonitor struct {
	onChange func()
	stop     chan struct{}
	wg       sync.WaitGroup

	// seenAddrs deduplicates RTM_NEWADDR notifications for addresses
	// that are already on an interface. The kernel re-emits NEWADDR
	// every time an IPv6 SLAAC lifetime is refreshed by a router
	// advertisement (typically every 3-10s on home networks), and
	// every refresh would otherwise trigger a reconnect check and
	// eventually rate-limit us off Proton's cert API.
	addrMu    sync.Mutex
	seenAddrs map[string]bool
}

// newNetworkMonitor starts netlink subscriptions and returns a running
// monitor. Returns nil (without error) if netlink is unavailable — the
// daemon falls back to the old WireGuard-handshake polling behavior,
// which is slower but still correct.
func newNetworkMonitor(onChange func()) *networkMonitor {
	m := &networkMonitor{
		onChange:  onChange,
		stop:      make(chan struct{}),
		seenAddrs: make(map[string]bool),
	}

	linkCh := make(chan netlink.LinkUpdate, 16)
	if err := netlink.LinkSubscribe(linkCh, m.stop); err != nil {
		log.Printf("Network monitor: link subscribe failed: %v", err)
		return nil
	}

	addrCh := make(chan netlink.AddrUpdate, 16)
	if err := netlink.AddrSubscribe(addrCh, m.stop); err != nil {
		log.Printf("Network monitor: addr subscribe failed: %v", err)
		return nil
	}

	// Debounce channel: collapses a burst of events (NM reconfiguring
	// a NIC often emits 6-10 in a row) into a single onChange call
	// within the debounce window.
	trigger := make(chan struct{}, 1)

	m.wg.Add(3)

	// Link watcher: carrier up/down, admin up/down.
	go func() {
		defer m.wg.Done()
		for {
			select {
			case u, ok := <-linkCh:
				if !ok {
					return
				}
				// Ignore changes to pVPN's own tunnel interface — it
				// goes up and down as part of normal connect /
				// teardown and reacting to that would cause a
				// reconnect loop.
				if u.Link != nil && u.Link.Attrs().Name == "pvpn0" {
					continue
				}
				// Filter to events that actually matter: link
				// becoming up, becoming down, or losing carrier.
				flags := u.Flags
				isUp := flags&unix.IFF_UP != 0
				hasCarrier := flags&unix.IFF_LOWER_UP != 0
				log.Printf("Network monitor: link %q state=%s carrier=%v",
					u.Link.Attrs().Name, boolFlag(isUp, "up", "down"), hasCarrier)
				m.kick(trigger)
			case <-m.stop:
				return
			}
		}
	}()

	// Address watcher: IP assigned or removed (DHCP lease change,
	// manual ifconfig, network switch).
	go func() {
		defer m.wg.Done()
		for {
			select {
			case u, ok := <-addrCh:
				if !ok {
					return
				}
				// Skip the tunnel interface itself and skip IPv6
				// link-local thrash (fe80::/10) which fires constantly
				// during normal operation.
				if u.LinkAddress.IP != nil && u.LinkAddress.IP.IsLinkLocalUnicast() {
					continue
				}
				// Drop pure refreshes — IPv6 RAs re-emit RTM_NEWADDR
				// for an address already on the interface every few
				// seconds. Only kick the debouncer on actual add/del
				// transitions.
				if !m.transitionedAddr(u.LinkIndex, u.LinkAddress.String(), u.NewAddr) {
					continue
				}
				log.Printf("Network monitor: addr %s ifindex=%d new=%v",
					u.LinkAddress.String(), u.LinkIndex, u.NewAddr)
				m.kick(trigger)
			case <-m.stop:
				return
			}
		}
	}()

	// Debouncer: on the first trigger wait 500ms for more, then fire
	// onChange once. This collapses the typical NM reconfiguration
	// burst into exactly one reconnect check.
	go func() {
		defer m.wg.Done()
		const debounce = 500 * time.Millisecond
		for {
			select {
			case <-trigger:
				// Drain any more triggers that arrive in the window.
				timer := time.NewTimer(debounce)
			drain:
				for {
					select {
					case <-trigger:
						// reset the window
						if !timer.Stop() {
							<-timer.C
						}
						timer.Reset(debounce)
					case <-timer.C:
						break drain
					case <-m.stop:
						timer.Stop()
						return
					}
				}
				if onChange != nil {
					onChange()
				}
			case <-m.stop:
				return
			}
		}
	}()

	log.Printf("Network monitor: watching link and address events")
	return m
}

// Stop shuts down the monitor and blocks until the watcher goroutines
// exit. Safe to call on a nil monitor.
func (m *networkMonitor) Stop() {
	if m == nil {
		return
	}
	select {
	case <-m.stop:
		// already stopped
		return
	default:
		close(m.stop)
	}
	m.wg.Wait()
}

// transitionedAddr reports whether the given address represents an
// actual change in the interface's address set. RTM_NEWADDR for an
// IP already known on the same ifindex (lifetime refresh) returns
// false; a true add or a true remove returns true.
func (m *networkMonitor) transitionedAddr(ifindex int, addr string, isNew bool) bool {
	key := fmt.Sprintf("%d/%s", ifindex, addr)
	m.addrMu.Lock()
	defer m.addrMu.Unlock()
	if isNew {
		if m.seenAddrs[key] {
			return false
		}
		m.seenAddrs[key] = true
		return true
	}
	if !m.seenAddrs[key] {
		return false
	}
	delete(m.seenAddrs, key)
	return true
}

// kick pushes a non-blocking signal into the debounce channel.
// Used by both the link and addr watchers so they share one debounce
// window instead of each firing their own.
func (m *networkMonitor) kick(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func boolFlag(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
