//go:build !windows

package config

import "os"

func makePrivate(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
