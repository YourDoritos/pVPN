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
	natPMPGateway    = "10.2.0.1"
	natPMPLifetime   = 60 // seconds
	natPMPRenewEvery = 45 * time.Second
)

// PortForwarder manages NAT-PMP port mappings.
type PortForwarder struct {
	mu     sync.RWMutex
	port   uint16
	client *natpmp.Client
	cancel context.CancelFunc
	wg     sync.WaitGroup
	onLog  func(string)
}

// NewPortForwarder creates and starts port forwarding.
// Returns immediately after the first port mapping attempt.
// If the server doesn't support port forwarding, port will be 0.
func NewPortForwarder(ctx context.Context, onLog func(string)) *PortForwarder {
	pf := &PortForwarder{onLog: onLog}

	gateway := net.ParseIP(natPMPGateway)
	client := natpmp.NewClient(gateway)
	pf.client = client

	// Try initial mapping
	result, err := client.AddPortMapping("udp", 0, 0, natPMPLifetime)
	if err != nil {
		if onLog != nil {
			onLog(fmt.Sprintf("Port forwarding unavailable: %v", err))
		}
		return pf
	}
	pf.mu.Lock()
	pf.port = result.MappedExternalPort
	pf.mu.Unlock()

	// Also map TCP (same port)
	if _, err := client.AddPortMapping("tcp", 0, 0, natPMPLifetime); err != nil {
		if onLog != nil {
			onLog(fmt.Sprintf("TCP port mapping failed: %v", err))
		}
	}

	if onLog != nil {
		onLog(fmt.Sprintf("Port forwarded: %d", pf.Port()))
	}

	// Start renewal goroutine
	renewCtx, cancel := context.WithCancel(ctx)
	pf.cancel = cancel

	pf.wg.Add(1)
	go func() {
		defer pf.wg.Done()
		ticker := time.NewTicker(natPMPRenewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				res, err := client.AddPortMapping("udp", 0, 0, natPMPLifetime)
				if err != nil {
					if pf.onLog != nil {
						pf.onLog(fmt.Sprintf("Port forwarding renewal failed: %v", err))
					}
					continue
				}
				if _, err := client.AddPortMapping("tcp", 0, 0, natPMPLifetime); err != nil {
					if pf.onLog != nil {
						pf.onLog(fmt.Sprintf("TCP renewal failed: %v", err))
					}
				}
				pf.mu.Lock()
				pf.port = res.MappedExternalPort
				pf.mu.Unlock()
			}
		}
	}()

	return pf
}

// Port returns the currently assigned external port (0 if none).
func (pf *PortForwarder) Port() uint16 {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.port
}

// Stop cancels the renewal goroutine and unmaps ports from the gateway.
func (pf *PortForwarder) Stop() {
	if pf.cancel != nil {
		pf.cancel()
	}
	pf.wg.Wait()

	// Actively unmap ports (lifetime 0 = remove)
	if pf.client != nil {
		pf.client.AddPortMapping("udp", 0, 0, 0)
		pf.client.AddPortMapping("tcp", 0, 0, 0)
	}
}
