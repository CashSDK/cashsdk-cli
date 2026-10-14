package cmd

import (
	"strings"

	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

func init() {
	register(command{
		name:    "snippets",
		summary: "Compilable SDK integration snippets for this app",
		usage:   "cashsdk snippets [--platform ios|macos|android|web|macos-direct|chrome-extension|stripe]",
		flags: []FlagSpec{
			{Name: "platform", Value: true, Example: "ios|macos|android|web|macos-direct|chrome-extension|stripe", Help: "which platform (default: the app's own)"},
		},
		run: runSnippets,
	})
}

// snippetsPayload covers both platforms: iOS carries sdk.spm_url, Android
// carries sdk.maven_coordinate and friends. Fields absent on one platform
// simply decode empty.
type snippetsPayload struct {
	SDK struct {
		SpmURL          string `json:"spm_url"`
		MavenCoordinate string `json:"maven_coordinate"`
		MavenRepository string `json:"maven_repository"`
		PlayBilling     string `json:"play_billing"`
		Version         string `json:"version"`
		DocsURL         string `json:"docs_url"`
		Published       *bool  `json:"published"`
		Required        *bool  `json:"required"`
	} `json:"sdk"`
	Snippets []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		FileHint string `json:"file_hint"`
		Language string `json:"language"`
		Code     string `json:"code"`
		Verify   string `json:"verify"`
	} `json:"snippets"`
}

func runSnippets(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	path := "/v1/apps/" + ctx.Auth.App + "/snippets"
	platform := ctx.Args.Str("platform")
	if platform != "" {
		if !validPlatform(platform) {
			return ui.Usage("--platform must be ios, macos, android, web, macos-direct, chrome-extension or stripe")
		}
		path += "?platform=" + platform
	}
	sp := ui.NewSpinner("fetching snippets")
	var s snippetsPayload
	raw, err := ctx.Client().Get(path, &s)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
		return nil
	}

	ui.Title("Integration snippets", ctx.Auth.App)
	pairs := [][2]string{}
	if s.SDK.SpmURL != "" {
		pairs = append(pairs, [2]string{"swift package", s.SDK.SpmURL + " from " + s.SDK.Version})
	}
	if s.SDK.MavenCoordinate != "" {
		pairs = append(pairs, [2]string{"gradle", s.SDK.MavenCoordinate + " (" + s.SDK.MavenRepository + ")"})
		if s.SDK.PlayBilling != "" {
			pairs = append(pairs, [2]string{"play billing", s.SDK.PlayBilling})
		}
	}
	if s.SDK.DocsURL != "" {
		pairs = append(pairs, [2]string{"guide", s.SDK.DocsURL})
	}
	if s.SDK.Published != nil && !*s.SDK.Published {
		pairs = append(pairs, [2]string{"availability", "source candidate; not published"})
	}
	if s.SDK.Required != nil && !*s.SDK.Required {
		pairs = append(pairs, [2]string{"client SDK", "not required"})
	}
	ui.KV(pairs)

	for _, sn := range s.Snippets {
		ui.Blank()
		ui.Out("%s %s", ui.Bold.Render(sn.Title), ui.Dim.Render("("+sn.ID+", "+sn.Language+")"))
		if sn.FileHint != "" {
			ui.Out("%s", ui.Dim.Render("  where: "+sn.FileHint))
		}
		for _, line := range strings.Split(strings.TrimRight(sn.Code, "\n"), "\n") {
			ui.Out("    %s", line)
		}
		if sn.Verify != "" {
			ui.Out("%s", ui.Dim.Render("  verify: "+sn.Verify))
		}
	}
	ui.Blank()
	ui.Note("Use your authenticated app user ID consistently. Keep server and provider secrets on your backend. Follow the guide for verification and package availability.")
	return nil
}

func validPlatform(platform string) bool {
	switch platform {
	case "ios", "macos", "android", "web", "macos-direct", "chrome-extension", "stripe":
		return true
	default:
		return false
	}
}
