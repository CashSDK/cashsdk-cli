package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cashsdk/cashsdk-cli/internal/config"
	"github.com/cashsdk/cashsdk-cli/internal/ui"
	"golang.org/x/term"
)

func init() {
	register(command{
		name:    "auth set",
		summary: "Save a credential locally so commands stop needing --token",
		usage:   "cashsdk auth set [--app <app_id>] [--token <csk_...>] [--label <name>] [--workspace <slug>]",
		flags: []FlagSpec{
			{Name: "label", Value: true, Help: "friendly name shown in `auth status`"},
			{Name: "workspace", Value: true, Help: "workspace slug, used by `apps` with an MCP token"},
		},
		run: runAuthSet,
	})
	register(command{
		name:    "auth status",
		summary: "Show which credentials are saved and where they come from",
		run:     runAuthStatus,
	})
	register(command{
		name:    "auth clear",
		summary: "Remove saved credentials (all, or one app's with --app)",
		run:     runAuthClear,
	})
	register(command{
		name:    "login",
		summary: "Interactive credential setup (paste a token; browser pairing later)",
		run:     runLogin,
	})
}

func runAuthSet(ctx *Ctx) error {
	token := ctx.Args.Str("token")
	if token == "" {
		token = os.Getenv("CASHSDK_TOKEN")
	}
	if token == "" {
		var err error
		token, err = promptToken()
		if err != nil {
			return err
		}
	}
	token = strings.TrimSpace(token)
	kind := config.TokenKind(token)
	if kind == "secret" {
		return &ui.ExitError{Code: 2, Message: "that is a secret key (csk_sk_), which the CLI cannot use",
			Remediation: []string{
				"secret keys authenticate the server data API only",
				"use a setup token (csk_st_) from the dashboard's Generate prompt, or a durable MCP token from Settings, MCP",
			}}
	}
	if kind == "none" || kind == "unknown" {
		return ui.Usage("that does not look like a CashSDK token (expected a csk_st_ or csk_mcp_ prefix)")
	}
	if !config.TokenSyntaxValid(token) {
		return ui.Usage("that token contains invalid characters; copy it again from the dashboard")
	}

	app := ctx.Args.Str("app")
	if app == "" {
		app = ctx.Auth.App
	}
	if kind == "setup" && app == "" {
		return ui.Usage("a setup token is scoped to one app: pass --app <app_id> (shown next to the token in the dashboard)")
	}

	cfg := ctx.Cfg
	if ctx.Args.Has("api-url") {
		cfg.APIURL = ctx.Args.Str("api-url")
	}
	if ws := ctx.Args.Str("workspace"); ws != "" {
		cfg.Workspace = ws
	}
	now := time.Now().UTC()
	if app != "" {
		if cfg.Apps == nil {
			cfg.Apps = map[string]config.AppAuth{}
		}
		cfg.Apps[app] = config.AppAuth{Token: token, Label: ctx.Args.Str("label"), SavedAt: now}
		cfg.DefaultApp = app
	} else {
		cfg.Token = token
		cfg.SavedAt = now
	}
	if err := cfg.Save(); err != nil {
		return &ui.ExitError{Code: 2, Message: "could not save credentials: " + err.Error()}
	}

	if ui.Current.JSON {
		ui.JSON(map[string]any{"saved": true, "kind": kind, "app": app, "path": config.Path()})
		return nil
	}
	ui.OK("saved %s token %s", kind, ui.Dim.Render("("+config.Mask(token)+")"))
	pairs := [][2]string{{"config", config.Path()}}
	if app != "" {
		pairs = append(pairs, [2]string{"app", app + " (now the default)"})
	}
	if note := config.KindNote(kind); note != "" {
		pairs = append(pairs, [2]string{"note", note})
	}
	if kind == "setup" {
		pairs = append(pairs, [2]string{"expires", "24h after it was minted; re-generating the prompt in the dashboard revokes it"})
	}
	ui.KV(pairs)
	return nil
}

func promptToken() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Paste a CashSDK token (input hidden): ")
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", &ui.ExitError{Code: 2, Message: "could not read the token: " + err.Error()}
		}
		return string(raw), nil
	}
	// Piped: read one line from stdin so `echo $TOKEN | cashsdk auth set --app x` works.
	sc := bufio.NewScanner(os.Stdin)
	if sc.Scan() {
		return sc.Text(), nil
	}
	return "", ui.Usage("no token: pass --token, set CASHSDK_TOKEN, or pipe it on stdin")
}

func runAuthStatus(ctx *Ctx) error {
	cfg := ctx.Cfg
	if ui.Current.JSON {
		apps := map[string]any{}
		for id, a := range cfg.Apps {
			apps[id] = map[string]any{"kind": config.TokenKind(a.Token), "token": config.Mask(a.Token), "label": a.Label, "saved_at": a.SavedAt}
		}
		out := map[string]any{
			"path": config.Path(), "api_url": ctx.Auth.APIURL, "default_app": cfg.DefaultApp,
			"workspace": cfg.Workspace, "apps": apps,
			"env_token": os.Getenv("CASHSDK_TOKEN") != "",
		}
		if cfg.Token != "" {
			out["workspace_token"] = map[string]any{"kind": config.TokenKind(cfg.Token), "token": config.Mask(cfg.Token)}
		}
		ui.JSON(out)
		return nil
	}
	ui.Title("cashsdk auth", config.Path())
	pairs := [][2]string{{"api", ctx.Auth.APIURL}}
	if cfg.DefaultApp != "" {
		pairs = append(pairs, [2]string{"default app", cfg.DefaultApp})
	}
	if cfg.Workspace != "" {
		pairs = append(pairs, [2]string{"workspace", cfg.Workspace})
	}
	if os.Getenv("CASHSDK_TOKEN") != "" {
		pairs = append(pairs, [2]string{"env", "CASHSDK_TOKEN is set and overrides saved tokens"})
	}
	ui.KV(pairs)
	if cfg.Token != "" {
		ui.Blank()
		ui.Out("  %s %s %s", config.Mask(cfg.Token), ui.Dim.Render(config.TokenKind(cfg.Token)), ui.Dim.Render("workspace token"))
	}
	if len(cfg.Apps) > 0 {
		ui.Blank()
		rows := [][]string{}
		for id, a := range cfg.Apps {
			age := time.Since(a.SavedAt).Round(time.Minute)
			note := config.TokenKind(a.Token)
			if note == "setup" && age > 24*time.Hour {
				note = ui.Bad.Render("setup, likely expired")
			}
			label := a.Label
			if label == "" {
				label = ui.Dim.Render("(no label)")
			}
			rows = append(rows, []string{id, config.Mask(a.Token), note, label, ui.Dim.Render("saved " + age.String() + " ago")})
		}
		ui.Columns(rows)
	}
	if cfg.Token == "" && len(cfg.Apps) == 0 {
		ui.Blank()
		ui.Note("no saved credentials; run `cashsdk login` or `cashsdk auth set`")
	}
	return nil
}

func runAuthClear(ctx *Ctx) error {
	cfg := ctx.Cfg
	if app := ctx.Args.Str("app"); app != "" {
		delete(cfg.Apps, app)
		if cfg.DefaultApp == app {
			cfg.DefaultApp = ""
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		ui.OK("removed credentials for %s", app)
		return nil
	}
	cfg.Apps = nil
	cfg.Token = ""
	cfg.DefaultApp = ""
	if err := cfg.Save(); err != nil {
		return err
	}
	ui.OK("removed all saved credentials")
	return nil
}

func runLogin(ctx *Ctx) error {
	if !ui.Current.JSON {
		ui.Title("Sign in to CashSDK", "")
		ui.Out("The CLI authenticates with a token from your dashboard:")
		ui.Blank()
		rows := [][]string{
			{ui.Accent.Render("Setup token"), "app page, Generate prompt", "scoped to one app, lasts 24h; ideal while onboarding"},
			{ui.Accent.Render("MCP token"), "Settings, MCP, Create token", "durable; ideal for daily use and CI"},
		}
		ui.Columns(rows)
		ui.Blank()
		ui.Note("browser sign-in from the CLI is planned; today you paste a token once and it is stored at %s", config.Path())
		ui.Blank()
	}
	return runAuthSet(ctx)
}
