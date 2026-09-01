package vpn

import (
	"context"
	"testing"
	"time"
)

// The NAT-PMP gateway lives inside the tunnel and is unreachable here,
// which is exactly the case that used to stall connect and teardown for
// minutes. Both must now return promptly.
func TestPortForwarder_DoesNotBlockConnectOrTeardown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t0 := time.Now()
	pf := NewPortForwarder(ctx, func(string) {})
	ctor := time.Since(t0)

	t1 := time.Now()
	pf.Stop()
	stop := time.Since(t1)

	t.Logf("NewPortForwarder: %v", ctor.Round(time.Millisecond))
	t.Logf("Stop():           %v", stop.Round(time.Millisecond))

	if ctor > time.Second {
		t.Errorf("NewPortForwarder blocked the connect path for %v, want well under 1s", ctor)
	}
	if stop > 10*time.Second {
		t.Errorf("Stop() blocked teardown for %v, want bounded", stop)
	}
	if pf.Port() != 0 || pf.Protocols() != "" {
		t.Errorf("no mapping was possible, but Port()=%d Protocols()=%q", pf.Port(), pf.Protocols())
	}

	// Stop must stay safe when called again from a second teardown path.
	pf.Stop()
}
