//go:build unix

package store

import (
	"fmt"
	"os"
)

func checkExistingParent(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("database directory %s is writable by others; chmod go-w that directory or use the default data directory. This tool does not change permissions on a custom directory", dir)
	}
	return nil
}
