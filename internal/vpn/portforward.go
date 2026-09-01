package vpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	natpmp "github.com/jackpal/go-nat-pmp"
)

const (
	natPMPGateway = "10.2.0.1"

	// natPMPLifetime is what we ask the gateway to hold the mapping for,
	// and natPMPRenewEvery how often we refresh it. Renewing well inside
	// the lifetime leaves room for a lost datagram or two without the
	// mapping lapsing.
	natPMPLifetime   = 60 // seconds
	natPMPRenewEvery = 20 * time.Second

	// natPMPTimeout bounds a single NAT-PMP exchange. The library's
	// default is ~128s of internal retries (9 tries with 250ms doubling),
	// which is unusable here: this runs on the connect and teardown
	// paths, where a server that simply does not answer NAT-PMP would
	// otherwise stall pVPN for minutes.
	natPMPTimeout = 3 * time.Second

	// Retry pacing after a failure, with exponential backoff. A server
	// without port forwarding must not cost a request every few seconds
	// for the life of the session.
	natPMPRetryMin = 15 * time.Second
	natPMPRetryMax = 5 * time.Minute

	// natPMPStopGrace bounds how long teardown waits for the mapping loop
	// to unwind before abandoning it. Disconnect must stay responsive even
	// when the gateway has gone silent.
	natPMPStopGrace = 500 * time.Millisecond

	// natPMPGraceAttempts is how many consecutive failed attempts we
	// tolerate before declaring the mapping gone and clearing the port.
	// The UI must not keep advertising a port that has lapsed.
	natPMPGraceAttempts = 2
)

// errStopped aborts an attempt that is racing teardown.
var errStopped = errors.New("port forwarder stopped")

// PortForwarder manages NAT-PMP port mappings for the tunnel.
//
// Construction does not block: the first mapping attempt happens on the
// background goroutine along with every renewal, so a gateway that never
// answers costs nothing on the connect path. Port returns 0 until a
// mapping exists, and returns to 0 once one has lapsed.
//
// Lifetime is owned by Stop(), which teardown always calls. It
// deliberately takes no context: it used to accept one, and the daemon
// passed it the two-minute connect context, which was cancelled the
// instant the connect call returned and silently killed the renewal
// loop about 150ms in. Removing the parameter removes that footgun.
type PortForwarder struct {
	mu         sync.RWMutex
	port       uint16
	bothTCP    bool // TCP mapped on the same external port as UDP
	everMapped bool
	stopped    bool

	client   *natpmp.Client
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
	onLog    func(string)
}

// NewPortForwarder starts port forwarding in the background and returns
// immediately.
func NewPortForwarder(onLog func(string)) *PortForwarder {
	pf := &PortForwarder{
		onLog:  onLog,
		client: natpmp.NewClientWithTimeout(net.ParseIP(natPMPGateway), natPMPTimeout),
	}

	runCtx, cancel := context.WithCancel(context.Background())
	pf.cancel = cancel

	pf.wg.Add(1)
	go pf.run(runCtx)

	return pf
}

// run owns the mapping for the lifetime of the connection: it acquires
// one, refreshes it, and re-acquires it after a failure.
func (pf *PortForwarder) run(ctx context.Context) {
	defer pf.wg.Done()

	var (
		failures  int
		announced uint16
		lastErr   string
	)
	backoff := natPMPRetryMin

	for {
		port, both, err := pf.mapBoth()
		if err != nil {
			failures++
			// Log only when the message changes. Otherwise a server
			// without port forwarding broadcasts the same line to every
			// IPC client every few seconds, forever.
			if msg := err.Error(); msg != lastErr {
				lastErr = msg
				pf.log("Port forwarding unavailable: %v", err)
			}
			// Only give up on the port once the mapping can no longer
			// plausibly be alive, so one lost datagram does not blank a
			// working port in the UI.
			if failures >= natPMPGraceAttempts && pf.setPort(0, false) != 0 {
				pf.log("Port forwarding lapsed, inbound traffic is no longer accepted")
				announced = 0
			}
		} else {
			failures = 0
			lastErr = ""
			pf.setPort(port, both)
			if port != announced {
				announced = port
				if both {
					pf.log("Port forwarded: %d (TCP+UDP)", port)
				} else {
					pf.log("Port forwarded: %d (UDP only, TCP mapping unavailable)", port)
				}
			}
		}

		wait := natPMPRenewEvery
		if pf.Port() == 0 {
			wait = backoff
			if backoff < natPMPRetryMax {
				backoff *= 2
			}
		} else {
			backoff = natPMPRetryMin
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// mapBoth requests a UDP mapping and then a TCP mapping, and reports
// whether both landed on the same external port. The UDP result is
// authoritative; TCP is reported separately so the UI never claims a
// protocol the gateway did not grant.
func (pf *PortForwarder) mapBoth() (port uint16, bothProtocols bool, err error) {
	if pf.isStopped() {
		return 0, false, errStopped
	}

	udp, err := pf.client.AddPortMapping("udp", 0, 0, natPMPLifetime)
	if err != nil {
		return 0, false, err
	}
	port = udp.MappedExternalPort
	if port == 0 {
		return 0, false, fmt.Errorf("gateway returned port 0")
	}

	// Teardown may have released everything while the UDP exchange was in
	// flight. Do not add a TCP mapping on top of a connection that is
	// going away.
	if pf.isStopped() {
		return 0, false, errStopped
	}

	// Ask for any external port, exactly as before, and merely CHECK
	// whether the gateway picked the same one. Requesting a specific
	// external port would be new request semantics that some NAT-PMP
	// implementations reject outright, which would downgrade the report
	// to UDP-only on servers where this used to work.
	tcp, terr := pf.client.AddPortMapping("tcp", int(udp.InternalPort), 0, natPMPLifetime)
	if terr != nil {
		return port, false, nil
	}
	if tcp.MappedExternalPort != port {
		return port, false, nil
	}
	return port, true, nil
}

func (pf *PortForwarder) setPort(port uint16, both bool) (previous uint16) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	previous = pf.port
	if pf.stopped {
		// An attempt that was in flight when Stop ran must not resurrect
		// a mapping teardown has already released.
		return previous
	}
	pf.port = port
	pf.bothTCP = both
	if port != 0 {
		pf.everMapped = true
	}
	return previous
}

func (pf *PortForwarder) isStopped() bool {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.stopped
}

func (pf *PortForwarder) log(format string, args ...any) {
	if pf.onLog != nil {
		pf.onLog(fmt.Sprintf(format, args...))
	}
}

// Port returns the currently mapped external port, or 0 if there is no
// live mapping.
func (pf *PortForwarder) Port() uint16 {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.port
}

// Protocols reports which protocols the current mapping actually covers.
// Callers must not claim TCP unless this says so.
func (pf *PortForwarder) Protocols() string {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	if pf.port == 0 {
		return ""
	}
	if pf.bothTCP {
		return "TCP+UDP"
	}
	return "UDP"
}

// Stop cancels the mapping loop and releases the mappings. It is bounded:
// teardown must not sit behind an unresponsive gateway, so the unmap is
// best-effort and abandoned if it does not complete promptly.
func (pf *PortForwarder) Stop() {
	pf.stopOnce.Do(func() {
		// Mark stopped BEFORE anything else. The NAT-PMP calls are not
		// context aware, so an attempt already inside AddPortMapping can
		// only be abandoned; this flag stops it from adding a mapping or
		// recording a port after we have released them.
		pf.mu.Lock()
		pf.stopped = true
		everMapped := pf.everMapped
		pf.mu.Unlock()

		if pf.cancel != nil {
			pf.cancel()
		}

		loopDone := make(chan struct{})
		go func() {
			pf.wg.Wait()
			close(loopDone)
		}()

		loopEnded := false
		select {
		case <-loopDone:
			loopEnded = true
		case <-time.After(natPMPStopGrace):
		}

		if pf.client == nil {
			return
		}
		// Nothing to release only if we never held a mapping AND the loop
		// finished cleanly. If we abandoned it mid-attempt it may have
		// created one we cannot see, so release anyway.
		if !everMapped && loopEnded {
			return
		}

		// Per RFC 6886 section 3.4, internal port 0 with lifetime 0 asks
		// the gateway to delete every mapping this client holds for that
		// protocol. Run it off the caller's goroutine and give up quickly
		// either way: an orphaned mapping expires on its own within
		// natPMPLifetime seconds.
		done := make(chan struct{})
		go func() {
			defer close(done)
			pf.client.AddPortMapping("udp", 0, 0, 0)
			pf.client.AddPortMapping("tcp", 0, 0, 0)
		}()
		select {
		case <-done:
		case <-time.After(2 * natPMPTimeout):
			pf.log("Port forwarding unmap timed out, mapping will expire on its own")
		}

		pf.mu.Lock()
		pf.port = 0
		pf.bothTCP = false
		pf.mu.Unlock()
	})
}
