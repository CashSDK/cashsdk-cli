package config

import (
	"os"
	"path/filepath"
)

// WritePrivateFile atomically writes credential-bearing data with mode 0600.
// The temporary file lives beside the destination so replacement cannot cross
// filesystems. replaceFile supplies overwrite semantics on every supported OS.
func WritePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cashsdk-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
