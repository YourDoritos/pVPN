package daemon

import (
	"log"

	"github.com/godbus/dbus/v5"
)

// sleepMonitor watches for system suspend/resume via logind D-Bus signals.
// When the system wakes from sleep, it calls the onWake callback so the
// daemon can trigger an immediate VPN reconnection.
type sleepMonitor struct {
	conn   *dbus.Conn
	onWake func()
	stop   chan struct{}
}

// newSleepMonitor subscribes to org.freedesktop.login1.Manager.PrepareForSleep.
// Returns nil (no error) if D-Bus or logind is unavailable — sleep monitoring
// is best-effort.
func newSleepMonitor(onWake func()) *sleepMonitor {
	conn, err := dbus.SystemBus()
	if err != nil {
		log.Printf("Sleep monitor: D-Bus unavailable: %v", err)
		return nil
	}

	// Subscribe to the PrepareForSleep signal from logind.
	// PrepareForSleep(true)  = about to sleep
	// PrepareForSleep(false) = just woke up
	matchRule := "type='signal',sender='org.freedesktop.login1',interface='org.freedesktop.login1.Manager',member='PrepareForSleep'"
	call := conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, matchRule)
	if call.Err != nil {
		log.Printf("Sleep monitor: failed to add D-Bus match rule: %v", call.Err)
		return nil
	}

	m := &sleepMonitor{
		conn:   conn,
		onWake: onWake,
		stop:   make(chan struct{}),
	}

	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)

	go func() {
		for {
			select {
			case sig, ok := <-signals:
				if !ok {
					return
				}
				if sig.Name != "org.freedesktop.login1.Manager.PrepareForSleep" {
					continue
				}
				if len(sig.Body) < 1 {
					continue
				}
				sleeping, ok := sig.Body[0].(bool)
				if !ok {
					continue
				}
				if sleeping {
					log.Printf("Sleep monitor: system going to sleep")
				} else {
					log.Printf("Sleep monitor: system woke up, triggering reconnect check")
					m.onWake()
				}
			case <-m.stop:
				conn.RemoveSignal(signals)
				return
			}
		}
	}()

	log.Printf("Sleep monitor: watching for suspend/resume events")
	return m
}

// Stop stops the sleep monitor.
func (m *sleepMonitor) Stop() {
	if m == nil {
		return
	}
	close(m.stop)
}
