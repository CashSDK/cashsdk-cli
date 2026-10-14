package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/cashsdk/cashsdk-cli/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserLoginCallbackSecurity(t *testing.T) {
	for _, scenario := range []string{"success", "denied", "save-failed", "bad-token", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			isolate(t)
			life, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var exchanges, revocations, saves atomic.Int32
			var challenge, callback string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var input map[string]any
				_ = json.NewDecoder(r.Body).Decode(&input)
				switch r.URL.Path {
				case "/oauth/register":
					if input["software_id"] != "cashsdk-cli" || input["token_endpoint_auth_method"] != "none" {
						t.Error("registration does not identify public CLI")
					}
					callback = input["redirect_uris"].([]any)[0].(string)
					json.NewEncoder(w).Encode(map[string]string{"client_id": "cli-test"})
				case "/oauth/token":
					exchanges.Add(1)
					digest := sha256.Sum256([]byte(input["code_verifier"].(string)))
					if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge || input["redirect_uri"] != callback || input["client_id"] != "cli-test" {
						t.Error("PKCE or redirect binding incorrect")
					}
					access := "csk_at_abcdef1234"
					if scenario == "bad-token" {
						access = "csk_mcp_wrongkind"
					}
					if scenario == "canceled" {
						cancel()
					}
					json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "csk_rt_abcdef1234", "expires_in": 3600, "cashsdk": map[string]string{"workspace": "test", "workspaceName": "Test <script>"}})
				case "/oauth/revoke":
					revocations.Add(1)
					w.Write([]byte("{}"))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer provider.Close()
			oldOpen := openLoginBrowser
			defer func() { openLoginBrowser = oldOpen }()
			openLoginBrowser = func(address string) error {
				u, _ := url.Parse(address)
				challenge = u.Query().Get("code_challenge")
				state := u.Query().Get("state")
				if u.Query().Get("code_challenge_method") != "S256" || state == "" {
					t.Error("missing PKCE/state")
				}
				for _, query := range []string{"state=wrong&code=x", "state=" + state + "&state=" + state + "&code=x"} {
					resp, err := http.Get(callback + "?" + query)
					if err != nil {
						t.Fatal(err)
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode != 400 {
						t.Errorf("invalid state returned %d", resp.StatusCode)
					}
				}
				values := url.Values{"state": {state}, "code": {"authorized"}}
				if scenario == "denied" {
					values.Del("code")
					values.Set("error", "access_denied")
				}
				resp, err := http.Get(callback + "?" + values.Encode())
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				html := string(raw)
				if strings.Contains(html, "csk_at_") || strings.Contains(html, "csk_rt_") {
					t.Error("credential leaked into HTML")
				}
				if resp.Header.Get("Cache-Control") != "no-store" {
					t.Error("callback is cacheable")
				}
				if scenario == "success" {
					if resp.StatusCode != 200 || !strings.Contains(html, "Access granted") || !strings.Contains(html, "Test &lt;script&gt;") {
						t.Errorf("invalid success HTML/status %d", resp.StatusCode)
					}
					replay, err := http.Get(callback + "?" + values.Encode())
					if err != nil {
						t.Fatal(err)
					}
					replay.Body.Close()
					if replay.StatusCode != 409 {
						t.Error("callback replay accepted")
					}
				} else if resp.StatusCode != 400 {
					t.Errorf("failed login returned %d", resp.StatusCode)
				}
				return nil
			}
			_, err := browserAuthorize(life, provider.URL, true, func(client string, pair oauthPair) error {
				saves.Add(1)
				if client != "cli-test" || pair.CashSDK.Workspace != "test" {
					t.Error("incorrect saved identity")
				}
				if scenario == "save-failed" {
					return errors.New("disk unavailable")
				}
				return nil
			})
			if (err == nil) != (scenario == "success") {
				t.Errorf("unexpected login error: %v", err)
			}
			wantSave := int32(0)
			if scenario == "success" || scenario == "save-failed" {
				wantSave = 1
			}
			if saves.Load() != wantSave {
				t.Errorf("saved %d times, want %d", saves.Load(), wantSave)
			}
			if scenario == "denied" && exchanges.Load() != 0 {
				t.Error("denial exchanged a token")
			}
			if (scenario == "save-failed" || scenario == "canceled") && revocations.Load() != 1 {
				t.Error("unsaved credential not revoked")
			}
		})
	}
}

func TestOAuthConcurrentRefreshAndOriginIsolation(t *testing.T) {
	isolate(t)
	var refreshes atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/oauth/token" || body["grant_type"] != "refresh_token" || body["client_id"] != "cli-test" || body["refresh_token"] != "csk_rt_original" {
			t.Error("incorrect refresh request")
		}
		refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"csk_at_new","refresh_token":"csk_rt_rotated","expires_in":3600}`))
	}))
	defer provider.Close()
	cfg := &config.Config{Token: "csk_at_old", APIURL: provider.URL, Workspace: "workspace", OAuth: &config.OAuthSession{ClientID: "cli-test", RefreshToken: "csk_rt_original", APIURL: provider.URL, ExpiresAt: time.Now().Add(-time.Minute)}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	ctxs := []*Ctx{{Cfg: cfg, Args: &Parsed{}, Auth: config.Resolve(cfg, "", "", "")}, {Cfg: cfg, Args: &Parsed{}, Auth: config.Resolve(cfg, "", "", "")}}
	completed := make(chan error, 2)
	for _, ctx := range ctxs {
		go func(c *Ctx) { completed <- c.refreshOAuth() }(ctx)
	}
	for range ctxs {
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refresh happened %d times", refreshes.Load())
	}
	for _, ctx := range ctxs {
		if ctx.Auth.Token != "csk_at_new" {
			t.Error("stale token")
		}
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.OAuth.RefreshToken != "csk_rt_rotated" || saved.Workspace != "workspace" {
		t.Error("rotation lost state")
	}
	info, err := os.Stat(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Errorf("credential permissions %o", info.Mode().Perm())
	}
	ctxs[0].Auth.APIURL = "https://different.example"
	if err := ctxs[0].refreshOAuth(); err == nil {
		t.Error("session allowed a different API")
	}
}

func TestConnectDefaultsToIndependentOAuth(t *testing.T) {
	isolate(t)
	for _, target := range []string{"claude", "cursor", "codex", "print"} {
		code, out := runCLI(t, "connect", target, "--json")
		if code != 0 {
			t.Fatalf("%s failed: %s", target, out)
		}
		if strings.Contains(out, "Authorization") || strings.Contains(out, "csk_") {
			t.Fatalf("%s exposed credentials", target)
		}
		if !strings.Contains(out, "https://mcp.cashsdk.com/mcp") {
			t.Error("missing endpoint")
		}
	}
	code, _ := runCLI(t, "connect", "cursor", "--token", "csk_at_test")
	if code == 0 {
		t.Error("exported CLI OAuth token to another client")
	}
}

func TestBrowserLauncherPreservesQueryAndUsesNoShell(t *testing.T) {
	address := "https://api.cashsdk.com/oauth/authorize?client_id=cli&state=abc&code_challenge=def"
	for _, platform := range []string{"darwin", "windows", "linux"} {
		command, err := loginBrowserCommand(platform, address)
		if err != nil {
			t.Fatal(err)
		}
		if command.Args[len(command.Args)-1] != address {
			t.Error("authorization query was lost")
		}
		want := 2
		if platform == "windows" {
			want = 3
		}
		if len(command.Args) != want {
			t.Error("URL was split into command arguments")
		}
	}
	for _, address := range []string{"javascript:alert(1)", "file:///tmp/unsafe", "http://remote.example/authorize?state=x", "https://user:secret@example.com/authorize"} {
		if _, err := loginBrowserCommand("darwin", address); err == nil {
			t.Errorf("accepted unsafe browser URL: %s", address)
		}
	}
}
