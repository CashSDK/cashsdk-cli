package cmd

import (
	"fmt"
	"time"

	"github.com/cashsdk/cashsdk-cli/internal/api"
	"github.com/cashsdk/cashsdk-cli/internal/config"
	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

func init() {
	register(command{
		name:    "doctor",
		summary: "Check reachability, credential, app and checklist in one pass",
		run:     runDoctor,
	})
	register(command{
		name:    "whoami",
		summary: "What credential this invocation resolves to, and whether it works",
		run:     runWhoami,
	})
}

type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func runDoctor(ctx *Ctx) error {
	checks := []check{}
	failed := false
	add := func(c check) {
		checks = append(checks, c)
		if !c.OK {
			failed = true
		}
		if !ui.Current.JSON {
			if c.OK {
				ui.OK("%s  %s", c.Name, ui.Dim.Render(c.Detail))
			} else {
				ui.Failline("%s  %s", c.Name, c.Detail)
				if c.Fix != "" {
					ui.Out("      %s", ui.Dim.Render(c.Fix))
				}
			}
		}
	}
	if !ui.Current.JSON {
		ui.Title("cashsdk doctor", ctx.Auth.APIURL)
		ui.Blank()
	}

	// 1. Reachability (public route, no credential involved).
	sp := ui.NewSpinner("checking the API")
	var health struct {
		Status string  `json:"status"`
		SHA    *string `json:"sha"`
	}
	_, err := api.New(ctx.Auth.APIURL, "").Get("/healthz", &health)
	sp.Stop()
	if err != nil {
		add(check{Name: "api", OK: false, Detail: err.Error(),
			Fix: "check the network or --api-url; production is https://api.cashsdk.com"})
	} else {
		sha := ""
		if health.SHA != nil {
			sha = ", release " + short(*health.SHA)
		}
		add(check{Name: "api", OK: true, Detail: "reachable, status " + health.Status + sha})
	}

	// 2. Credential presence and kind.
	kind := config.TokenKind(ctx.Auth.Token)
	switch kind {
	case "none":
		add(check{Name: "credential", OK: false, Detail: "no token configured",
			Fix: "run `cashsdk login`, or `cashsdk auth set --app <id> --token <csk_st_ or csk_mcp_>`"})
	case "secret":
		add(check{Name: "credential", OK: false, Detail: "a secret key (csk_sk_) cannot authenticate the CLI",
			Fix: "use a setup token (dashboard, Generate prompt) or an MCP token (Settings, MCP)"})
	case "unknown":
		add(check{Name: "credential", OK: false, Detail: "unrecognized token prefix",
			Fix: "expected csk_st_ (setup) or csk_mcp_ (durable)"})
	default:
		add(check{Name: "credential", OK: true, Detail: kind + " token from " + ctx.Auth.Source + " (" + config.Mask(ctx.Auth.Token) + ")"})
		// 3. Validity, against a route every CLI credential can reach.
		sp = ui.NewSpinner("verifying the credential")
		_, verr := ctx.Client().Get("/v1/templates", nil)
		sp.Stop()
		if verr != nil {
			detail := errMessage(verr)
			fix := "mint a fresh token in the dashboard and run `cashsdk auth set` again"
			if kind == "setup" {
				fix = "setup tokens expire after 24h and re-generating the prompt revokes the old one; " + fix
			}
			add(check{Name: "token valid", OK: false, Detail: detail, Fix: fix})
		} else {
			add(check{Name: "token valid", OK: true, Detail: "accepted by the API"})
		}
	}

	// 4. App, when one is configured or required.
	if ctx.Auth.App == "" {
		add(check{Name: "app", OK: false, Detail: "no app id configured",
			Fix: "pass --app, set CASHSDK_APP, or save one with `cashsdk auth set --app <id>`"})
	} else if kind == "setup" || kind == "mcp" || kind == "oauth" {
		sp = ui.NewSpinner("checking the app")
		app, aerr := fetchApp(ctx)
		sp.Stop()
		if aerr != nil {
			add(check{Name: "app", OK: false, Detail: errMessage(aerr),
				Fix: "the token may be scoped to a different app; check `cashsdk auth status`"})
		} else {
			platform := "iOS"
			if app.PackageName != nil && *app.PackageName != "" {
				platform = "Android"
			}
			add(check{Name: "app", OK: true, Detail: fmt.Sprintf("%s (%s, %s, %s)", app.Name, app.ID, platform, app.Environment)})
			sp = ui.NewSpinner("computing the checklist")
			c, _, cerr := fetchChecklist(ctx)
			sp.Stop()
			if cerr != nil {
				add(check{Name: "checklist", OK: false, Detail: errMessage(cerr)})
			} else {
				add(check{Name: "checklist", OK: true, Detail: summaryLine(c)})
			}
		}
	}

	if ui.Current.JSON {
		ui.JSON(map[string]any{"checks": checks, "ok": !failed})
	}
	if failed {
		return &ui.ExitError{Code: 4, Message: "doctor found problems (details above)"}
	}
	if !ui.Current.JSON {
		ui.Blank()
		ui.Note("all good; `cashsdk setup guide` shows what to do next")
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func runWhoami(ctx *Ctx) error {
	kind := config.TokenKind(ctx.Auth.Token)
	out := map[string]any{
		"kind":    kind,
		"source":  ctx.Auth.Source,
		"api_url": ctx.Auth.APIURL,
	}
	if ctx.Auth.Token != "" {
		out["token"] = config.Mask(ctx.Auth.Token)
	}
	if ctx.Auth.App != "" {
		out["app"] = ctx.Auth.App
	}

	// A credential the CLI can never use exits 3 in BOTH output modes, with
	// the accurate explanation; no API call happened, so never imply one did.
	if kind != "setup" && kind != "mcp" && kind != "oauth" {
		out["valid"] = false
		if ui.Current.JSON {
			ui.JSON(out)
		}
		if err := ctx.NeedToken(); err != nil {
			return err // none and secret both explain themselves here
		}
		return &ui.ExitError{Code: 3, Message: "unrecognized token prefix",
			Remediation: []string{"expected csk_st_ (setup) or csk_mcp_ (durable)"}}
	}

	sp := ui.NewSpinner("verifying")
	_, verr := ctx.Client().Get("/v1/templates", nil)
	sp.Stop()
	valid := verr == nil
	out["valid"] = valid
	if a, ok := ctx.Cfg.Apps[ctx.Auth.App]; ok && kind == "setup" && ctx.Auth.Source == "config" {
		expires := a.SavedAt.Add(24 * time.Hour)
		out["expires_by"] = expires.UTC().Format(time.RFC3339)
	}
	if ui.Current.JSON {
		ui.JSON(out)
		if !valid {
			return &ui.ExitError{Code: 3, Message: "the credential was rejected by the API"}
		}
		return nil
	}
	ui.Title("whoami", "")
	pairs := [][2]string{
		{"token", config.Mask(ctx.Auth.Token) + "  (" + kind + ", from " + ctx.Auth.Source + ")"},
		{"api", ctx.Auth.APIURL},
	}
	if ctx.Auth.App != "" {
		pairs = append(pairs, [2]string{"app", ctx.Auth.App})
	}
	if note := config.KindNote(kind); note != "" {
		pairs = append(pairs, [2]string{"scope", note})
	}
	if v, ok := out["expires_by"].(string); ok {
		pairs = append(pairs, [2]string{"expires by", v + " at the latest (24h after it was saved)"})
	}
	status := ui.Bad.Render("rejected by the API")
	if valid {
		status = ui.Good.Render("accepted by the API")
	}
	pairs = append(pairs, [2]string{"status", status})
	ui.KV(pairs)
	if !valid {
		return &ui.ExitError{Code: 3, Message: "the credential was rejected",
			Remediation: []string{"mint a fresh token in the dashboard, then `cashsdk auth set`"}}
	}
	return nil
}
