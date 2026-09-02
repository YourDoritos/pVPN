package tui

// Regression guard for issue #1: the Settings account row read
// "not logged in" for the whole session in standalone mode, because the
// only thing that ever populated it was an IPC round trip to the daemon.
// Daemon mode was never broken, which is why the issue looked fixed.

import (
	"strings"
	"testing"

	"github.com/YourDoritos/pvpn/internal/api"
	"github.com/YourDoritos/pvpn/internal/config"
)

// accountRow returns the account line plus the line after it, since the
// 60-column settings box wraps a long plan title onto the next row.
func accountRow(t *testing.T, m SettingsModel, cfg *config.Config) string {
	t.Helper()
	lines := strings.Split(m.ViewWithConfig(cfg), "\n")
	for i, line := range lines {
		if strings.Contains(line, "Account") {
			row := line
			if i+1 < len(lines) {
				row += " " + lines[i+1]
			}
			return strings.Join(strings.Fields(row), " ")
		}
	}
	return ""
}

func TestAccountRow_PopulatedInStandaloneMode(t *testing.T) {
	cfg := config.DefaultConfig()

	client := api.NewClient(&api.Session{
		UID:          "uid",
		AccessToken:  "access",
		RefreshToken: "refresh",
		LoginEmail:   "someone@example.com",
	})

	app := NewApp(client, nil, cfg)
	app.authenticated = true
	app.daemonMode = false
	app.width, app.height = 80, 24
	app.vpnInfo = &api.VPNInfoResponse{VPN: api.VPNInfo{PlanTitle: "Proton Unlimited", MaxTier: 2}}

	m, _ := app.Update(runeKey('3'))
	got := m.(App)

	row := accountRow(t, got.settings, cfg)
	t.Logf("standalone account row: %q", row)

	if strings.Contains(row, "not logged in") {
		t.Errorf("account row still reads 'not logged in' while signed in: %q", row)
	}
	// The plan title wraps across the box border, so check the words
	// rather than the exact string.
	for _, want := range []string{"Proton", "Unlimited"} {
		if !strings.Contains(row, want) {
			t.Errorf("account row does not show the plan (%q missing): %q", want, row)
		}
	}
	if !strings.Contains(row, "some") {
		t.Errorf("account row does not show the login: %q", row)
	}
}

// Signed out really should say so.
func TestAccountRow_SaysNotLoggedInWhenThereIsNoSession(t *testing.T) {
	cfg := config.DefaultConfig()
	app := NewApp(api.NewClient(nil), nil, cfg)
	app.authenticated = true
	app.daemonMode = false
	app.width, app.height = 80, 24

	m, _ := app.Update(runeKey('3'))
	row := accountRow(t, m.(App).settings, cfg)
	if !strings.Contains(row, "not logged in") {
		t.Errorf("expected 'not logged in' with no session, got %q", row)
	}
}
