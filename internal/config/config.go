// Package config stores CLI credentials and defaults on disk.
//
// Location: $XDG_CONFIG_HOME/cashsdk/config.json, falling back to
// ~/.config/cashsdk/config.json on every OS (matching gh and friends), written
// 0600 in a 0700 directory. A pasted setup token lands here exactly once via
// `cashsdk auth set`; commands then read it from disk so the token never has to
// ride along on each command line (agents run every shell call in a fresh
// process, so exported variables do not survive between invocations).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AppAuth struct {
	Token   string    `json:"token"`
	Label   string    `json:"label,omitempty"`
	SavedAt time.Time `json:"saved_at"`
}

type Config struct {
	APIURL     string             `json:"api_url,omitempty"`
	DefaultApp string             `json:"default_app,omitempty"`
	Workspace  string             `json:"workspace,omitempty"`
	// Per-app credentials (setup tokens are app-scoped by design).
	Apps map[string]AppAuth `json:"apps,omitempty"`
	// A workspace-level durable token (csk_mcp_) that is not tied to one app.
	Token   string    `json:"token,omitempty"`
	SavedAt time.Time `json:"saved_at,omitempty"`
}

// Path returns the config file location without touching the disk.
func Path() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cashsdk", "config.json")
}

// Load reads the config; a missing file is an empty config, not an error.
func Load() (*Config, error) {
	p := Path()
	if p == "" {
		return &Config{}, nil
	}
	info, statErr := os.Lstat(p)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("config at %s is a symbolic link; refusing to read credentials through it", p)
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	// #nosec G304 -- p is the fixed per-user config path, and symlinks are rejected above.
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		return nil, fmt.Errorf("could not make config private: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("config at %s is not valid JSON: %w", p, err)
	}
	return &c, nil
}

// Save writes the config with private permissions, atomically: a crash mid
// write must never leave a half-written credentials file behind.
func (c *Config) Save() error {
	p := Path()
	if p == "" {
		return errors.New("cannot resolve a home directory to store credentials in")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// #nosec G302 -- this is a directory and 0700 is the intended private mode.
	if err := os.Chmod(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivateFile(p, append(raw, '\n'))
}

// TokenKind names a credential by its prefix.
func TokenKind(token string) string {
	switch {
	case strings.HasPrefix(token, "csk_st_"):
		return "setup"
	case strings.HasPrefix(token, "csk_mcp_"):
		return "mcp"
	case strings.HasPrefix(token, "csk_at_"):
		return "oauth"
	case strings.HasPrefix(token, "csk_sk_"):
		return "secret"
	case token == "":
		return "none"
	default:
		return "unknown"
	}
}

// TokenSyntaxValid rejects whitespace and shell metacharacters before a token
// can be sent to another process or an API. Server-issued token suffixes are
// alphanumeric; length remains server-controlled for forward compatibility.
func TokenSyntaxValid(token string) bool {
	kind := TokenKind(token)
	if kind == "none" || kind == "unknown" {
		return false
	}
	prefix := "csk_" + kind + "_"
	switch kind {
	case "setup":
		prefix = "csk_st_"
	case "oauth":
		prefix = "csk_at_"
	case "secret":
		prefix = "csk_sk_"
	}
	suffix := strings.TrimPrefix(token, prefix)
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// KindNote explains what a credential can and cannot do with this CLI.
func KindNote(kind string) string {
	switch kind {
	case "setup":
		return "app-scoped setup token, expires 24h after minting"
	case "mcp":
		return "durable workspace token"
	case "oauth":
		return "OAuth access token (short-lived; prefer an MCP token for the CLI)"
	case "secret":
		return "secret API key: NOT accepted by these endpoints, use a setup or MCP token"
	default:
		return ""
	}
}

// Auth is the resolved credential set for one command invocation.
type Auth struct {
	Token  string
	App    string
	APIURL string
	Source string // where the token came from: flag, env, config
}

// Resolve merges flags, environment and the config file, in that priority.
func Resolve(cfg *Config, tokenFlag, appFlag, apiURLFlag string) Auth {
	a := Auth{}

	a.APIURL = firstNonEmpty(apiURLFlag, os.Getenv("CASHSDK_API_URL"), cfg.APIURL, "https://api.cashsdk.com")
	a.App = firstNonEmpty(appFlag, os.Getenv("CASHSDK_APP"), cfg.DefaultApp)

	switch {
	case tokenFlag != "":
		a.Token, a.Source = tokenFlag, "flag"
	case os.Getenv("CASHSDK_TOKEN") != "":
		a.Token, a.Source = os.Getenv("CASHSDK_TOKEN"), "env"
	default:
		if a.App != "" {
			if app, ok := cfg.Apps[a.App]; ok && app.Token != "" {
				a.Token, a.Source = app.Token, "config"
				return a
			}
		}
		if cfg.Token != "" {
			a.Token, a.Source = cfg.Token, "config"
		}
	}
	return a
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Mask shortens a token for display: csk_st_ab…yz.
func Mask(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 14 {
		return strings.Repeat("•", len(token))
	}
	return token[:10] + "…" + token[len(token)-2:]
}
