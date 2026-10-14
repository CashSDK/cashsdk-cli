package cmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cashsdk/cashsdk-cli/internal/api"
	"github.com/cashsdk/cashsdk-cli/internal/config"
	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

//go:embed login_success.html
var loginSuccessHTML string

var openLoginBrowser = func(address string) error {
	command, err := loginBrowserCommand(runtime.GOOS, address)
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func loginBrowserCommand(platform, address string) (*exec.Cmd, error) {
	parsed, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("invalid browser URL")
	}
	// Authorization URLs include the signed request parameters. Validate their origin
	// with the endpoint policy while retaining the original query as a single argument.
	parsed.RawQuery = ""
	if err := api.ValidateEndpointURL(parsed.String()); err != nil {
		return nil, err
	}
	switch platform {
	case "darwin":
		// #nosec G204 -- fixed executable; validated HTTP URL is one argument, without a shell.
		return exec.Command("open", address), nil
	case "windows":
		// #nosec G204 -- fixed handler; validated HTTP URL is one argument, without a shell.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", address), nil
	default:
		// #nosec G204 -- fixed executable; validated HTTP URL is one argument, without a shell.
		return exec.Command("xdg-open", address), nil
	}
}

type oauthPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	CashSDK      struct {
		Workspace     string `json:"workspace"`
		WorkspaceName string `json:"workspaceName"`
		AppID         string `json:"appId"`
	} `json:"cashsdk"`
}

func randomURLToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}
func loginPairingCode(challenge string) string {
	sum := sha256.Sum256([]byte(challenge))
	s := strings.ToUpper(hex.EncodeToString(sum[:8]))
	return s[:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

func runBrowserLogin(ctx *Ctx) error {
	if ctx.Args.Bool("with-token") || ctx.Args.Has("token") {
		return runAuthSet(ctx)
	}
	if err := api.ValidateEndpointURL(ctx.Auth.APIURL); err != nil {
		return err
	}
	life, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	life, cancel := context.WithTimeout(life, 5*time.Minute)
	defer cancel()
	pair, err := browserAuthorize(life, ctx.Auth.APIURL, !ctx.Args.Bool("no-browser"), func(clientID string, pair oauthPair) error {
		return config.WithLock(func() error {
			if err := life.Err(); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.Token = pair.AccessToken
			cfg.APIURL = ctx.Auth.APIURL
			cfg.SavedAt = time.Now().UTC()
			cfg.Workspace = pair.CashSDK.Workspace
			cfg.DefaultApp = pair.CashSDK.AppID
			if cfg.DefaultApp != "" {
				delete(cfg.Apps, cfg.DefaultApp)
			}
			cfg.OAuth = &config.OAuthSession{ClientID: clientID, RefreshToken: pair.RefreshToken, APIURL: ctx.Auth.APIURL, ExpiresAt: time.Now().Add(time.Duration(pair.ExpiresIn) * time.Second)}
			return cfg.Save()
		})
	})
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.JSON(map[string]any{"saved": true, "kind": "oauth", "workspace": pair.CashSDK.Workspace, "app": pair.CashSDK.AppID, "path": config.Path()})
	} else {
		ui.OK("Connected to %s", pair.CashSDK.WorkspaceName)
		ui.Note("Credentials saved privately. Run cashsdk connect to set up your coding agent.")
	}
	return nil
}

// The loopback callback exchanges PKCE and persists credentials before reporting success.
func browserAuthorize(life context.Context, base string, open bool, save func(string, oauthPair) error) (oauthPair, error) {
	var empty oauthPair
	verifier, err := randomURLToken()
	if err != nil {
		return empty, err
	}
	state, err := randomURLToken()
	if err != nil {
		return empty, err
	}
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return empty, err
	}
	defer listener.Close()
	callback := "http://" + listener.Addr().String() + "/callback"
	client := api.New(base, "")
	client.HTTP.Timeout = 20 * time.Second
	var registration struct {
		ClientID string `json:"client_id"`
	}
	_, err = client.Post("/oauth/register", map[string]any{"client_name": "CashSDK CLI", "software_id": "cashsdk-cli", "token_endpoint_auth_method": "none", "redirect_uris": []string{callback}}, &registration)
	if err != nil {
		return empty, err
	}
	if registration.ClientID == "" {
		return empty, fmt.Errorf("authorization server did not return a client ID")
	}
	resource := strings.TrimRight(base, "/") + "/mcp"
	query := url.Values{"client_id": {registration.ClientID}, "redirect_uri": {callback}, "response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}, "scope": {"read write"}, "resource": {resource}}
	address := strings.TrimRight(base, "/") + "/oauth/authorize?" + query.Encode()
	type result struct {
		pair oauthPair
		err  error
	}
	done := make(chan result, 1)
	var mutex sync.Mutex
	completed := false
	handler := http.NewServeMux()
	handler.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src https://assets.cashsdk.com; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if r.Method != "GET" || r.Host != listener.Addr().String() {
			http.Error(w, "Invalid callback", 400)
			return
		}
		q := r.URL.Query()
		if len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "The sign-in state does not match. Return to the original login request.", 400)
			return
		}
		mutex.Lock()
		if completed {
			mutex.Unlock()
			http.Error(w, "This sign-in was already completed.", 409)
			return
		}
		completed = true
		mutex.Unlock()
		fail := func(err error) {
			http.Error(w, "Sign-in was not completed. Return to your terminal and try again.", 400)
			done <- result{err: err}
		}
		if q.Get("error") != "" {
			fail(fmt.Errorf("CashSDK access was denied; no credentials were saved"))
			return
		}
		if len(q["code"]) != 1 || q.Get("code") == "" {
			fail(fmt.Errorf("callback did not contain an authorization code"))
			return
		}
		var pair oauthPair
		_, err := client.Post("/oauth/token", map[string]string{"grant_type": "authorization_code", "client_id": registration.ClientID, "redirect_uri": callback, "code": q.Get("code"), "code_verifier": verifier, "resource": resource}, &pair)
		if err != nil {
			fail(fmt.Errorf("could not exchange the authorization code; run cashsdk login again"))
			return
		}
		if config.TokenKind(pair.AccessToken) != "oauth" || !config.TokenSyntaxValid(pair.AccessToken) || !strings.HasPrefix(pair.RefreshToken, "csk_rt_") || pair.ExpiresIn <= 0 || pair.CashSDK.Workspace == "" {
			fail(fmt.Errorf("invalid authorization response; update the CashSDK API and CLI together"))
			return
		}
		if err := life.Err(); err != nil {
			_, _ = client.Post("/oauth/revoke", map[string]string{"token": pair.RefreshToken}, nil)
			fail(err)
			return
		}
		if err := save(registration.ClientID, pair); err != nil {
			_, _ = client.Post("/oauth/revoke", map[string]string{"token": pair.RefreshToken}, nil)
			fail(fmt.Errorf("could not save credentials: %w", err))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		endpoint := resource
		if strings.TrimRight(base, "/") == "https://api.cashsdk.com" {
			endpoint = "https://mcp.cashsdk.com/mcp"
		}
		_ = template.Must(template.New("success").Parse(loginSuccessHTML)).Execute(w, map[string]string{"Workspace": pair.CashSDK.WorkspaceName, "Endpoint": endpoint})
		done <- result{pair: pair}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	fmt.Fprintf(os.Stderr, "Verify this code in your browser: %s\nOpen: %s\n", loginPairingCode(challenge), address)
	if open {
		if err := openLoginBrowser(address); err != nil {
			fmt.Fprintln(os.Stderr, "Could not open a browser. Open the URL above on this computer.")
		}
	}
	select {
	case result := <-done:
		return result.pair, result.err
	case <-life.Done():
		return empty, fmt.Errorf("browser login canceled or expired; run cashsdk auth status to check saved credentials")
	}
}

func (c *Ctx) refreshOAuth() error {
	if c.Auth.Source != "config" || c.Cfg.OAuth == nil || c.Auth.Token != c.Cfg.Token {
		return nil
	}
	if strings.TrimRight(c.Auth.APIURL, "/") != strings.TrimRight(c.Cfg.OAuth.APIURL, "/") {
		return fmt.Errorf("saved OAuth session belongs to a different API; sign in to this API separately")
	}
	if time.Until(c.Cfg.OAuth.ExpiresAt) > time.Minute {
		return nil
	}
	return config.WithLock(func() error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.OAuth == nil || cfg.OAuth.APIURL != c.Cfg.OAuth.APIURL || cfg.OAuth.ClientID != c.Cfg.OAuth.ClientID {
			return fmt.Errorf("saved session changed during refresh")
		}
		if time.Until(cfg.OAuth.ExpiresAt) <= time.Minute {
			client := api.New(cfg.OAuth.APIURL, "")
			client.HTTP.Timeout = 10 * time.Second
			var pair oauthPair
			if _, err := client.Post("/oauth/token", map[string]string{"grant_type": "refresh_token", "client_id": cfg.OAuth.ClientID, "refresh_token": cfg.OAuth.RefreshToken}, &pair); err != nil {
				return fmt.Errorf("could not refresh your CashSDK session")
			}
			if config.TokenKind(pair.AccessToken) != "oauth" || !config.TokenSyntaxValid(pair.AccessToken) || !strings.HasPrefix(pair.RefreshToken, "csk_rt_") || pair.ExpiresIn <= 0 {
				return fmt.Errorf("invalid session refresh response")
			}
			cfg.Token = pair.AccessToken
			cfg.OAuth.RefreshToken = pair.RefreshToken
			cfg.OAuth.ExpiresAt = time.Now().Add(time.Duration(pair.ExpiresIn) * time.Second)
			if err := cfg.Save(); err != nil {
				return err
			}
		}
		c.Cfg = cfg
		c.Auth = config.Resolve(cfg, c.Args.Str("token"), c.Args.Str("app"), c.Args.Str("api-url"))
		return nil
	})
}
