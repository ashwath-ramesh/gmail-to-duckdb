// Package privfile writes and reads owner-only secret files.
package privfile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to path as a private regular file.
// It replaces an existing regular file.
// It rejects a symlink destination and a non-regular file.
func Write(path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if err := MkdirPrivate(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if err := rejectBadDest(path); err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// Read checks that path is a regular file owned by the current user,
// tightens permissions, then returns the bytes.
// It does not follow a symlink.
func Read(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}
	return readPrivate(path)
}

// Check reports whether path is a private regular file for the current user.
// It does not follow a symlink and does not change the file.
func Check(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	return checkPrivate(path)
}

// Harden sets owner-only permissions on an existing regular file.
// It does not follow a symlink and does not read file bytes.
func Harden(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	return hardenPath(path)
}

// MkdirPrivate creates dir with owner-only permissions.
// It does not change an existing directory or an unrelated parent.
func MkdirPrivate(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	return mkdirPrivate(dir)
}

func rejectBadDest(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination is a symlink")
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("destination is not a regular file")
	}
	return nil
}

func newTempPath(dest string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return dest + ".tmp." + hex.EncodeToString(b[:]), nil
}

func writeAtomic(path string, data []byte) (err error) {
	tmp, err := newTempPath(path)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	f, err := createPrivateTemp(tmp)
	if err != nil {
		return fmt.Errorf("create private temp: %w", err)
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		return fmt.Errorf("write: %w", werr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return fmt.Errorf("sync: %w", serr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("close: %w", cerr)
	}
	if rerr := replaceFile(tmp, path); rerr != nil {
		return fmt.Errorf("replace: %w", rerr)
	}
	return syncDir(filepath.Dir(path))
}
