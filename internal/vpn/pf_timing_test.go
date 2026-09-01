package vpn

// Guards for the port forwarder's timing contract. The NAT-PMP gateway
// lives inside the tunnel and is unreachable from a test host, which is
// exactly the case that used to stall connect and teardown for minutes.
//
// These send real NAT-PMP datagrams at 10.2.0.1, so they are skipped
// under -short. On a host where 10.2.0.1 happens to answer they still
// hold: every assertion here is an upper bound.

import (
	"testing"
	"time"
)

func TestPortForwarder_DoesNotBlockConnectOrTeardown(t *testing.T) {
	if testing.Short() {
		t.Skip("sends NAT-PMP datagrams to 10.2.0.1")
	}

	t0 := time.Now()
	pf := NewPortForwarder(func(string) {})
	ctor := time.Since(t0)

	t1 := time.Now()
	pf.Stop()
	stop := time.Since(t1)

	t.Logf("NewPortForwarder: %v", ctor.Round(time.Millisecond))
	t.Logf("Stop():           %v", stop.Round(time.Millisecond))

	// Before this was fixed these were ~2m8s and ~4m16s respectively.
	if ctor > time.Second {
		t.Errorf("NewPortForwarder blocked the connect path for %v, want well under 1s", ctor)
	}
	if stop > 10*time.Second {
		t.Errorf("Stop() blocked teardown for %v, want bounded", stop)
	}

	// Stop must stay safe when a second teardown path calls it again.
	pf.Stop()
}

// Stop must actually end the mapping loop. If it does not, an abandoned
// attempt can re-create a mapping the gateway was just told to delete,
// leaving an inbound port open on the exit IP after disconnect.
func TestPortForwarder_StopEndsTheLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("sends NAT-PMP datagrams to 10.2.0.1")
	}

	pf := NewPortForwarder(func(string) {})
	pf.Stop()

	if !pf.isStopped() {
		t.Error("Stop() did not mark the forwarder stopped")
	}
	if got := pf.Port(); got != 0 {
		t.Errorf("Port() = %d after Stop(), want 0", got)
	}
	if got := pf.Protocols(); got != "" {
		t.Errorf("Protocols() = %q after Stop(), want empty", got)
	}

	// A late attempt finishing after Stop must not resurrect the mapping.
	if prev := pf.setPort(41234, true); prev != 0 {
		t.Errorf("setPort returned previous %d, want 0", prev)
	}
	if got := pf.Port(); got != 0 {
		t.Errorf("a late attempt resurrected the port: Port() = %d, want 0", got)
	}

	// And a late mapBoth must refuse to issue any request at all.
	if _, _, err := pf.mapBoth(); err != errStopped {
		t.Errorf("mapBoth after Stop returned %v, want errStopped", err)
	}
}

// Protocols must never claim TCP unless the gateway granted the same
// external port for it. The old code asserted "(TCP+UDP)" regardless.
func TestPortForwarder_ProtocolsReportsOnlyWhatWasGranted(t *testing.T) {
	pf := &PortForwarder{}

	if got := pf.Protocols(); got != "" {
		t.Errorf("Protocols() with no mapping = %q, want empty", got)
	}

	pf.setPort(41234, false)
	if got := pf.Protocols(); got != "UDP" {
		t.Errorf("Protocols() with UDP only = %q, want \"UDP\"", got)
	}

	pf.setPort(41234, true)
	if got := pf.Protocols(); got != "TCP+UDP" {
		t.Errorf("Protocols() with both = %q, want \"TCP+UDP\"", got)
	}

	pf.setPort(0, false)
	if got := pf.Protocols(); got != "" {
		t.Errorf("Protocols() after the mapping lapsed = %q, want empty", got)
	}
}
