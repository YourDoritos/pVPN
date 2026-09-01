package network

// Regression guard for issue #5: pVPN failed to connect on systems where
// /etc/NetworkManager/conf.d does not exist. writeFileAtomic creates its
// temp file inside the target directory, so a missing parent produced
// ENOENT, which propagated up through SetDNS and aborted the connect.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileAtomic_CreatesMissingParent(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "NetworkManager", "conf.d", "pvpn-dns.conf")
	want := "[global-dns-domain-*]\nservers=10.2.0.1\n"

	if err := writeFileAtomic(target, []byte(want), 0644); err != nil {
		t.Fatalf("writeFileAtomic with a missing parent: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestWriteFileAtomic_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pvpn-dns.conf")

	if err := writeFileAtomic(target, []byte("first\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("second\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second\n" {
		t.Errorf("content = %q, want %q", got, "second\n")
	}

	// The temp file must not be left behind next to the target.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want just the target: %v", len(entries), entries)
	}
}

func TestWriteFileAtomic_UnwritableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		// Root bypasses directory permissions, so simulate the real
		// failure mode instead: a parent path component that is a file.
		base := t.TempDir()
		blocker := filepath.Join(base, "NetworkManager")
		if err := os.WriteFile(blocker, []byte("not a directory\n"), 0644); err != nil {
			t.Fatal(err)
		}
		err := writeFileAtomic(filepath.Join(blocker, "conf.d", "pvpn-dns.conf"), []byte("x"), 0644)
		if !errors.Is(err, syscall.ENOTDIR) {
			t.Errorf("error = %v, want ENOTDIR", err)
		}
		return
	}

	base := t.TempDir()
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0500); err != nil {
		t.Fatal(err)
	}
	err := writeFileAtomic(filepath.Join(locked, "sub", "pvpn-dns.conf"), []byte("x"), 0644)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error = %v, want a permission error", err)
	}
}
