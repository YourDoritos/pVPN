package tui

// The forwarded port is acquired asynchronously after connect and can
// lapse mid-session, so both refresh paths have to re-read it. They used
// to sample it once at connect time, which was fine only while mapping
// was synchronous; once it moved to the background the row silently
// stopped appearing at all.

import (
	"strings"
	"testing"

	"github.com/YourDoritos/pvpn/internal/config"
	"github.com/YourDoritos/pvpn/internal/ipc"
)

func portRow(t *testing.T, m StatusModel) string {
	t.Helper()
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "Port Forward") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func TestDaemonStatsPoll_RefreshesForwardedPort(t *testing.T) {
	m := NewStatusModel()
	m.SetSize(80, 24)
	m.SetConnectedFromDaemon(&ipc.StatusData{
		State: "connected", Server: "DE#3", Country: "DE",
	})

	if got := portRow(t, m); got != "" {
		t.Fatalf("expected no port row before a mapping exists, got %q", got)
	}

	// The mapping lands a moment later and arrives on the next poll.
	m, _ = m.UpdateDaemon(daemonStatsMsg{stats: &ipc.StatusData{
		ForwardedPort: 55000, ForwardedProto: "TCP+UDP",
	}}, nil)

	row := portRow(t, m)
	if !strings.Contains(row, "55000") {
		t.Errorf("port row = %q, want it to contain 55000", row)
	}
	if !strings.Contains(row, "TCP+UDP") {
		t.Errorf("port row = %q, want it to contain TCP+UDP", row)
	}

	// And when the mapping lapses the row must go away again.
	m, _ = m.UpdateDaemon(daemonStatsMsg{stats: &ipc.StatusData{}}, nil)
	if got := portRow(t, m); got != "" {
		t.Errorf("port row = %q after the mapping lapsed, want it gone", got)
	}
}

// An older daemon omits forwarded_proto entirely. Printing "UDP" there
// turns missing data into a false claim, so print the bare port instead.
func TestForwardedPort_DoesNotInventAProtocol(t *testing.T) {
	m := NewStatusModel()
	m.SetSize(80, 24)
	m.SetConnectedFromDaemon(&ipc.StatusData{State: "connected", Server: "DE#3"})
	m, _ = m.UpdateDaemon(daemonStatsMsg{stats: &ipc.StatusData{ForwardedPort: 55000}}, nil)

	row := portRow(t, m)
	if !strings.Contains(row, "55000") {
		t.Errorf("port row = %q, want it to contain 55000", row)
	}
	if strings.Contains(row, "UDP") || strings.Contains(row, "TCP") {
		t.Errorf("port row = %q, must not claim a protocol it was not told", row)
	}
}

// Same bug class as issue #3: Proton server names carry digits (DE#3,
// US-NY#12), so the search box must receive them rather than losing them
// to the tab shortcuts.
func TestServerSearch_AcceptsDigits(t *testing.T) {
	cfg := config.DefaultConfig()

	app := NewApp(nil, nil, cfg)
	app.authenticated = true
	app.width, app.height = 80, 24
	app.view = ViewServers
	app.servers.searching = true

	got := typeInto(app, "us-ny#12")

	if got.view != ViewServers {
		t.Errorf("typing a server name navigated away from Servers (view=%v)", got.view)
	}
	if !got.servers.InputFocused() {
		t.Error("search lost focus while typing")
	}
}
