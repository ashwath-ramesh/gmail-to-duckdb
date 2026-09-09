//go:build unix

package privfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func createPrivateTemp(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return f, nil
}

func replaceFile(from, to string) error {
	return os.Rename(from, to)
}

func readPrivate(path string) ([]byte, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := verifyOwnerRegular(f); err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("tighten permissions: %w", err)
	}
	if err := checkFileInfo(f); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func checkPrivate(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return checkInfo(fi)
}

func hardenPath(path string) error {
	f, err := openNoFollow(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := verifyOwnerRegular(f); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("tighten permissions: %w", err)
	}
	return nil
}

func lockDown() {
	syscall.Umask(0o077)
}

func checkDir(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return checkDirInfo(fi)
}

func hardenDir(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return fmt.Errorf("path is a symlink")
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := checkDirOwner(fi); err != nil {
		return err
	}
	if err := f.Chmod(0o700); err != nil {
		return fmt.Errorf("tighten directory permissions: %w", err)
	}
	return nil
}

func checkDirInfo(fi os.FileInfo) error {
	if err := checkDirOwner(fi); err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("directory mode %o allows access to others", fi.Mode().Perm())
	}
	return nil
}

func checkDirOwner(fi os.FileInfo) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path is a symlink")
	}
	if !fi.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("owner check unavailable")
	}
	if st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("directory owner is not the current user")
	}
	return nil
}

func mkdirPrivate(dir string) error {
	dir = filepath.Clean(dir)
	if dir == "." || dir == "" {
		return nil
	}
	fi, err := os.Lstat(dir)
	if err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("not a directory")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := mkdirPrivate(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("path is a symlink")
		}
		return nil, err
	}
	return f, nil
}

func verifyOwnerRegular(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("owner check unavailable")
	}
	if st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("file owner is not the current user")
	}
	return nil
}

func checkFileInfo(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return checkInfo(fi)
}

func checkInfo(fi os.FileInfo) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path is a symlink")
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("owner check unavailable")
	}
	if st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("file owner is not the current user")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("file mode %o allows access to others", fi.Mode().Perm())
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		if isUnsupportedSync(err) {
			return nil
		}
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

func isUnsupportedSync(err error) bool {
	return errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.ENOSYS)
}
