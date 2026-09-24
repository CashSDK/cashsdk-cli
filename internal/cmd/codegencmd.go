package cmd

import (
	"os"
	"strings"

	"github.com/cashsdk/cashsdk-cli/internal/codegen"
	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

func init() {
	register(command{
		name:    "codegen",
		summary: "Typed catalog constants so id drift becomes a compile error",
		usage:   "cashsdk codegen --lang swift|kotlin|typescript [--out <file>]",
		flags: []FlagSpec{
			{Name: "lang", Value: true, Example: "swift|kotlin|typescript", Help: "output language (default swift)"},
			{Name: "out", Value: true, Example: "<file>", Help: "write to a file instead of stdout"},
		},
		run: runCodegen,
	})
}

func runCodegen(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	lang := strings.ToLower(ctx.Args.StrOr("lang", "swift"))

	client := ctx.Client()
	sp := ui.NewSpinner("reading the live catalog")
	var cat codegen.Catalog
	if _, err := client.Get("/v1/apps/"+ctx.Auth.App+"/catalog", &cat); err != nil {
		sp.Stop()
		return err
	}
	var off struct {
		Offerings []codegen.Offering `json:"offerings"`
	}
	if _, err := client.Get("/v1/apps/"+ctx.Auth.App+"/catalog/offerings", &off); err != nil {
		sp.Stop()
		return err
	}
	sp.Stop()

	source, err := codegen.Render(lang, cat, off.Offerings)
	if err != nil {
		return ui.Usage("%s", err.Error())
	}
	if out := ctx.Args.Str("out"); out != "" {
		// #nosec G306 -- generated source is intentionally a normal project file, not a secret.
		if err := os.WriteFile(out, []byte(source), 0o644); err != nil {
			return &ui.ExitError{Code: 2, Message: "could not write " + out + ": " + err.Error()}
		}
		if ui.Current.JSON {
			ui.JSON(map[string]any{"file": out, "lang": lang})
			return nil
		}
		ui.OK("wrote %s (%s)", out, lang)
		return nil
	}
	// The generated source IS the output; print it raw even in JSON mode.
	if _, err := os.Stdout.WriteString(source); err != nil {
		return err
	}
	return nil
}
