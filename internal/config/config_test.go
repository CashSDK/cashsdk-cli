package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveTwiceIsAtomicAndPrivate(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	cfg := &Config{Token: "csk_mcp_first"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg.Token = "csk_mcp_second"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token != cfg.Token {
		t.Fatalf("second save was not preserved: got %q", loaded.Token)
	}
	if runtime.GOOS != "windows" {
		assertMode(t, filepath.Dir(Path()), 0o700)
		assertMode(t, Path(), 0o600)
	}
}

func TestWritePrivateFileTightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("got %q", got)
	}
	if runtime.GOOS != "windows" {
		assertMode(t, path, 0o600)
	}
}

func TestLoadRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows systems")
	}
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, Path()); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
}

func TestMaskNeverReturnsShortToken(t *testing.T) {
	for _, token := range []string{"x", "csk_st_short", "csk_sk_abc"} {
		if got := Mask(token); got == token || strings.Contains(got, token) {
			t.Fatalf("Mask(%q) leaked the token as %q", token, got)
		}
	}
}

func TestTokenSyntax(t *testing.T) {
	for _, token := range []string{"csk_st_abc123", "csk_mcp_ABC123", "csk_at_token9", "csk_sk_secret1"} {
		if !TokenSyntaxValid(token) {
			t.Errorf("expected valid token: %s", token)
		}
	}
	for _, token := range []string{"", "csk_st_", "csk_st_bad_value", "csk_st_bad value", "csk_st_bad$(command)", "other_abc"} {
		if TokenSyntaxValid(token) {
			t.Errorf("expected invalid token: %q", token)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
