package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

// The simple read commands: list things, print them well, exit.

func init() {
	register(command{
		name:    "catalog",
		summary: "Products and entitlements for the app",
		run:     runCatalog,
	})
	register(command{
		name:    "templates",
		summary: "Paywall templates you can start from",
		run:     runTemplates,
	})
	register(command{
		name:    "paywalls",
		summary: "The app's paywalls and their version status",
		run:     runPaywalls,
	})
	register(command{
		name:    "events",
		summary: "Recent SDK and server events",
		usage:   "cashsdk events [--limit <n>] [--follow]",
		flags: []FlagSpec{
			{Name: "limit", Value: true, Example: "<n>", Help: "how many events (default 20)"},
			{Name: "follow", Help: "keep polling for new events"},
		},
		run: runEvents,
	})
	register(command{
		name:    "transactions",
		summary: "Recent store transactions",
		flags: []FlagSpec{
			{Name: "limit", Value: true, Example: "<n>", Help: "how many transactions (default 20)"},
		},
		run: runTransactions,
	})
	register(command{
		name:    "apps",
		summary: "List apps (workspace token) or show the configured app",
		usage:   "cashsdk apps [--workspace <slug>]",
		flags: []FlagSpec{
			{Name: "workspace", Value: true, Help: "workspace slug (saved via `auth set --workspace`)"},
		},
		run: runApps,
	})
}

// catalogPayload matches GET /v1/apps/:id/catalog: products carry their
// mapping as entitlementIds (entitlement ROW ids, not identifiers).
type catalogPayload struct {
	Products []struct {
		ID                string   `json:"id"`
		Identifier        string   `json:"identifier"`
		BasePlanID        *string  `json:"basePlanId"`
		DisplayName       *string  `json:"displayName"`
		Price             *int     `json:"price"`
		Currency          *string  `json:"currency"`
		Type              string   `json:"type"`
		Store             string   `json:"store"`
		EntitlementIDs    []string `json:"entitlementIds"`
		Status            string   `json:"status"`
		SoldWithoutAccess bool     `json:"soldWithoutAccess"`
	} `json:"products"`
	Entitlements []struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		Name       string `json:"name"`
		Rank       *int   `json:"rank"`
	} `json:"entitlements"`
}

func money(price *int, currency *string) string {
	if price == nil {
		return ""
	}
	cur := "USD"
	if currency != nil && *currency != "" {
		cur = *currency
	}
	return fmt.Sprintf("%.2f %s", float64(*price)/100, cur)
}

func runCatalog(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	sp := ui.NewSpinner("fetching catalog")
	var cat catalogPayload
	raw, err := ctx.Client().Get("/v1/apps/"+ctx.Auth.App+"/catalog", &cat)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
		return nil
	}
	ui.Title("Catalog", ctx.Auth.App)
	ui.Blank()
	ui.Out("%s", ui.Bold.Render("Entitlements"))
	if len(cat.Entitlements) == 0 {
		ui.Note("  none yet; create one during `cashsdk setup run --map <product>=<entitlement>`")
	}
	entByID := map[string]string{}
	rows := [][]string{}
	for _, e := range cat.Entitlements {
		entByID[e.ID] = e.Identifier
		rank := ""
		if e.Rank != nil {
			rank = fmt.Sprintf("rank %d", *e.Rank)
		}
		rows = append(rows, []string{ui.Accent.Render(e.Identifier), e.Name, ui.Dim.Render(rank)})
	}
	ui.Columns(rows)
	ui.Blank()
	ui.Out("%s", ui.Bold.Render("Products"))
	if len(cat.Products) == 0 {
		ui.Note("  none yet; `cashsdk catalog sync` imports them from the store")
	}
	rows = rows[:0]
	for _, p := range cat.Products {
		ent := ui.Dim.Render("unmapped")
		if p.Type == "consumable" {
			ent = "balance (no mapping needed)"
		} else if p.SoldWithoutAccess {
			ent = "sold without access"
		} else if p.Status != "" && p.Status != "active" {
			ent = "inactive"
		}
		if len(p.EntitlementIDs) > 0 {
			names := ""
			for i, id := range p.EntitlementIDs {
				if i > 0 {
					names += ", "
				}
				if ident, ok := entByID[id]; ok {
					names += ident
				} else {
					names += id
				}
			}
			ent = names
		}
		identifier := p.Identifier
		if p.BasePlanID != nil {
			identifier += ":" + *p.BasePlanID
		}
		rows = append(rows, []string{ui.Accent.Render(identifier), money(p.Price, p.Currency), p.Type, ent})
	}
	ui.Columns(rows)
	return nil
}

func runTemplates(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	sp := ui.NewSpinner("fetching templates")
	var templates []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Category string `json:"category"`
	}
	raw, err := ctx.Client().Get("/v1/templates", &templates)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
		return nil
	}
	ui.Title("Paywall templates", fmt.Sprintf("%d available", len(templates)))
	rows := [][]string{}
	for _, t := range templates {
		rows = append(rows, []string{ui.Accent.Render(t.ID), t.Name, ui.Dim.Render(t.Category)})
	}
	ui.Columns(rows)
	return nil
}

func runPaywalls(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	sp := ui.NewSpinner("fetching paywalls")
	var paywalls []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Identifier    string `json:"identifier"`
		ActiveVersion *struct {
			Status string `json:"status"`
		} `json:"activeVersion"`
	}
	raw, err := ctx.Client().Get("/v1/apps/"+ctx.Auth.App+"/paywalls", &paywalls)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
		return nil
	}
	ui.Title("Paywalls", ctx.Auth.App)
	if len(paywalls) == 0 {
		ui.Note("none yet; `cashsdk setup run` creates one from a template")
		return nil
	}
	rows := [][]string{}
	for _, p := range paywalls {
		name := p.Name
		if name == "" {
			name = p.Identifier
		}
		status := "draft"
		if p.ActiveVersion != nil && p.ActiveVersion.Status != "" {
			status = p.ActiveVersion.Status
		}
		badge := ui.Dim.Render("[" + status + "]")
		if status == "published" || status == "active" {
			badge = ui.Good.Render("[" + status + "]")
		}
		rows = append(rows, []string{ui.Accent.Render(p.ID), name, badge})
	}
	ui.Columns(rows)
	return nil
}

type eventRow struct {
	ID        string  `json:"id"`
	TS        string  `json:"ts"`
	Event     string  `json:"event"`
	Placement *string `json:"placement"`
}

func (e eventRow) when() string { return e.TS }

func runEvents(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	limit := ctx.Args.StrOr("limit", "20")
	fetch := func() ([]eventRow, json.RawMessage, error) {
		var page struct {
			Rows []eventRow `json:"rows"`
		}
		raw, err := ctx.Client().Get("/v1/apps/"+ctx.Auth.App+"/analytics/events?limit="+limit, &page)
		return page.Rows, raw, err
	}
	follow := ctx.Args.Bool("follow")
	sp := ui.NewSpinner("fetching events")
	events, raw, err := fetch()
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON && !follow {
		ui.RawJSON(raw)
		return nil
	}
	if ui.Current.JSON && follow {
		// NDJSON: one event object per line, oldest first, then each fresh
		// event as it arrives. ui.Out is suppressed in JSON mode, so this
		// writes stdout directly.
		enc := json.NewEncoder(os.Stdout)
		seen := map[string]bool{}
		emit := func(evs []eventRow) {
			for i := len(evs) - 1; i >= 0; i-- {
				if !seen[evs[i].ID] {
					seen[evs[i].ID] = true
					_ = enc.Encode(evs[i])
				}
			}
		}
		emit(events)
		for {
			time.Sleep(3 * time.Second)
			events, _, err = fetch()
			if err != nil {
				return err
			}
			emit(events)
		}
	}
	ui.Title("Events", ctx.Auth.App)
	printEvents := func(evs []eventRow) {
		rows := [][]string{}
		for i := len(evs) - 1; i >= 0; i-- { // oldest first, like a log
			e := evs[i]
			placement := ""
			if e.Placement != nil {
				placement = *e.Placement
			}
			rows = append(rows, []string{ui.Dim.Render(e.when()), ui.Accent.Render(e.Event), placement})
		}
		ui.Columns(rows)
	}
	printEvents(events)
	if !ctx.Args.Bool("follow") {
		if len(events) == 0 {
			ui.Note("no events yet; they appear once the SDK calls configure()")
		}
		return nil
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.ID] = true
	}
	for {
		time.Sleep(3 * time.Second)
		events, _, err = fetch()
		if err != nil {
			return err
		}
		fresh := []eventRow{}
		for _, e := range events {
			if !seen[e.ID] {
				seen[e.ID] = true
				fresh = append(fresh, e)
			}
		}
		printEvents(fresh)
	}
}

func runTransactions(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	// The route reads `limit` (not `pageSize`, which it silently ignores).
	limit := ctx.Args.StrOr("limit", "20")
	sp := ui.NewSpinner("fetching transactions")
	var page struct {
		Rows []struct {
			ProductIdentifier string  `json:"productIdentifier"`
			Store             string  `json:"store"`
			Environment       string  `json:"environment"`
			PurchaseDate      string  `json:"purchaseDate"`
			PriceMinor        *int    `json:"priceMinor"`
			Currency          *string `json:"currency"`
			Revoked           bool    `json:"revoked"`
		} `json:"rows"`
	}
	raw, err := ctx.Client().Get("/v1/apps/"+ctx.Auth.App+"/transactions?limit="+limit, &page)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
		return nil
	}
	ui.Title("Transactions", ctx.Auth.App)
	if len(page.Rows) == 0 {
		ui.Note("none yet; they appear after the first verified purchase")
		return nil
	}
	rows := [][]string{}
	for _, t := range page.Rows {
		state := ""
		if t.Revoked {
			state = ui.Bad.Render("revoked")
		}
		rows = append(rows, []string{
			ui.Dim.Render(t.PurchaseDate), ui.Accent.Render(t.ProductIdentifier),
			money(t.PriceMinor, t.Currency), t.Store, ui.Dim.Render(t.Environment), state,
		})
	}
	ui.Columns(rows)
	return nil
}

func runApps(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	ws := ctx.Args.Str("workspace")
	if ws == "" {
		ws = ctx.Cfg.Workspace
	}
	client := ctx.Client()
	if ws != "" {
		sp := ui.NewSpinner("fetching apps")
		var apps []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			BundleID string `json:"bundleId"`
			Package  string `json:"packageName"`
		}
		raw, err := client.Get("/v1/workspaces/"+ws+"/apps", &apps)
		sp.Stop()
		if err != nil {
			if apiErr, ok := err.(interface{ Error() string }); ok {
				_ = apiErr
			}
			return err
		}
		if ui.Current.JSON {
			ui.RawJSON(raw)
			return nil
		}
		ui.Title("Apps", "workspace "+ws)
		rows := [][]string{}
		for _, a := range apps {
			platform := "iOS"
			if a.Package != "" {
				platform = "Android"
			}
			rows = append(rows, []string{ui.Accent.Render(a.ID), a.Name, a.BundleID, ui.Dim.Render(platform)})
		}
		ui.Columns(rows)
		return nil
	}
	// No workspace known: a setup token can still describe its own app.
	if e := ctx.NeedApp(); e != nil {
		return &ui.ExitError{Code: 2, Message: "listing apps needs a workspace slug",
			Remediation: []string{
				"pass --workspace <slug> (it is in your dashboard URL) or save it: cashsdk auth set --workspace <slug>",
				"a setup token is scoped to one app; with one configured, `cashsdk apps` shows that app",
			}}
	}
	sp := ui.NewSpinner("fetching app")
	raw, err := client.Get("/v1/apps/"+ctx.Auth.App, nil)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
		return nil
	}
	var app map[string]any
	_ = json.Unmarshal(raw, &app)
	ui.Title("App", ctx.Auth.App)
	pairs := [][2]string{}
	for _, k := range []string{"name", "bundleId", "packageName", "appleAppId", "environment", "defaultPlacement"} {
		if v, ok := app[k]; ok && v != nil && fmt.Sprint(v) != "" {
			pairs = append(pairs, [2]string{k, fmt.Sprint(v)})
		}
	}
	ui.KV(pairs)
	ui.Note("to list every app in the workspace, use a workspace (MCP) token and --workspace <slug>")
	return nil
}
