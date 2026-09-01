package vpn

import (
	"context"
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
	// the lifetime leaves room for one lost datagram without the mapping
	// lapsing.
	natPMPLifetime   = 60 // seconds
	natPMPRenewEvery = 20 * time.Second

	// natPMPTimeout bounds a single NAT-PMP exchange. The library's
	// default is ~128s of internal retries, which is unusable here: this
	// runs on the connect and teardown paths, where a server that simply
	// does not answer NAT-PMP would otherwise stall pVPN for minutes.
	natPMPTimeout = 3 * time.Second

	// natPMPRetryEvery is how often to reattempt after a failure. The
	// gateway may not be ready the instant the tunnel comes up, and a
	// server without port forwarding should cost us a cheap retry rather
	// than disabling the feature for the whole session.
	natPMPRetryEvery = 15 * time.Second

	// natPMPStopGrace bounds how long teardown will wait for the mapping
	// loop to unwind before abandoning it. Disconnect must stay responsive
	// even when the gateway has gone silent.
	natPMPStopGrace = 500 * time.Millisecond

	// natPMPGraceRenewals is how many consecutive renewal failures we
	// tolerate before declaring the mapping gone and clearing the port.
	// The UI must not keep advertising a port that has lapsed.
	natPMPGraceRenewals = 2
)

// PortForwarder manages NAT-PMP port mappings for the tunnel.
//
// Construction does not block: the first mapping attempt happens on the
// background goroutine along with every renewal, so a gateway that never
// answers costs nothing on the connect path. Port returns 0 until a
// mapping exists, and returns to 0 once one has lapsed.
type PortForwarder struct {
	mu       sync.RWMutex
	port     uint16
	bothTCP  bool // TCP mapped on the same port as UDP
	client   *natpmp.Client
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
	onLog    func(string)
}

// NewPortForwarder starts port forwarding in the background and returns
// immediately.
func NewPortForwarder(ctx context.Context, onLog func(string)) *PortForwarder {
	pf := &PortForwarder{
		onLog:  onLog,
		client: natpmp.NewClientWithTimeout(net.ParseIP(natPMPGateway), natPMPTimeout),
	}

	runCtx, cancel := context.WithCancel(ctx)
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
	)

	attempt := func() {
		port, both, err := pf.mapBoth()
		if err != nil {
			failures++
			pf.log("Port forwarding attempt failed: %v", err)
			// Only give up on the port once the mapping can no longer
			// plausibly still be alive, so a single lost datagram does
			// not blank a working port in the UI.
			if failures >= natPMPGraceRenewals {
				if pf.setPort(0, false) != 0 {
					pf.log("Port forwarding lapsed, no longer accepting inbound traffic")
					announced = 0
				}
			}
			return
		}
		failures = 0
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

	attempt()

	for {
		// Retry sooner when we have nothing than when we are just
		// keeping a healthy mapping alive.
		wait := natPMPRenewEvery
		if pf.Port() == 0 {
			wait = natPMPRetryEvery
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		attempt()
	}
}

// mapBoth requests a UDP mapping and then a TCP mapping on the same
// external port. The UDP result is authoritative: TCP is reported
// separately so the UI can avoid claiming a protocol it did not get.
func (pf *PortForwarder) mapBoth() (port uint16, bothProtocols bool, err error) {
	udp, err := pf.client.AddPortMapping("udp", 0, 0, natPMPLifetime)
	if err != nil {
		return 0, false, err
	}
	port = udp.MappedExternalPort
	if port == 0 {
		return 0, false, fmt.Errorf("gateway returned port 0")
	}

	// Ask for the same external port for TCP. A gateway that hands back
	// a different one has not given us the pair the UI advertises.
	tcp, terr := pf.client.AddPortMapping("tcp", int(udp.InternalPort), int(port), natPMPLifetime)
	if terr != nil {
		pf.log("TCP port mapping failed: %v (UDP mapping is still active)", terr)
		return port, false, nil
	}
	if tcp.MappedExternalPort != port {
		pf.log("TCP mapped to %d but UDP is on %d, reporting UDP only", tcp.MappedExternalPort, port)
		return port, false, nil
	}
	return port, true, nil
}

func (pf *PortForwarder) setPort(port uint16, both bool) (previous uint16) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	previous = pf.port
	pf.port = port
	pf.bothTCP = both
	return previous
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
// Callers should not claim TCP unless this says so.
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
		if pf.cancel != nil {
			pf.cancel()
		}

		// Wait for the mapping loop to notice, but never sit behind an
		// in-flight NAT-PMP exchange. The library call is not context
		// aware, so it can only be abandoned, not cancelled, and a
		// disconnect must not visibly hang because of it.
		loopDone := make(chan struct{})
		go func() {
			pf.wg.Wait()
			close(loopDone)
		}()
		select {
		case <-loopDone:
		case <-time.After(natPMPStopGrace):
		}

		if pf.client == nil || pf.Port() == 0 {
			return
		}

		// Lifetime 0 removes the mapping. Both calls are bounded by
		// natPMPTimeout, but run them off the caller's goroutine and
		// give up quickly regardless: the mapping expires on its own
		// within natPMPLifetime seconds anyway.
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

		pf.setPort(0, false)
	})
}
