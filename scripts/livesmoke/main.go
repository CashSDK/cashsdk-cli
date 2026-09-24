// Live smoke for the cashsdk CLI: a real customer's first commands against a
// real deployment, with a throwaway workspace.
//
//	go build -o dist/cashsdk . && go run ./scripts/livesmoke
//
// Env:
//
//	BASE           API root (default https://api.cashsdk.com)
//	SMOKE_CODE     sign-in code (default 111111, the scoped bypass)
//	SMOKE_CLEANUP  "0" keeps the workspace for inspection
//	CLI            path to the built binary (default dist/cashsdk)
//
// Conventions (see apps/api/scripts/*): the workspace NAME starts with "Smoke"
// so the derived slug starts with "smoke-" and purge-smoke-workspace.ts will
// accept it; sign-in happens exactly once (per-IP send caps are strict); the
// slug is always printed at the end.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	base    = strings.TrimRight(env("BASE", "https://api.cashsdk.com"), "/")
	code    = env("SMOKE_CODE", "111111")
	cliPath = env("CLI", "dist/cashsdk")

	checks, failures int
	session          string
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func ok(cond bool, msg string, extra ...any) {
	checks++
	if cond {
		fmt.Printf("  ok  %s\n", msg)
		return
	}
	failures++
	fmt.Printf("  FAIL %s", msg)
	if len(extra) > 0 {
		fmt.Printf("  %v", extra)
	}
	fmt.Println()
}

func api(method, path string, body any) (int, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, base+path, &buf)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("Authorization", "Bearer "+session)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("  FAIL %s %s: %v\n", method, path, err)
		failures++
		checks++
		return 0, nil
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// run executes the CLI with an isolated config home and returns exit + stdout.
func run(home string, args ...string) (int, string) {
	// #nosec G204 -- CLI is an explicit test-harness path supplied by the operator.
	cmd := exec.Command(cliPath, args...)
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+home, "CASHSDK_TOKEN=", "CASHSDK_APP=", "CASHSDK_API_URL=", "NO_COLOR=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if ee, isExit := err.(*exec.ExitError); isExit {
		exit = ee.ExitCode()
	} else if err != nil {
		exit = -1
	}
	return exit, stdout.String()
}

func parse(out string) map[string]any {
	var v map[string]any
	_ = json.Unmarshal([]byte(out), &v)
	return v
}

func main() {
	sfx := strconv.FormatInt(time.Now().UnixMilli(), 36)
	email := "smoke-" + sfx + "@cashsdk-smoke.test"
	home, _ := os.MkdirTemp("", "cashsdk-smoke-home")
	defer os.RemoveAll(home)

	fmt.Printf("cashsdk CLI live smoke against %s\n\n", base)

	if _, err := os.Stat(cliPath); err != nil {
		fmt.Println("build the binary first: go build -o dist/cashsdk .")
		os.Exit(1)
	}

	// Sign in ONCE (per-IP send caps are strict in production).
	st, _ := api("POST", "/auth/start", map[string]any{"email": email})
	ok(st < 300, fmt.Sprintf("auth start (%d)", st))
	st, body := api("POST", "/auth/verify", map[string]any{"email": email, "code": code})
	tok, _ := body["token"].(string)
	ok(st < 300 && tok != "", fmt.Sprintf("signed in as %s (%d)", email, st))
	if tok == "" {
		finish("")
	}
	session = tok

	// Workspace named "Smoke ..." so the purge tool accepts the derived slug.
	st, body = api("POST", "/v1/workspaces", map[string]any{"name": "Smoke cli " + sfx})
	slug := ""
	if ws, isMap := body["workspace"].(map[string]any); isMap {
		slug, _ = ws["slug"].(string)
	}
	if slug == "" {
		slug, _ = body["slug"].(string)
	}
	ok(st < 300 && strings.HasPrefix(slug, "smoke-"), fmt.Sprintf("workspace %s (%d)", slug, st))

	st, body = api("POST", "/v1/workspaces/"+slug+"/apps",
		map[string]any{"name": "Smoke CLI app", "bundleId": "com.smoke.cli" + sfx})
	appID := ""
	if a, isMap := body["app"].(map[string]any); isMap {
		appID, _ = a["id"].(string)
	}
	ok(st < 300 && appID != "", fmt.Sprintf("app %s (%d)", appID, st))
	if appID == "" {
		finish(slug)
	}

	st, body = api("POST", "/v1/apps/"+appID+"/setup/token", nil)
	setupToken, _ := body["setupToken"].(string)
	ok(st < 300 && strings.HasPrefix(setupToken, "csk_st_"), fmt.Sprintf("setup token minted (%d)", st))

	st, body = api("POST", "/v1/workspaces/"+slug+"/mcp-tokens", map[string]any{"name": "cli smoke"})
	mcpToken, _ := body["token"].(string)
	ok(st < 300 && strings.HasPrefix(mcpToken, "csk_mcp_"), fmt.Sprintf("mcp token minted (%d)", st))

	fmt.Println("\nCLI, as the customer would run it:")

	exit, _ := run(home, "auth", "set", "--app", appID, "--token", setupToken, "--api-url", base, "--json")
	ok(exit == 0, "auth set stores the setup token")

	exit, out := run(home, "whoami", "--json")
	who := parse(out)
	ok(exit == 0 && who["valid"] == true && who["kind"] == "setup", "whoami: setup token accepted", out)

	exit, _ = run(home, "doctor", "--json")
	ok(exit == 0, "doctor: api + credential + app + checklist all pass")

	exit, out = run(home, "setup", "status", "--json")
	status := parse(out)
	summary, _ := status["summary"].(map[string]any)
	total, _ := summary["total"].(float64)
	ok(exit == 0 && total >= 8, fmt.Sprintf("setup status: %v items computed", total))

	exit, out = run(home, "setup", "guide", "--json")
	guide := parse(out)
	next, _ := guide["next"].([]any)
	ok(exit == 0 && len(next) > 0, fmt.Sprintf("setup guide: %d next steps", len(next)))

	// No store credentials on a smoke app, so `setup run` must stop at the
	// human gate: exit 5, a parseable report, and zero writes attempted.
	exit, out = run(home, "setup", "run", "--json")
	rep := parse(out)
	blocked, _ := rep["blocked_on"].([]any)
	ok(exit == 5 && len(blocked) == 1 && strings.Contains(fmt.Sprint(blocked[0]), "App Store Connect"),
		"setup run blocks on credentials with exit 5", out)

	exit, out = run(home, "setup", "verify", "--json")
	ok(exit == 5, "setup verify without --wait reports incomplete via exit 5")
	_ = out

	exit, out = run(home, "snippets", "--json")
	sn := parse(out)
	sdk, _ := sn["sdk"].(map[string]any)
	spm, _ := sdk["spm_url"].(string)
	ok(exit == 0 && spm == "https://github.com/cashsdk/cashsdk-ios", "snippets: published SPM URL", spm)
	// The masked-key regression looks like "csk_pk_ab…yz"; an ellipsis in
	// Apple's own menu text ("Add Package Dependencies…") is fine.
	pkRe := regexp.MustCompile(`csk_pk_[A-Za-z0-9]{24}`)
	maskedRe := regexp.MustCompile(`csk_pk_\S*…`)
	ok(pkRe.MatchString(out) && !maskedRe.MatchString(out), "snippets: real publishable key, not masked")

	exit, out = run(home, "templates", "--json")
	var tpls []any
	_ = json.Unmarshal([]byte(out), &tpls)
	ok(exit == 0 && len(tpls) > 0, fmt.Sprintf("templates: %d public templates", len(tpls)))

	exit, _ = run(home, "catalog", "--json")
	ok(exit == 0, "catalog reads (empty is fine)")

	exit, out = run(home, "codegen", "--lang", "swift")
	ok(exit == 0 && strings.Contains(out, "public enum CatalogProducts"), "codegen emits Swift scaffolding")

	exit, _ = run(home, "events", "--json")
	ok(exit == 0, "events reads")

	exit, _ = run(home, "transactions", "--json")
	ok(exit == 0, "transactions reads")

	exit, _ = run(home, "paywalls", "--json")
	ok(exit == 0, "paywalls reads")

	exit, out = run(home, "connect", "print")
	ok(exit == 0 && strings.Contains(out, "/mcp"), "connect prints MCP configs")

	exit, _ = run(home, "checklist", "--require-complete", "--json")
	ok(exit == 1, "checklist --require-complete gates CI with exit 1")

	exit, out = run(home, "apps", "--workspace", slug, "--token", mcpToken, "--api-url", base, "--json")
	ok(exit == 0 && strings.Contains(out, appID), "apps lists the workspace with an MCP token")

	exit, _ = run(home, "catalog", "--app", appID, "--api-url", base, "--token", "csk_st_bogusbogusbogusbogus", "--json")
	ok(exit == 3, "a bogus token maps to exit 3")

	finish(slug)
}

func cleanup(slug string) {
	if slug == "" {
		return
	}
	if os.Getenv("SMOKE_CLEANUP") == "0" {
		fmt.Printf("\nkept workspace %s (SMOKE_CLEANUP=0)\n", slug)
		return
	}
	st, _ := api("POST", "/v1/workspaces/"+slug+"/deletion",
		map[string]any{"confirmSlug": slug, "reason": "CLI live smoke"})
	fmt.Printf("\nscheduled workspace deletion: %s (%d)\n", slug, st)
	fmt.Printf("purge now with:  npx tsx scripts/purge-smoke-workspace.ts %s  (from apps/api)\n", slug)
}

// finish runs cleanup (os.Exit would skip a defer) and ends the process.
func finish(slug string) {
	cleanup(slug)
	fmt.Printf("\n%d checks, %d failure(s)\n", checks, failures)
	if slug != "" {
		fmt.Printf("workspace: %s\n", slug)
	}
	if failures > 0 {
		os.Exit(1)
	}
	os.Exit(0)
}
