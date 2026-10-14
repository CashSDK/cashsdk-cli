package cmd

// Regression pins for the defects the 2026-08-27 adversarial audit verified.
// Each test failed before its fix.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Piped output must be plain: SetColorProfile(0) used to force TrueColor into
// pipes because termenv's Profile zero value is TrueColor, not Ascii.
func TestPipedOutputHasNoAnsiEscapes(t *testing.T) {
	isolate(t)
	_, out := runCLI(t, "help")
	if strings.Contains(out, "\x1b") {
		t.Fatalf("piped help output contains ANSI escapes:\n%q", out[:200])
	}
	_, out = runCLI(t, "connect", "--token", "csk_st_testtoken")
	if strings.Contains(out, "\x1b") {
		t.Fatalf("piped connect output contains ANSI escapes")
	}
}

// --version used to be dead code that exited 2 with "unknown flag".
func TestVersionFlag(t *testing.T) {
	isolate(t)
	code, out := runCLI(t, "--version")
	if code != 0 || !strings.Contains(out, "cashsdk") {
		t.Fatalf("--version: code=%d out=%q", code, out)
	}
	code, out = runCLI(t, "version")
	if code != 0 || !strings.Contains(out, "cashsdk") {
		t.Fatalf("version: code=%d out=%q", code, out)
	}
}

// connect --json with no target used to print three concatenated documents.
func TestConnectJSONIsOneDocument(t *testing.T) {
	isolate(t)
	code, out := runCLI(t, "connect", "--json", "--token", "csk_st_testtoken")
	if code != 0 {
		t.Fatalf("connect --json exited %d", code)
	}
	var doc struct {
		Clients map[string]json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not a single JSON document: %v\n%s", err, out)
	}
	for _, c := range []string{"claude", "cursor", "codex"} {
		if _, ok := doc.Clients[c]; !ok {
			t.Errorf("clients missing %q", c)
		}
	}
}

func TestConnectRejectsInsecureEndpointAndShellMetacharacters(t *testing.T) {
	isolate(t)
	code, out := runCLI(t, "connect", "--mcp-url", "http://example.com/mcp",
		"--token", "csk_st_testtoken")
	if code != 2 || strings.Contains(out, "csk_st_testtoken") {
		t.Fatalf("insecure endpoint: code=%d out=%q", code, out)
	}
	code, out = runCLI(t, "connect", "--mcp-url", "https://example.com/$(touch-bad)",
		"--token", "csk_st_testtoken")
	if code != 0 || !strings.Contains(out, `'https://example.com/$(touch-bad)'`) {
		t.Fatalf("endpoint was not safely shell-quoted: code=%d out=%q", code, out)
	}
	code, _ = runCLI(t, "connect", "--token", "csk_st_good$(touchbad)")
	if code != 3 {
		t.Fatalf("token with shell metacharacters should exit 3, got %d", code)
	}
}

// Repeating a single-value flag used to join values with \x1f and leak the
// joined string into URL paths.
func TestRepeatedSingleValueFlagIsUsageError(t *testing.T) {
	isolate(t)
	code, _ := runCLI(t, "whoami", "--app", "appA", "--app", "appB", "--json")
	if code != 2 {
		t.Fatalf("repeated --app should exit 2, got %d", code)
	}
	// Multi-declared flags still repeat fine.
	p, err := parseArgs([]string{"--map", "a=pro", "--map", "b=pro"},
		[]FlagSpec{{Name: "map", Value: true, Multi: true}})
	if err != nil || len(p.Multi("map")) != 2 {
		t.Fatalf("multi flag repetition broke: %v %v", err, p)
	}
}

// whoami with a secret key used to exit 0 in JSON mode while human mode
// claimed the API rejected a credential it never sent.
func TestWhoamiSecretKeyExitsThreeInJSONToo(t *testing.T) {
	isolate(t)
	code, out := runCLI(t, "whoami", "--token", "csk_sk_abc", "--json")
	if code != 3 {
		t.Fatalf("whoami with a secret key should exit 3 in JSON mode, got %d", code)
	}
	if !strings.Contains(out, `"valid": false`) {
		t.Errorf("JSON doc should carry valid:false, got %s", out)
	}
}

// connect cursor --write used to silently clobber an unparseable mcp.json.
func TestCursorWriteRefusesUnparseableConfig(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.MkdirAll(".cursor", 0o755); err != nil {
		t.Fatal(err)
	}
	broken := `{"mcpServers": {"other": {"url": "x"},}}` // trailing comma
	if err := os.WriteFile(filepath.Join(".cursor", "mcp.json"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _ := runCLI(t, "connect", "cursor", "--write", "--token", "csk_st_testtoken")
	if code != 2 {
		t.Fatalf("expected refusal exit 2, got %d", code)
	}
	got, _ := os.ReadFile(filepath.Join(".cursor", "mcp.json"))
	if string(got) != broken {
		t.Fatal("the unparseable file was modified; it must be left untouched")
	}
}

func TestCursorWriteTightensExistingPermissions(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	isolate(t)
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.MkdirAll(".cursor", 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".cursor", "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _ := runCLI(t, "connect", "cursor", "--write", "--token", "csk_st_testtoken")
	if code != 0 {
		t.Fatalf("write exited %d", code)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential-bearing config mode = %o, want 600", got)
	}
}

// A corrupt config file must fail with exit 2 (and remediation on stderr),
// and Save must be atomic so a crash cannot produce that state.
func TestCorruptConfigFailsClosed(t *testing.T) {
	isolate(t)
	dir := os.Getenv("XDG_CONFIG_HOME") + "/cashsdk"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/config.json", []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _ := runCLI(t, "auth", "status", "--json")
	if code != 2 {
		t.Fatalf("corrupt config should exit 2, got %d", code)
	}
}
