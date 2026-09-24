package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cashsdk/cashsdk-cli/internal/api"
	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

func init() {
	register(command{
		name:    "setup run",
		summary: "Perform every deterministic setup step in one idempotent pass",
		usage:   "cashsdk setup run [--dry-run] [--store app-store|play-store] [--map <product>=<entitlement>]... [--map-all <entitlement>] [--template <id>] [--webhook-url <https url>]",
		flags: []FlagSpec{
			{Name: "dry-run", Help: "show what would change without writing"},
			{Name: "store", Value: true, Example: "app-store|play-store", Help: "which store to set up (default: the app's platform)"},
			{Name: "map", Value: true, Multi: true, Example: "<product>[:<basePlanId>]=<entitlement>", Help: "map all rows for a product, or one Play base plan; preserves existing links; repeatable"},
			{Name: "map-all", Value: true, Example: "<entitlement>", Help: "map every unmapped active access row; skips consumables and products sold without access"},
			{Name: "template", Value: true, Example: "<template_id>", Help: "paywall template to start from (default: the first)"},
			{Name: "webhook-url", Value: true, Example: "<https url>", Help: "register and test this outbound webhook"},
		},
		run: runSetup,
	})
	register(command{
		name:    "setup guide",
		summary: "What is done, what is next, and the exact command for each step",
		run:     runGuide,
	})
}

type appInfo struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	BundleID         string  `json:"bundleId"`
	PackageName      *string `json:"packageName"`
	AppleAppID       *string `json:"appleAppId"`
	DefaultPlacement string  `json:"defaultPlacement"`
	Environment      string  `json:"environment"`
}

func fetchApp(ctx *Ctx) (*appInfo, error) {
	var app appInfo
	_, err := ctx.Client().Get("/v1/apps/"+ctx.Auth.App, &app)
	return &app, err
}

func storeFor(ctx *Ctx, app *appInfo) (string, *ui.ExitError) {
	s := ctx.Args.Str("store")
	switch s {
	case "app-store", "play-store":
		return s, nil
	case "":
		if app.PackageName != nil && *app.PackageName != "" {
			return "play-store", nil
		}
		return "app-store", nil
	default:
		return "", ui.Usage("--store must be app-store or play-store")
	}
}

// dashboardURL points a human at the right screen. Only the production API has
// a known dashboard host; anything else gets a relative path they can resolve.
func dashboardURL(ctx *Ctx, path string) string {
	if strings.TrimRight(ctx.Auth.APIURL, "/") == "https://api.cashsdk.com" {
		return "https://app.cashsdk.com" + path
	}
	return "your dashboard: " + path
}

// ── setup run ────────────────────────────────────────────────────────────────

type step struct {
	ID     string   `json:"id"`
	Status string   `json:"status"` // done | unchanged | planned | needs_attention | blocked
	Detail string   `json:"detail,omitempty"`
	Lines  []string `json:"-"` // extra human lines (console steps, secrets)
}

type runReport struct {
	steps   []step
	blocked []string
	needs   []string
}

func (r *runReport) add(s step) {
	r.steps = append(r.steps, s)
	switch s.Status {
	case "blocked":
		r.blocked = append(r.blocked, s.Detail)
	case "needs_attention":
		r.needs = append(r.needs, s.Detail)
	}
}

func runSetup(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	dry := ctx.Args.Bool("dry-run")
	client := ctx.Client()
	rep := &runReport{}

	sp := ui.NewSpinner("reading app state")
	app, err := fetchApp(ctx)
	if err != nil {
		sp.Stop()
		return err
	}
	store, uerr := storeFor(ctx, app)
	if uerr != nil {
		sp.Stop()
		return uerr
	}
	checklist, _, err := fetchChecklist(ctx)
	sp.Stop()
	if err != nil {
		return err
	}
	itemStatus := map[string]string{}
	for _, it := range checklist.Items {
		itemStatus[it.ID] = it.Status
	}

	ui.Title("Setup run", fmt.Sprintf("%s (%s), %s%s", app.Name, app.ID, store, map[bool]string{true: ", dry run", false: ""}[dry]))
	ui.Blank()

	// 1. Credentials gate: the one step only a human in the dashboard can do.
	credItem := "asc_credentials_valid"
	credAsk := "upload your App Store Connect API key (.p8 + Issuer ID + Key ID, App Manager role) at " + dashboardURL(ctx, "/apps/"+app.ID+"/setup")
	if store == "play-store" {
		credItem = "play_credentials_valid"
		credAsk = "upload your Google Play service-account JSON at " + dashboardURL(ctx, "/apps/"+app.ID+"/setup")
	}
	if itemStatus[credItem] == "passed" {
		rep.add(step{ID: "credentials", Status: "unchanged", Detail: "store credentials validated"})
		stepLine(rep.steps[len(rep.steps)-1])
	} else {
		rep.add(step{ID: "credentials", Status: "blocked", Detail: credAsk})
		stepLine(rep.steps[len(rep.steps)-1])
		finishRun(ctx, rep, app, store, true)
		return &ui.ExitError{Code: 5, Message: "blocked on store credentials",
			Remediation: []string{credAsk, "then re-run `cashsdk setup run`; re-running is always safe"}}
	}

	// 2. Store-side diagnostics: problems no amount of retrying here will clear.
	sp = ui.NewSpinner("running store diagnostics")
	var diag struct {
		Diagnostics []struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
			Title    string `json:"title"`
			Detail   string `json:"detail"`
			Fix      *struct {
				Label string   `json:"label"`
				URL   string   `json:"url"`
				Steps []string `json:"steps"`
			} `json:"fix"`
		} `json:"diagnostics"`
		Blockers int `json:"blockers"`
		Warnings int `json:"warnings"`
	}
	_, derr := client.Get("/v1/apps/"+app.ID+"/setup-diagnostics", &diag)
	sp.Stop()
	if derr != nil {
		rep.add(step{ID: "diagnostics", Status: "needs_attention", Detail: "diagnostics unavailable: " + derr.Error()})
		stepLine(rep.steps[len(rep.steps)-1])
	} else if diag.Blockers == 0 && diag.Warnings == 0 {
		rep.add(step{ID: "diagnostics", Status: "unchanged", Detail: "no store-side problems detected"})
		stepLine(rep.steps[len(rep.steps)-1])
	} else {
		s := step{ID: "diagnostics", Status: "needs_attention",
			Detail: fmt.Sprintf("%d blocker(s), %d warning(s) reported by the store", diag.Blockers, diag.Warnings)}
		for _, d := range diag.Diagnostics {
			if d.Severity == "ok" || d.Severity == "info" {
				continue
			}
			line := fmt.Sprintf("[%s] %s: %s", d.Severity, d.Title, d.Detail)
			if d.Fix != nil {
				if d.Fix.URL != "" {
					line += " (" + d.Fix.Label + ": " + d.Fix.URL + ")"
				} else if d.Fix.Label != "" {
					line += " (" + d.Fix.Label + ")"
				}
			}
			s.Lines = append(s.Lines, line)
		}
		if diag.Blockers == 0 {
			s.Status = "done"
			s.Detail = fmt.Sprintf("%d warning(s), nothing blocking", diag.Warnings)
		}
		rep.add(s)
		stepLine(rep.steps[len(rep.steps)-1])
	}

	// 3. Catalog sync, dry-run first so the diff is always shown.
	sp = ui.NewSpinner("syncing catalog from " + store + " (dry run)")
	var diff struct {
		Added []struct {
			Identifier string `json:"identifier"`
		} `json:"added"`
		Updated []struct {
			Identifier string `json:"identifier"`
		} `json:"updated"`
		Unchanged []struct {
			Identifier string `json:"identifier"`
		} `json:"unchanged"`
		Orphaned []struct {
			Identifier string `json:"identifier"`
		} `json:"orphaned"`
	}
	_, serr := client.Post("/v1/apps/"+app.ID+"/catalog:sync", map[string]any{"store": store, "dryRun": true}, &diff)
	sp.Stop()
	switch {
	case serr != nil:
		rep.add(step{ID: "catalog_sync", Status: "needs_attention", Detail: "sync failed: " + errMessage(serr)})
	case len(diff.Added)+len(diff.Updated)+len(diff.Orphaned) == 0:
		rep.add(step{ID: "catalog_sync", Status: "unchanged",
			Detail: fmt.Sprintf("catalog already in sync (%d product(s))", len(diff.Unchanged))})
	case dry:
		rep.add(step{ID: "catalog_sync", Status: "planned", Detail: diffSummary(len(diff.Added), len(diff.Updated), len(diff.Orphaned))})
	default:
		sp = ui.NewSpinner("applying catalog sync")
		_, aerr := client.Post("/v1/apps/"+app.ID+"/catalog:sync", map[string]any{"store": store}, nil)
		sp.Stop()
		if aerr != nil {
			rep.add(step{ID: "catalog_sync", Status: "needs_attention", Detail: "sync failed: " + errMessage(aerr)})
		} else {
			rep.add(step{ID: "catalog_sync", Status: "done", Detail: diffSummary(len(diff.Added), len(diff.Updated), len(diff.Orphaned))})
		}
	}
	stepLine(rep.steps[len(rep.steps)-1])

	// 4. Product to entitlement mapping. Which tier a product unlocks is a
	// judgment call, so nothing is ever guessed: only --map / --map-all write.
	mapStep := mapProducts(ctx, app, dry)
	rep.add(mapStep)
	stepLine(mapStep)

	// 5. Store notifications: print the console paste, fire the test when ready.
	noteStep := notifications(ctx, app, store, itemStatus, dry)
	rep.add(noteStep)
	stepLine(noteStep)

	// 6. Optional outbound webhook.
	if url := ctx.Args.Str("webhook-url"); url != "" {
		whStep := webhook(ctx, app, url, dry)
		rep.add(whStep)
		stepLine(whStep)
	}

	// 7. Paywall, placement, campaign.
	pwStep := paywallWiring(ctx, app, dry)
	rep.add(pwStep)
	stepLine(pwStep)

	finishRun(ctx, rep, app, store, false)

	if len(rep.blocked) > 0 {
		return &ui.ExitError{Code: 5, Message: "blocked on a step only you can do", Remediation: rep.blocked}
	}
	if len(rep.needs) > 0 {
		return &ui.ExitError{Code: 6, Message: "finished with items needing attention", Remediation: rep.needs}
	}
	return nil
}

func errMessage(err error) string {
	if e, ok := err.(*api.Error); ok {
		return e.Message
	}
	return err.Error()
}

func diffSummary(added, updated, orphaned int) string {
	parts := []string{}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	if updated > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", updated))
	}
	if orphaned > 0 {
		parts = append(parts, fmt.Sprintf("%d orphaned in the store", orphaned))
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

func stepLine(s step) {
	switch s.Status {
	case "done":
		ui.OK("%s  %s", s.ID, ui.Dim.Render(s.Detail))
	case "unchanged":
		ui.Out("  %s %s  %s", ui.GlyphSkip(), s.ID, ui.Dim.Render(s.Detail))
	case "planned":
		ui.Out("  %s %s  %s", ui.GlyphDot(), s.ID, ui.Dim.Render("would: "+s.Detail))
	case "blocked":
		ui.Out("  %s %s  %s", ui.GlyphWarn(), s.ID, "waiting on you: "+s.Detail)
	default:
		ui.Warnline("%s  %s", s.ID, s.Detail)
	}
	for _, l := range s.Lines {
		ui.Out("      %s", l)
	}
}

func mapProducts(ctx *Ctx, app *appInfo, dry bool) step {
	client := ctx.Client()
	var cat catalogPayload
	if _, err := client.Get("/v1/apps/"+app.ID+"/catalog", &cat); err != nil {
		return step{ID: "entitlements", Status: "needs_attention", Detail: "could not read the catalog: " + errMessage(err)}
	}
	if len(cat.Products) == 0 {
		return step{ID: "entitlements", Status: "needs_attention", Detail: "no products in the catalog yet; sync first, then map"}
	}
	entByIdent := map[string]string{}
	for _, e := range cat.Entitlements {
		entByIdent[e.Identifier] = e.ID
	}
	selectors := map[string]bool{}
	unmapped := map[int]string{}
	for i, p := range cat.Products {
		selectors[p.Identifier] = true
		key := p.Identifier
		if p.BasePlanID != nil && *p.BasePlanID != "" {
			key += ":" + *p.BasePlanID
		}
		selectors[key] = true
		if len(p.EntitlementIDs) == 0 && p.Type != "consumable" && !p.SoldWithoutAccess && (p.Status == "" || p.Status == "active") {
			unmapped[i] = key
		}
	}
	explicit := map[string]string{}
	for _, raw := range ctx.Args.Multi("map") {
		prod, ent, ok := strings.Cut(raw, "=")
		if !ok || prod == "" || ent == "" {
			return step{ID: "entitlements", Status: "needs_attention", Detail: "bad --map value " + raw + ", expected <product>[:<basePlanId>]=<entitlement>"}
		}
		if !selectors[prod] {
			return step{ID: "entitlements", Status: "needs_attention", Detail: fmt.Sprintf("unknown product or base plan %q (check cashsdk catalog)", prod)}
		}
		explicit[prod] = ent
	}
	// Resolve intent PER ROW. An identifier covers all its base plans, a specific
	// base plan wins, and --map-all only fills genuinely unmapped access rows.
	want := map[int]string{}
	for i, p := range cat.Products {
		if tier, ok := explicit[p.Identifier]; ok {
			want[i] = tier
		}
		if p.BasePlanID != nil {
			if tier, ok := explicit[p.Identifier+":"+*p.BasePlanID]; ok {
				want[i] = tier
			}
		}
		if _, requested := want[i]; !requested {
			if _, needsMapping := unmapped[i]; needsMapping && ctx.Args.Str("map-all") != "" {
				want[i] = ctx.Args.Str("map-all")
			}
		}
	}
	if len(unmapped) == 0 && len(want) == 0 {
		return step{ID: "entitlements", Status: "unchanged", Detail: "all active access products are mapped; consumables and products explicitly sold without access need no mapping"}
	}
	if len(want) == 0 {
		names := []string{}
		for _, key := range unmapped {
			names = append(names, key)
		}
		sort.Strings(names)
		return step{ID: "entitlements", Status: "needs_attention", Detail: fmt.Sprintf("%d unmapped product row(s): %s. Use --map <product>[:<basePlanId>]=<entitlement> or --map-all <entitlement>; no tier is guessed", len(unmapped), strings.Join(names, ", "))}
	}
	if dry {
		pairs := []string{}
		for i, tier := range want {
			p := cat.Products[i]
			key := p.Identifier
			if p.BasePlanID != nil {
				key += ":" + *p.BasePlanID
			}
			pairs = append(pairs, p.Store+"/"+key+"="+tier)
		}
		sort.Strings(pairs)
		return step{ID: "entitlements", Status: "planned", Detail: "map " + strings.Join(pairs, ", ")}
	}
	mapped := 0
	for i, p := range cat.Products {
		tier, requested := want[i]
		if !requested {
			continue
		}
		entID, ok := entByIdent[tier]
		if !ok {
			var created struct{ ID string }
			if _, err := client.Post("/v1/apps/"+app.ID+"/catalog/entitlements", map[string]any{"identifier": tier}, &created); err != nil {
				return step{ID: "entitlements", Status: "needs_attention", Detail: "could not create entitlement " + tier + ": " + errMessage(err)}
			}
			entID = created.ID
			if entID == "" {
				return step{ID: "entitlements", Status: "needs_attention", Detail: "entitlement creation returned no row ID"}
			}
			entByIdent[tier] = entID
		}
		// PATCH replaces the full set. Keep observed links and make re-runs no-ops.
		present := false
		for _, id := range p.EntitlementIDs {
			if id == entID {
				present = true
			}
		}
		if present {
			continue
		}
		ids := append(append([]string{}, p.EntitlementIDs...), entID)
		if _, err := client.Patch("/v1/apps/"+app.ID+"/catalog/products/"+p.ID, map[string]any{"entitlementIds": ids}, nil); err != nil {
			return step{ID: "entitlements", Status: "needs_attention", Detail: "could not map " + p.Identifier + " (row " + p.ID + "): " + errMessage(err)}
		}
		mapped++
	}
	still := 0
	for i := range unmapped {
		if _, ok := want[i]; !ok {
			still++
		}
	}
	if still > 0 {
		return step{ID: "entitlements", Status: "needs_attention", Detail: fmt.Sprintf("mapped %d product row(s); %d still unmapped, re-run with --map or --map-all", mapped, still)}
	}
	if mapped == 0 {
		return step{ID: "entitlements", Status: "unchanged", Detail: "requested entitlement mappings already exist"}
	}
	return step{ID: "entitlements", Status: "done", Detail: fmt.Sprintf("mapped %d product row(s)", mapped)}
}

func notifications(ctx *Ctx, app *appInfo, store string, itemStatus map[string]string, dry bool) step {
	client := ctx.Client()
	if store == "play-store" {
		if itemStatus["rtdn_configured"] == "passed" && itemStatus["rtdn_test_received"] == "passed" {
			return step{ID: "notifications", Status: "unchanged", Detail: "Play real-time developer notifications verified"}
		}
		var rtdn struct {
			Topic   *string  `json:"topic"`
			Steps   []string `json:"steps"`
			Warning string   `json:"warning"`
		}
		if _, err := client.Get("/v1/apps/"+app.ID+"/rtdn", &rtdn); err != nil {
			return step{ID: "notifications", Status: "needs_attention", Detail: errMessage(err)}
		}
		s := step{ID: "notifications", Status: "blocked", Detail: "paste the Pub/Sub topic into Play Console (steps below), then re-run"}
		if rtdn.Topic != nil {
			s.Lines = append(s.Lines, "topic: "+*rtdn.Topic)
		}
		s.Lines = append(s.Lines, rtdn.Steps...)
		if rtdn.Warning != "" {
			s.Lines = append(s.Lines, "warning: "+rtdn.Warning)
		}
		return s
	}

	urlDone := itemStatus["assn_url_configured"] == "passed"
	sandboxDone := itemStatus["assn_sandbox_verified"] == "passed"
	switch {
	case urlDone && sandboxDone:
		return step{ID: "notifications", Status: "unchanged", Detail: "App Store Server Notifications verified"}
	case !urlDone:
		var assn struct {
			URL   string   `json:"url"`
			Steps []string `json:"steps"`
		}
		if _, err := client.Get("/v1/apps/"+app.ID+"/assn", &assn); err != nil {
			return step{ID: "notifications", Status: "needs_attention", Detail: errMessage(err)}
		}
		s := step{ID: "notifications", Status: "blocked", Detail: "paste the notifications URL into App Store Connect (steps below), then re-run"}
		s.Lines = append(s.Lines, assn.Steps...)
		return s
	case dry:
		return step{ID: "notifications", Status: "planned", Detail: "request an Apple test notification (Sandbox)"}
	default:
		var res struct {
			Token     string `json:"token"`
			Simulated bool   `json:"simulated"`
			Manual    bool   `json:"manual"`
			Note      string `json:"note"`
			OK        *bool  `json:"ok"`
			Message   string `json:"message"`
		}
		if _, err := client.Post("/v1/apps/"+app.ID+"/notifications:test", map[string]any{"store": "app-store", "environment": "Sandbox"}, &res); err != nil {
			return step{ID: "notifications", Status: "needs_attention", Detail: "test request failed: " + errMessage(err)}
		}
		switch {
		case res.OK != nil && !*res.OK:
			return step{ID: "notifications", Status: "needs_attention", Detail: "Apple rejected the test request: " + res.Message}
		case res.Token != "" || res.Simulated:
			return step{ID: "notifications", Status: "done", Detail: "Apple test notification requested; `cashsdk setup verify --wait` watches for it"}
		default:
			return step{ID: "notifications", Status: "done", Detail: strings.TrimSpace("no automated test available. " + res.Note)}
		}
	}
}

func webhook(ctx *Ctx, app *appInfo, url string, dry bool) step {
	client := ctx.Client()
	var list struct {
		Rows []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"rows"`
	}
	if _, err := client.Get("/v1/apps/"+app.ID+"/webhooks", &list); err != nil {
		return step{ID: "webhook", Status: "needs_attention", Detail: errMessage(err)}
	}
	for _, w := range list.Rows {
		if w.URL == url {
			if dry {
				return step{ID: "webhook", Status: "unchanged", Detail: "already registered"}
			}
			if _, err := client.Post("/v1/apps/"+app.ID+"/webhooks/"+w.ID+"/test", map[string]any{}, nil); err != nil {
				return step{ID: "webhook", Status: "needs_attention", Detail: "test send failed: " + errMessage(err)}
			}
			return step{ID: "webhook", Status: "done", Detail: "already registered; sent a signed test event"}
		}
	}
	if dry {
		return step{ID: "webhook", Status: "planned", Detail: "register " + url + " and send a signed test event"}
	}
	var created struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if _, err := client.Post("/v1/apps/"+app.ID+"/webhooks", map[string]any{"url": url}, &created); err != nil {
		return step{ID: "webhook", Status: "needs_attention", Detail: "could not register: " + errMessage(err)}
	}
	s := step{ID: "webhook", Status: "done", Detail: "registered " + url + " and sent a signed test event"}
	s.Lines = append(s.Lines,
		"signing secret (shown once, store it now): "+created.Secret)
	if _, err := client.Post("/v1/apps/"+app.ID+"/webhooks/"+created.ID+"/test", map[string]any{}, nil); err != nil {
		s.Detail = "registered " + url + "; test send failed: " + errMessage(err)
	}
	return s
}

func paywallWiring(ctx *Ctx, app *appInfo, dry bool) step {
	client := ctx.Client()
	var campaigns []struct {
		Status    string `json:"status"`
		Placement string `json:"placement"`
	}
	if _, err := client.Get("/v1/apps/"+app.ID+"/campaigns", &campaigns); err != nil {
		return step{ID: "paywall", Status: "needs_attention", Detail: errMessage(err)}
	}
	for _, c := range campaigns {
		if c.Status == "active" {
			return step{ID: "paywall", Status: "unchanged", Detail: "an active campaign already serves placement " + c.Placement}
		}
	}
	if dry {
		return step{ID: "paywall", Status: "planned",
			Detail: fmt.Sprintf("create a paywall from a template, the %q placement, and an active campaign", app.DefaultPlacement)}
	}

	// Placement: find or create the app's default.
	var placements []struct {
		ID        string `json:"id"`
		EventName string `json:"eventName"`
	}
	if _, err := client.Get("/v1/apps/"+app.ID+"/placements", &placements); err != nil {
		return step{ID: "paywall", Status: "needs_attention", Detail: errMessage(err)}
	}
	placementID := ""
	for _, p := range placements {
		if p.EventName == app.DefaultPlacement {
			placementID = p.ID
			break
		}
	}
	if placementID == "" {
		var created struct {
			ID string `json:"id"`
		}
		if _, err := client.Post("/v1/apps/"+app.ID+"/placements", map[string]any{"eventName": app.DefaultPlacement}, &created); err != nil {
			return step{ID: "paywall", Status: "needs_attention", Detail: "could not create the placement: " + errMessage(err)}
		}
		placementID = created.ID
	}

	// Paywall: reuse the first existing one, otherwise create from a template
	// and publish the draft version so devices actually receive it.
	var paywalls []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if _, err := client.Get("/v1/apps/"+app.ID+"/paywalls", &paywalls); err != nil {
		return step{ID: "paywall", Status: "needs_attention", Detail: errMessage(err)}
	}
	paywallID := ""
	made := ""
	if len(paywalls) > 0 {
		paywallID = paywalls[0].ID
		made = "reused paywall " + paywalls[0].Name
	} else {
		templateID := ctx.Args.Str("template")
		var templates []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if _, err := client.Get("/v1/templates", &templates); err != nil {
			return step{ID: "paywall", Status: "needs_attention", Detail: errMessage(err)}
		}
		if templateID == "" {
			if len(templates) == 0 {
				return step{ID: "paywall", Status: "needs_attention", Detail: "no paywall templates available; create a paywall in the dashboard"}
			}
			templateID = templates[0].ID
		}
		var pw struct {
			ID string `json:"id"`
		}
		if _, err := client.Post("/v1/apps/"+app.ID+"/paywalls/from-template",
			map[string]any{"templateId": templateID}, &pw); err != nil {
			return step{ID: "paywall", Status: "needs_attention", Detail: "could not create the paywall: " + errMessage(err)}
		}
		paywallID = pw.ID
		made = "created a paywall from template " + templateID

		// from-template returns only {id, identifier, name}; the draft version
		// id comes from the detail route, and publish requires it in the body.
		var detail struct {
			Versions []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"versions"`
		}
		if _, err := client.Get("/v1/apps/"+app.ID+"/paywalls/"+paywallID, &detail); err == nil {
			for _, v := range detail.Versions {
				if v.Status == "draft" {
					if _, err := client.Post("/v1/apps/"+app.ID+"/paywalls/"+paywallID+"/publish",
						map[string]any{"versionId": v.ID}, nil); err == nil {
						made += ", published"
					}
					break
				}
			}
		}
	}

	var campaign struct {
		ID string `json:"id"`
	}
	if _, err := client.Post("/v1/apps/"+app.ID+"/campaigns", map[string]any{
		"placementId": placementID,
		"name":        "Default campaign",
		"variants":    []map[string]any{{"paywallId": paywallID, "name": "Default", "trafficPct": 100}},
	}, &campaign); err != nil {
		return step{ID: "paywall", Status: "needs_attention", Detail: "could not create the campaign: " + errMessage(err)}
	}
	return step{ID: "paywall", Status: "done",
		Detail: fmt.Sprintf("%s, wired to placement %q via an active campaign", made, app.DefaultPlacement)}
}

func finishRun(ctx *Ctx, rep *runReport, app *appInfo, store string, stoppedEarly bool) {
	final, _, err := fetchChecklist(ctx)
	if ui.Current.JSON {
		out := map[string]any{
			"app": app.ID, "store": store, "steps": rep.steps,
			"blocked_on": rep.blocked, "needs_attention": rep.needs,
		}
		if err == nil {
			out["checklist"] = final.Summary
		}
		ui.JSON(out)
		return
	}
	ui.Blank()
	if err == nil {
		ui.Out("  %s", summaryLine(final))
	}
	if stoppedEarly {
		return
	}
	ui.Blank()
	ui.Out("%s", ui.Bold.Render("What's left"))
	device := "a real device or simulator with a SANDBOX Apple ID"
	if store == "play-store" {
		device = "a real device with a Play Console licence-tester account (Play Billing needs Play Services)"
	}
	next := [][]string{
		{"1.", "cashsdk snippets", "apply every snippet in your app code, then build"},
		{"2.", "", "make a test purchase on " + device},
		{"3.", "cashsdk setup verify --wait", "watches until every checklist item passes"},
	}
	rows := [][]string{}
	for _, n := range next {
		cmd := n[1]
		if cmd != "" {
			cmd = ui.Accent.Render(cmd)
		}
		rows = append(rows, []string{ui.Dim.Render(n[0]), cmd, n[2]})
	}
	ui.Columns(rows)
}

// ── setup guide ──────────────────────────────────────────────────────────────

// guideActions maps checklist item ids onto the action that moves each one.
var guideActions = map[string][2]string{
	"asc_credentials_valid":               {"human", "upload your App Store Connect API key in the dashboard (app page, Setup)"},
	"asc_role_sufficient":                 {"human", "the uploaded key needs the App Manager role; replace it in the dashboard"},
	"play_credentials_valid":              {"human", "upload your Google Play service-account JSON in the dashboard (app page, Setup)"},
	"play_role_sufficient":                {"human", "grant the service account financial-data access in Play Console"},
	"catalog_synced":                      {"cmd", "cashsdk setup run"},
	"play_catalog_synced":                 {"cmd", "cashsdk setup run"},
	"entitlements_mapped":                 {"cmd", "cashsdk setup run --map <product>=<entitlement>"},
	"assn_url_configured":                 {"cmd", "cashsdk setup run  (prints the URL and the App Store Connect click-path)"},
	"assn_sandbox_verified":               {"auto", "passes when Apple's test notification arrives; `cashsdk setup run` requests one"},
	"assn_production_verified":            {"auto", "passes when a Production notification arrives after release"},
	"rtdn_configured":                     {"cmd", "cashsdk setup run  (prints the Pub/Sub topic and the Play Console click-path)"},
	"rtdn_test_received":                  {"human", "Play Console, Monetization setup: send a test notification"},
	"paywall_published":                   {"cmd", "cashsdk setup run"},
	"sdk_installed_first_ping":            {"cmd", "cashsdk snippets  (apply them, build, run the app once)"},
	"sandbox_purchase_verified":           {"human", "make a test purchase on a device; `cashsdk setup verify --wait` watches"},
	"entitlement_resolved_after_purchase": {"auto", "passes right after the first verified purchase"},
	"webhook_endpoint_verified":           {"cmd", "cashsdk setup run --webhook-url <https url>  (optional)"},
	"keys_issued":                         {"auto", "issued when the app was created"},
	"app_created":                         {"auto", "done"},
}

func runGuide(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	sp := ui.NewSpinner("reading setup state")
	app, err := fetchApp(ctx)
	if err != nil {
		sp.Stop()
		return err
	}
	c, _, err := fetchChecklist(ctx)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		type action struct {
			Item, Kind, Action string
		}
		pending := []action{}
		for _, it := range c.Items {
			if it.Status == "passed" {
				continue
			}
			a := guideActions[it.ID]
			pending = append(pending, action{Item: it.ID, Kind: a[0], Action: a[1]})
		}
		ui.JSON(map[string]any{"app": app.ID, "summary": c.Summary, "next": pending})
		return nil
	}
	ui.Title("Setup guide", fmt.Sprintf("%s (%s)", app.Name, app.ID))
	ui.Blank()
	ui.Out("  %s", summaryLine(c))
	ui.Blank()
	if c.Summary.Complete {
		ui.OK("nothing left; the app is fully set up")
		return nil
	}
	ui.Out("%s", ui.Bold.Render("Next steps, in order"))
	n := 0
	for _, it := range c.Items {
		if it.Status == "passed" {
			continue
		}
		n++
		a, known := guideActions[it.ID]
		label := it.Title
		switch {
		case !known:
			ui.Out("  %d. %s", n, label)
		case a[0] == "cmd":
			ui.Out("  %d. %s", n, label)
			ui.Out("     %s", ui.Accent.Render(a[1]))
		case a[0] == "human":
			ui.Out("  %d. %s  %s", n, label, ui.Warn.Render("(needs you)"))
			ui.Out("     %s", a[1])
		default:
			ui.Out("  %d. %s  %s", n, label, ui.Dim.Render("(automatic)"))
			ui.Out("     %s", ui.Dim.Render(a[1]))
		}
	}
	ui.Blank()
	ui.Note("`cashsdk setup run` performs every deterministic step above in one pass; re-running is always safe")
	return nil
}
