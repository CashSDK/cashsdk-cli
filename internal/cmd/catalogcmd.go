package cmd

import (
	"fmt"

	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

func init() {
	register(command{
		name:    "catalog sync",
		summary: "Import the product catalog from the store",
		usage:   "cashsdk catalog sync [--store app-store|play-store] [--dry-run]",
		flags: []FlagSpec{
			{Name: "store", Value: true, Example: "app-store|play-store", Help: "which store (default: the app's platform)"},
			{Name: "dry-run", Help: "show the diff without writing"},
		},
		run: runCatalogSync,
	})
	register(command{
		name:    "catalog push",
		summary: "Push local catalog changes to the store",
		usage:   "cashsdk catalog push [--store app-store|play-store] [--products <id,id>]",
		flags: []FlagSpec{
			{Name: "store", Value: true, Example: "app-store|play-store", Help: "which store (default: the app's platform)"},
			{Name: "products", Value: true, Multi: true, Example: "<id,id>", Help: "limit the push to these product identifiers"},
		},
		run: runCatalogPush,
	})
}

type syncedProduct struct {
	Identifier string `json:"identifier"`
	Type       string `json:"type"`
}

type syncDiff struct {
	Store     string          `json:"store"`
	Added     []syncedProduct `json:"added"`
	Updated   []syncedProduct `json:"updated"`
	Unchanged []syncedProduct `json:"unchanged"`
	Orphaned  []struct {
		Identifier string `json:"identifier"`
	} `json:"orphaned"`
	Applied bool     `json:"applied"`
	Caveats []string `json:"caveats"`
}

func runCatalogSync(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	app, err := fetchApp(ctx)
	if err != nil {
		return err
	}
	store, uerr := storeFor(ctx, app)
	if uerr != nil {
		return uerr
	}
	body := map[string]any{"store": store}
	label := "syncing catalog from " + store
	if ctx.Args.Bool("dry-run") {
		body["dryRun"] = true
		label += " (dry run)"
	}
	sp := ui.NewSpinner(label)
	var diff syncDiff
	raw, err := ctx.Client().Post("/v1/apps/"+app.ID+"/catalog:sync", body, &diff)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
		return nil
	}
	mode := "applied"
	if !diff.Applied {
		mode = "dry run, nothing written"
	}
	ui.Title("Catalog sync", fmt.Sprintf("%s (%s)", store, mode))
	section := func(name string, items []syncedProduct, style func(string) string) {
		if len(items) == 0 {
			return
		}
		ui.Blank()
		ui.Out("%s", ui.Bold.Render(fmt.Sprintf("%s (%d)", name, len(items))))
		for _, p := range items {
			ui.Out("  %s  %s", style(p.Identifier), ui.Dim.Render(p.Type))
		}
	}
	section("Added", diff.Added, func(s string) string { return ui.Good.Render(s) })
	section("Updated", diff.Updated, func(s string) string { return ui.Accent.Render(s) })
	if len(diff.Orphaned) > 0 {
		ui.Blank()
		ui.Out("%s", ui.Bold.Render(fmt.Sprintf("Orphaned in the store (%d)", len(diff.Orphaned))))
		for _, p := range diff.Orphaned {
			ui.Out("  %s", ui.Warn.Render(p.Identifier))
		}
	}
	ui.Blank()
	ui.Out("  %s", ui.Dim.Render(fmt.Sprintf("%d unchanged", len(diff.Unchanged))))
	for _, c := range diff.Caveats {
		ui.Warnline("%s", c)
	}
	if !diff.Applied && len(diff.Added)+len(diff.Updated) > 0 {
		ui.Note("run again without --dry-run to apply")
	}
	return nil
}

func runCatalogPush(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	app, err := fetchApp(ctx)
	if err != nil {
		return err
	}
	store, uerr := storeFor(ctx, app)
	if uerr != nil {
		return uerr
	}
	body := map[string]any{"store": store}
	if ids := ctx.Args.Multi("products"); len(ids) > 0 {
		body["productIds"] = ids
	}
	sp := ui.NewSpinner("pushing catalog to " + store)
	var res struct {
		Results []struct {
			Identifier  string `json:"identifier"`
			OK          bool   `json:"ok"`
			ReviewState string `json:"reviewState"`
			Error       string `json:"error"`
		} `json:"results"`
		Caveats []string `json:"caveats"`
	}
	raw, err := ctx.Client().Post("/v1/apps/"+app.ID+"/catalog:push", body, &res)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
		return nil
	}
	ui.Title("Catalog push", store)
	failed := 0
	for _, r := range res.Results {
		if r.OK {
			note := r.ReviewState
			ui.OK("%s  %s", r.Identifier, ui.Dim.Render(note))
		} else {
			failed++
			ui.Failline("%s  %s", r.Identifier, r.Error)
		}
	}
	for _, c := range res.Caveats {
		ui.Warnline("%s", c)
	}
	if failed > 0 {
		return &ui.ExitError{Code: 6, Message: fmt.Sprintf("%d of %d product(s) failed to push", failed, len(res.Results))}
	}
	if len(res.Results) == 0 {
		ui.Note("nothing to push")
	}
	return nil
}
