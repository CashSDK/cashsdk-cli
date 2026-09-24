// Package cmd routes argv to command implementations and owns the help output.
package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cashsdk/cashsdk-cli/internal/api"
	"github.com/cashsdk/cashsdk-cli/internal/config"
	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

// Version is stamped at build time via -ldflags "-X ...cmd.Version=x.y.z".
var Version = "dev"

// Ctx is everything a command needs, resolved once per invocation.
type Ctx struct {
	Cfg    *config.Config
	Auth   config.Auth
	Args   *Parsed
}

// Client returns an API client for the resolved credential.
func (c *Ctx) Client() *api.Client {
	return api.New(c.Auth.APIURL, c.Auth.Token)
}

// NeedToken fails with exit 3 guidance when no credential is configured.
func (c *Ctx) NeedToken() *ui.ExitError {
	if c.Auth.Token != "" {
		if !config.TokenSyntaxValid(c.Auth.Token) {
			return &ui.ExitError{Code: 3, Message: "credential has an invalid format",
				Remediation: []string{"use an unmodified token from the dashboard"}}
		}
		if config.TokenKind(c.Auth.Token) == "secret" {
			return &ui.ExitError{Code: 3, Message: "a secret key (csk_sk_) cannot authenticate the CLI",
				Remediation: []string{
					"secret keys only work on the server data API, not these endpoints",
					"use a setup token (csk_st_, from the dashboard's Generate prompt) or an MCP token (Settings, MCP)",
					"then: cashsdk auth set",
				}}
		}
		return nil
	}
	return &ui.ExitError{Code: 3, Message: "no credential configured",
		Remediation: []string{
			"cashsdk auth set --app <app_id> --token <csk_st_... or csk_mcp_...>",
			"or set CASHSDK_TOKEN in the environment",
			"tokens come from the dashboard: an app's Generate prompt (setup token) or Settings, MCP (durable token)",
		}}
}

// NeedApp fails with exit 2 guidance when no app id is resolvable.
func (c *Ctx) NeedApp() *ui.ExitError {
	if c.Auth.App != "" {
		return nil
	}
	return &ui.ExitError{Code: 2, Message: "no app id: pass --app, set CASHSDK_APP, or save one with `cashsdk auth set --app <id>`"}
}

type command struct {
	name    string
	summary string
	usage   string
	flags   []FlagSpec
	run     func(*Ctx) error
	hidden  bool
}

var globalFlags = []FlagSpec{
	{Name: "app", Value: true, Help: "CashSDK app id (default: saved app or CASHSDK_APP)"},
	{Name: "token", Value: true, Help: "credential for this invocation (default: saved token or CASHSDK_TOKEN)"},
	{Name: "api-url", Value: true, Help: "API base URL (default: https://api.cashsdk.com)"},
	{Name: "json", Help: "machine-readable output"},
	{Name: "no-color", Help: "disable styled output"},
	{Name: "verbose", Help: "extra diagnostics on stderr"},
	{Name: "version", Help: "print the CLI version"},
	{Name: "help", Short: "h", Help: "show help"},
}

var commands []command

func register(c command) { commands = append(commands, c) }

// Dispatch is the entry point main() calls. Returns the process exit code.
func Dispatch(argv []string) int {
	api.Version = Version

	// Split "command [sub]" from flags. The first two non-flag tokens select
	// the command; everything else is handed to the command's parser.
	name, rest := splitCommand(argv)

	var cmdDef *command
	for i := range commands {
		if commands[i].name == name {
			cmdDef = &commands[i]
			break
		}
	}

	specs := append([]FlagSpec{}, globalFlags...)
	if cmdDef != nil {
		specs = append(specs, cmdDef.flags...)
	}
	parsed, uerr := parseArgs(rest, specs)
	if uerr != nil {
		ui.Init(false, false, false)
		uerr.Render()
		return uerr.Code
	}

	ui.Init(parsed.Bool("json"), parsed.Bool("no-color"), parsed.Bool("verbose"))

	if name == "version" || parsed.Bool("version") {
		fmt.Println("cashsdk " + Version)
		return 0
	}
	if name == "" || name == "help" {
		printRootHelp()
		if name == "" && len(argv) > 0 {
			return 2
		}
		return 0
	}
	if cmdDef == nil {
		e := ui.Usage("unknown command %q (run `cashsdk help`)", name)
		e.Render()
		return 2
	}
	if parsed.Bool("help") {
		printCommandHelp(cmdDef)
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		(&ui.ExitError{Code: 2, Message: err.Error(), Remediation: []string{
			"the config file is unreadable; delete it and run `cashsdk auth set` again",
			"path: " + config.Path(),
		}}).Render()
		return 2
	}
	ctx := &Ctx{
		Cfg:  cfg,
		Auth: config.Resolve(cfg, parsed.Str("token"), parsed.Str("app"), parsed.Str("api-url")),
		Args: parsed,
	}

	if err := cmdDef.run(ctx); err != nil {
		return renderError(err)
	}
	return 0
}

func renderError(err error) int {
	switch e := err.(type) {
	case *ui.ExitError:
		e.Render()
		return e.Code
	case *api.Error:
		ex := &ui.ExitError{Code: e.ExitCode(), Message: e.Message}
		if e.Remediation != "" {
			ex.Remediation = []string{e.Remediation}
		}
		ex.Render()
		return ex.Code
	default:
		(&ui.ExitError{Code: 4, Message: err.Error()}).Render()
		return 4
	}
}

// splitCommand pulls the command path ("setup run") off the front of argv.
func splitCommand(argv []string) (string, []string) {
	words := []string{}
	rest := []string{}
	for i, a := range argv {
		if strings.HasPrefix(a, "-") {
			rest = append(rest, argv[i:]...)
			break
		}
		if len(words) < 2 {
			words = append(words, a)
			continue
		}
		rest = append(rest, argv[i:]...)
		break
	}
	// Try the two-word command first, then one-word with the second as positional.
	if len(words) == 2 {
		two := words[0] + " " + words[1]
		for i := range commands {
			if commands[i].name == two {
				return two, rest
			}
		}
		return words[0], append([]string{words[1]}, rest...)
	}
	if len(words) == 1 {
		return words[0], rest
	}
	return "", rest
}

// ── help ─────────────────────────────────────────────────────────────────────

var helpSections = []struct {
	title string
	names []string
}{
	{"Getting started", []string{"login", "auth set", "auth status", "auth clear", "whoami", "doctor"}},
	{"Setup", []string{"setup guide", "setup run", "setup status", "setup verify", "snippets", "connect"}},
	{"Catalog", []string{"catalog", "catalog sync", "catalog push", "codegen"}},
	{"Monetization", []string{"paywalls", "templates", "apps"}},
	{"Observe", []string{"events", "transactions"}},
}

func printRootHelp() {
	if ui.Current.JSON {
		names := []string{}
		for _, c := range commands {
			if !c.hidden {
				names = append(names, c.name)
			}
		}
		sort.Strings(names)
		ui.JSON(map[string]any{"version": Version, "commands": names})
		return
	}
	out := os.Stdout
	fmt.Fprintf(out, "%s %s\n", ui.Head.Render("CashSDK CLI"), ui.Dim.Render(Version))
	fmt.Fprintf(out, "%s\n\n", ui.Dim.Render("In-app purchases, subscriptions and paywalls, from your terminal."))
	fmt.Fprintf(out, "%s cashsdk <command> [flags]\n", ui.Bold.Render("Usage:"))

	byName := map[string]command{}
	for _, c := range commands {
		byName[c.name] = c
	}
	for _, sec := range helpSections {
		rows := [][]string{}
		for _, n := range sec.names {
			if c, ok := byName[n]; ok && !c.hidden {
				rows = append(rows, []string{ui.Accent.Render(c.name), c.summary})
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s\n", ui.Bold.Render(sec.title))
		printAligned(rows)
	}
	fmt.Fprintf(out, "\n%s\n", ui.Bold.Render("Global flags"))
	rows := [][]string{}
	for _, f := range globalFlags {
		rows = append(rows, []string{ui.Accent.Render("--" + f.Name), f.Help})
	}
	printAligned(rows)
	fmt.Fprintf(out, "\n%s\n", ui.Dim.Render("Run `cashsdk <command> --help` for details. Docs: https://docs.cashsdk.com/cli"))
}

func printCommandHelp(c *command) {
	out := os.Stdout
	fmt.Fprintf(out, "%s  %s\n\n", ui.Head.Render("cashsdk "+c.name), ui.Dim.Render(c.summary))
	usage := c.usage
	if usage == "" {
		usage = "cashsdk " + c.name + " [flags]"
	}
	fmt.Fprintf(out, "%s %s\n", ui.Bold.Render("Usage:"), usage)
	if len(c.flags) > 0 {
		fmt.Fprintf(out, "\n%s\n", ui.Bold.Render("Flags"))
		rows := [][]string{}
		for _, f := range c.flags {
			name := "--" + f.Name
			if f.Value {
				ph := f.Example
				if ph == "" {
					ph = "<value>"
				}
				name += " " + ph
			}
			rows = append(rows, []string{ui.Accent.Render(name), f.Help})
		}
		printAligned(rows)
	}
	fmt.Fprintf(out, "\n%s\n", ui.Dim.Render("Global flags: --app --token --api-url --json --no-color (see `cashsdk help`)"))
}

func printAligned(rows [][]string) {
	w := 0
	for _, r := range rows {
		if l := visibleLen(r[0]); l > w {
			w = l
		}
	}
	for _, r := range rows {
		padding := strings.Repeat(" ", w-visibleLen(r[0])+2)
		fmt.Fprintf(os.Stdout, "  %s%s%s\n", r[0], padding, r[1])
	}
}

func visibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == '\033' {
			inEsc = true
			continue
		}
		n++
	}
	return n
}
