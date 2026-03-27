package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

const appName = "pvpn"

// realHome returns the real user's home directory, even when running under sudo.
// sudo changes HOME to /root, but we want the invoking user's home.
func realHome() string {
	// If SUDO_USER is set, we're running under sudo — use their home
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil {
			return u.HomeDir
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// ConfigDir returns the configuration directory (~/.config/pvpn).
func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appName)
	}
	return filepath.Join(realHome(), ".config", appName)
}

// DataDir returns the data directory (~/.local/share/pvpn).
func DataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, appName)
	}
	return filepath.Join(realHome(), ".local", "share", appName)
}

// DebugPaths prints the resolved paths (for troubleshooting).
func DebugPaths() string {
	return fmt.Sprintf("config=%s data=%s session=%s", ConfigDir(), DataDir(), SessionFile())
}

// ConfigFile returns the path to the config file.
func ConfigFile() string {
	return filepath.Join(ConfigDir(), "config.toml")
}

// SessionFile returns the path to the encrypted session file.
func SessionFile() string {
	return filepath.Join(DataDir(), "session.enc")
}

// EnsureDirs creates the config and data directories if they don't exist.
// If running as root with SUDO_USER set, directories and files are chowned
// to the real user so the unprivileged TUI can read them.
func EnsureDirs() error {
	dirs := []string{ConfigDir(), DataDir()}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	// If running as root, fix ownership so the real user can access
	fixOwnership(dirs...)
	return nil
}

// FixFileOwnership chowns the given paths to the real user (SUDO_USER).
// No-op if not running as root or SUDO_USER is not set.
func FixFileOwnership(paths ...string) {
	fixOwnership(paths...)
}

func fixOwnership(paths ...string) {
	if os.Getuid() != 0 {
		return
	}
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	for _, p := range paths {
		os.Chown(p, uid, gid)
	}
}
