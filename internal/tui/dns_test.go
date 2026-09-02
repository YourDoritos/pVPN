package tui

// Custom DNS entry: validation, and editing an existing value.

import (
	"strings"
	"testing"

	"github.com/YourDoritos/pvpn/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestParseDNSInput_RejectsNonAddresses(t *testing.T) {
	for _, tc := range []struct {
		in          string
		wantServers []string
		wantInvalid []string
	}{
		{"1.1.1.1", []string{"1.1.1.1"}, nil},
		{"1.1.1.1, 9.9.9.9", []string{"1.1.1.1", "9.9.9.9"}, nil},
		{"1.1.1.1 2606:4700:4700::1111", []string{"1.1.1.1", "2606:4700:4700::1111"}, nil},
		{"999.999.999.999", nil, []string{"999.999.999.999"}},
		{"not-an-ip", nil, []string{"not-an-ip"}},
		{"1.1.1.1 nope", []string{"1.1.1.1"}, []string{"nope"}},
	} {
		servers, invalid := parseDNSInput(tc.in)
		if strings.Join(servers, ",") != strings.Join(tc.wantServers, ",") {
			t.Errorf("parseDNSInput(%q) servers = %v, want %v", tc.in, servers, tc.wantServers)
		}
		if strings.Join(invalid, ",") != strings.Join(tc.wantInvalid, ",") {
			t.Errorf("parseDNSInput(%q) invalid = %v, want %v", tc.in, invalid, tc.wantInvalid)
		}
	}
}

// A typo must not be silently accepted into the resolver config.
func TestDNSEntry_InvalidAddressIsRejectedAndKeepsTheField(t *testing.T) {
	cfg := config.DefaultConfig()
	app := settingsAppEditingDNS(cfg)

	got := typeInto(app, "999.999.999.999")
	m, _ := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := m.(App)

	if len(cfg.DNS.CustomDNS) != 0 {
		t.Errorf("an invalid address was saved: %v", cfg.DNS.CustomDNS)
	}
	if !after.settings.dnsEditing {
		t.Error("editing should stay open so the address can be corrected")
	}
	if after.settings.dnsErr == "" {
		t.Error("no error was reported for an invalid address")
	}
	if !strings.Contains(after.settings.ViewWithConfig(cfg), "not a valid IP") {
		t.Error("the error is not shown in the view")
	}
}

// Opening the editor on an existing entry must pre-fill it, not wipe it.
func TestDNSEntry_EditingExistingValuePrefills(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DNS.CustomDNS = []string{"1.1.1.1", "9.9.9.9"}

	app := NewApp(nil, nil, cfg)
	app.authenticated = true
	app.width, app.height = 80, 24
	app.view = ViewSettings
	app.settings = NewSettingsModel()
	app.settings.SetSize(80, 24)
	for app.settings.cursor < len(settingItems) && settingItems[app.settings.cursor].key != "dns" {
		app.settings.cursor++
	}

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeySpace})
	after := m.(App)

	if !after.settings.dnsEditing {
		t.Fatal("space on an existing custom DNS should open the editor")
	}
	if after.settings.dnsInput != "1.1.1.1 9.9.9.9" {
		t.Errorf("editor pre-filled with %q, want the existing servers", after.settings.dnsInput)
	}
	if len(cfg.DNS.CustomDNS) != 2 {
		t.Errorf("opening the editor destroyed the existing value: %v", cfg.DNS.CustomDNS)
	}
}

// Clearing the field is how you go back to Proton DNS.
func TestDNSEntry_EmptyInputRestoresProton(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DNS.CustomDNS = []string{"1.1.1.1"}

	app := settingsAppEditingDNS(cfg)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := m.(App)

	if len(cfg.DNS.CustomDNS) != 0 {
		t.Errorf("clearing the field should restore Proton DNS, got %v", cfg.DNS.CustomDNS)
	}
	if after.settings.dnsEditing {
		t.Error("confirming should close the editor")
	}
}
