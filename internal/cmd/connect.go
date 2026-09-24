package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cashsdk/cashsdk-cli/internal/api"
	"github.com/cashsdk/cashsdk-cli/internal/config"
	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

func init() {
	register(command{
		name:    "connect",
		summary: "Print or write the MCP config for Claude Code, Cursor, or Codex",
		usage:   "cashsdk connect [claude|cursor|codex|print] [--write]",
		flags: []FlagSpec{
			{Name: "write", Help: "cursor only: write .cursor/mcp.json in the current repo"},
			{Name: "mcp-url", Value: true, Help: "MCP endpoint (default derived from --api-url)"},
		},
		run: runConnect,
	})
}

// mcpURL derives the MCP endpoint from the API host. The production API serves
// MCP on both its own origin and mcp.cashsdk.com; advertise the branded host
// for production and same-origin for everything else.
func mcpURL(ctx *Ctx) (string, error) {
	endpoint := ""
	if u := ctx.Args.Str("mcp-url"); u != "" {
		endpoint = u
	} else {
		base := strings.TrimRight(ctx.Auth.APIURL, "/")
		if base == "https://api.cashsdk.com" {
			endpoint = "https://mcp.cashsdk.com/mcp"
		} else {
			endpoint = base + "/mcp"
		}
	}
	if err := api.ValidateEndpointURL(endpoint); err != nil {
		return "", err
	}
	return endpoint, nil
}

type connectPayloads struct {
	claudeCommand string
	cursorConfig  map[string]any
	codexBlock    string
}

func buildConnect(url, token string) connectPayloads {
	return connectPayloads{
		claudeCommand: fmt.Sprintf(
			`claude mcp add cashsdk --transport http --url %s --header %s`,
			shellQuote(url), shellQuote("Authorization: Bearer "+token)),
		cursorConfig: map[string]any{
			"mcpServers": map[string]any{
				"cashsdk": map[string]any{
					"type":    "http",
					"url":     url,
					"headers": map[string]string{"Authorization": "Bearer " + token},
				},
			},
		},
		codexBlock: strings.Join([]string{
			"[mcp_servers.cashsdk]",
			fmt.Sprintf("url = %q", url),
			`bearer_token_env_var = "CASHSDK_TOKEN"`,
		}, "\n"),
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func runConnect(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	target := ""
	if len(ctx.Args.Positional) > 0 {
		target = ctx.Args.Positional[0]
	}
	endpoint, err := mcpURL(ctx)
	if err != nil {
		return ui.Usage("invalid MCP endpoint: %v", err)
	}
	p := buildConnect(endpoint, ctx.Auth.Token)

	claudeDoc := map[string]any{"command": p.claudeCommand}
	cursorDoc := map[string]any{"file": ".cursor/mcp.json", "config": p.cursorConfig,
		"note": "enable the server in Cursor Settings, MCP; the file contains your token"}
	codexDoc := map[string]any{"file": "~/.codex/config.toml", "config": p.codexBlock,
		"note": "export CASHSDK_TOKEN in the environment Codex starts from, then restart Codex"}

	switch target {
	case "claude":
		if ui.Current.JSON {
			ui.JSON(map[string]any{"client": "claude", "claude": claudeDoc})
			return nil
		}
		printClaude(p)
	case "cursor":
		if ctx.Args.Bool("write") {
			return writeCursor(p.cursorConfig)
		}
		if ui.Current.JSON {
			ui.JSON(map[string]any{"client": "cursor", "cursor": cursorDoc})
			return nil
		}
		printCursor(p)
	case "codex":
		if ui.Current.JSON {
			ui.JSON(map[string]any{"client": "codex", "codex": codexDoc})
			return nil
		}
		printCodex(p)
	case "", "print":
		// One machine-readable document, always, even for the everything view.
		if ui.Current.JSON {
			ui.JSON(map[string]any{"clients": map[string]any{
				"claude": claudeDoc, "cursor": cursorDoc, "codex": codexDoc,
			}})
			return nil
		}
		printClaude(p)
		ui.Blank()
		printCursor(p)
		ui.Blank()
		printCodex(p)
	default:
		return ui.Usage("unknown client %q (expected claude, cursor, codex, or print)", target)
	}
	if !ui.Current.JSON {
		ui.Blank()
		ui.Note("a setup token expires 24h after minting; for a config you keep, use a durable MCP token (dashboard: Settings, MCP)")
	}
	return nil
}

func printClaude(p connectPayloads) {
	ui.Title("Claude Code", "one command")
	ui.Out("  %s", p.claudeCommand)
}

func printCursor(p connectPayloads) {
	raw, _ := json.MarshalIndent(p.cursorConfig, "", "  ")
	ui.Title("Cursor", ".cursor/mcp.json (then enable it in Settings, MCP)")
	for _, line := range strings.Split(string(raw), "\n") {
		ui.Out("  %s", line)
	}
	ui.Warnline("this file contains your token; keep it out of version control")
}

func writeCursor(cfg map[string]any) error {
	dir := ".cursor"
	path := filepath.Join(dir, "mcp.json")
	merged := cfg
	// #nosec G304 -- path is the constant .cursor/mcp.json in the current project.
	if raw, err := os.ReadFile(path); err == nil {
		var existing map[string]any
		if jsonErr := json.Unmarshal(raw, &existing); jsonErr != nil {
			// Never clobber a config we cannot parse; the user may have other
			// servers in there behind a syntax slip.
			return &ui.ExitError{Code: 2,
				Message: path + " exists but is not valid JSON; refusing to overwrite it",
				Remediation: []string{
					"parse error: " + jsonErr.Error(),
					"fix the file (or move it aside), then re-run `cashsdk connect cursor --write`",
					"or print the block with `cashsdk connect cursor` and merge it by hand",
				}}
		}
		servers, _ := existing["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["cashsdk"] = cfg["mcpServers"].(map[string]any)["cashsdk"]
		existing["mcpServers"] = servers
		merged = existing
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(merged, "", "  ")
	if err := config.WritePrivateFile(path, append(raw, '\n')); err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.JSON(map[string]any{"wrote": path})
		return nil
	}
	ui.OK("wrote %s", path)
	ui.Warnline("it contains your token; add it to .gitignore if this repo is shared, then enable the server in Cursor Settings, MCP")
	return nil
}

func printCodex(p connectPayloads) {
	ui.Title("Codex", "append to ~/.codex/config.toml, export CASHSDK_TOKEN where Codex starts, then restart Codex")
	for _, line := range strings.Split(p.codexBlock, "\n") {
		ui.Out("  %s", line)
	}
}
