package tui

// Regression guard for issue #3: while a text field has focus, the global
// 1/2/3 tab shortcuts must not swallow the key press. They used to, which
// made the custom DNS field unusable for any address containing 1, 2 or 3,
// and that is most real resolvers.

import (
	"testing"

	"github.com/YourDoritos/pvpn/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// settingsAppEditingDNS builds an authenticated app sitting on the
// Settings tab with the DNS field open, as pressing Space on the DNS row
// leaves it.
func settingsAppEditingDNS(cfg *config.Config) App {
	app := NewApp(nil, nil, cfg)
	app.authenticated = true
	app.width, app.height = 80, 24
	app.view = ViewSettings
	app.settings = NewSettingsModel()
	app.settings.SetSize(80, 24)
	app.settings.dnsEditing = true
	app.settings.dnsInput = ""
	return app
}

func typeInto(app App, s string) App {
	var m tea.Model = app
	for _, r := range s {
		m, _ = m.Update(runeKey(r))
	}
	return m.(App)
}

func TestDNSInput_AcceptsDigitsUsedByTabShortcuts(t *testing.T) {
	cfg := config.DefaultConfig()

	for _, addr := range []string{"1.1.1.1", "9.9.9.9", "192.168.1.1", "2.3.1.2"} {
		got := typeInto(settingsAppEditingDNS(cfg), addr)

		if got.view != ViewSettings {
			t.Errorf("typing %q navigated away from Settings (view=%v)", addr, got.view)
		}
		if !got.settings.dnsEditing {
			t.Errorf("typing %q dropped out of DNS edit mode", addr)
		}
		if got.settings.dnsInput != addr {
			t.Errorf("typing %q produced dnsInput=%q", addr, got.settings.dnsInput)
		}
	}
}

// The shortcuts must still work when nothing has focus, or the fix would
// have traded one bug for another.
func TestTabShortcuts_StillWorkWhenNoInputFocused(t *testing.T) {
	cfg := config.DefaultConfig()

	base := func() App {
		app := NewApp(nil, nil, cfg)
		app.authenticated = true
		app.width, app.height = 80, 24
		app.view = ViewStatus
		app.settings = NewSettingsModel()
		app.settings.SetSize(80, 24)
		return app
	}

	for _, tc := range []struct {
		key  rune
		want View
	}{
		{'2', ViewServers},
		{'3', ViewSettings},
		{'1', ViewStatus},
	} {
		m, _ := base().Update(runeKey(tc.key))
		if got := m.(App).view; got != tc.want {
			t.Errorf("key %q switched to view %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestInputFocused_TracksTheVisibleView(t *testing.T) {
	cfg := config.DefaultConfig()

	app := settingsAppEditingDNS(cfg)
	if !app.inputFocused() {
		t.Error("Settings with the DNS field open should report focus")
	}

	app.settings.dnsEditing = false
	if app.inputFocused() {
		t.Error("Settings with no field open should not report focus")
	}

	app.view = ViewStatus
	app.settings.dnsEditing = true
	if app.inputFocused() {
		t.Error("focus in a view that is not visible must not block shortcuts")
	}
}
