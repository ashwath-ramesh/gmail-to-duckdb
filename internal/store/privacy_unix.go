//go:build unix

package store

import (
	"fmt"
	"os"
	"syscall"
)

func checkExistingParent(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("database directory %s is writable by others; chmod go-w that directory or use the default data directory. This tool does not change permissions on a custom directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("database directory %s: owner check unavailable", dir)
	}
	if !parentOwnerOK(st.Uid, os.Geteuid()) {
		return fmt.Errorf("database directory %s belongs to another user (uid %d), who can replace the database files; use a directory that you or root own", dir, st.Uid)
	}
	return nil
}

// parentOwnerOK accepts a directory that the current user or root owns.
// Another owner can rename or replace files in it, whatever its mode.
func parentOwnerOK(uid uint32, euid int) bool {
	return uid == 0 || int64(uid) == int64(euid)
}
