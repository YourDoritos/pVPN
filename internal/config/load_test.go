package config

// Regression guard for issue #4. /etc/pvpn/config.toml is mode 0660
// root:pvpn, so a user whose `usermod -aG pvpn` has not taken effect in
// the current shell gets EACCES here. The bare wrapped error sent the
// reporter chasing their AUR helper instead; the message now names the
// group and the fix.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_PermissionDeniedExplainsTheGroup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[features]\n"), 0000); err != nil {
		t.Fatal(err)
	}

	orig := configDir
	configDir = dir
	t.Cleanup(func() { configDir = orig })

	_, err := Load()
	if err == nil {
		t.Fatal("expected an error reading an unreadable config")
	}
	msg := err.Error()
	t.Logf("reported:\n%s", msg)

	for _, want := range []string{SocketGroup, "usermod", "newgrp", path} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message does not mention %q:\n%s", want, msg)
		}
	}
}

// A missing config is normal on first run and must still yield defaults.
func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	orig := configDir
	configDir = dir
	t.Cleanup(func() { configDir = orig })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with no config file: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned a nil config")
	}
}
