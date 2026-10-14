package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Serialize rotating OAuth credentials across CLI processes. The OS releases the
// lock on process exit, including crashes, without stealing a running command's lock.
func WithLock(fn func() error) error {
	path := Path() + ".lock"
	if Path() == "" {
		return fmt.Errorf("cannot resolve a home directory to lock credentials")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// #nosec G302 -- credentials live in this private directory.
	if err := makePrivate(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := openLockFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("credential lock at %s must be a regular file", path)
	}
	if err := makePrivate(path, 0600); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		locked, err := tryLockFile(file)
		if err != nil {
			return err
		}
		if locked {
			defer unlockFile(file)
			// Never unlink the lock file: another process may already hold its inode.
			return fn()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("credential lock is busy at %s; finish the other CashSDK command and retry", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
