package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCredentialLockSerializesWriters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := WithLock(func() error {
				count := active.Add(1)
				if count > peak.Load() {
					peak.Store(count)
				}
				time.Sleep(10 * time.Millisecond)
				active.Add(-1)
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("simultaneous writers: %d", peak.Load())
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(Path() + ".lock")
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("lock must be private: %v", err)
		}
	}
}

func TestCredentialLockReleasesAfterError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	expected := errors.New("write refused")
	if err := WithLock(func() error { return expected }); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	if err := WithLock(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialLockCrashHelper(t *testing.T) {
	if os.Getenv("CASHSDK_LOCK_TEST_CHILD") != "1" {
		return
	}
	if err := WithLock(func() error {
		fmt.Println("locked")
		for {
			time.Sleep(time.Second)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialLockReleasesAfterProcessCrash(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	child := exec.Command(os.Args[0], "-test.run=^TestCredentialLockCrashHelper$")
	child.Env = append(os.Environ(), "CASHSDK_LOCK_TEST_CHILD=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		ready <- scanner.Scan() && scanner.Text() == "locked"
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child failed to acquire lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not start")
	}
	file, err := openLockFile(Path() + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	locked, err := tryLockFile(file)
	_ = file.Close()
	if err != nil || locked {
		t.Fatal("must not steal a live process's lock", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	started := time.Now()
	if err := WithLock(func() error { return nil }); err != nil {
		t.Fatal("crashed process left credentials locked", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("crash recovery should not wait for a stale lock timeout")
	}
}

func TestCredentialLockRejectsSymlink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, Path()+".lock"); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("Windows symlink creation needs developer mode")
		}
		t.Fatal(err)
	}
	called := false
	if err := WithLock(func() error { called = true; return nil }); err == nil || called {
		t.Fatal("symlink lock must be refused")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "untouched" {
		t.Fatal("symlink target changed", err)
	}
}
