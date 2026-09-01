package network

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to a file atomically via temp file + rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	// The parent may not exist: some distros ship NetworkManager without
	// an /etc/NetworkManager/conf.d directory, and CreateTemp below fails
	// with ENOENT there. That error used to travel all the way up and
	// abort the connect entirely (issue #5).
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".pvpn-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	return os.Rename(tmpName, path)
}

// removeIfExists removes a file if it exists, ignoring "not found" errors.
func removeIfExists(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
