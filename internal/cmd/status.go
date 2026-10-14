package cmd

import (
	"fmt"
	"time"

	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

func init() {
	register(command{
		name:    "setup status",
		summary: "The live setup checklist, computed from observed state",
		flags: []FlagSpec{
			{Name: "require-complete", Help: "exit non-zero unless every item passed (CI gate)"},
		},
		run: runStatus,
	})
	register(command{
		name:    "checklist",
		summary: "Alias of `setup status`",
		flags: []FlagSpec{
			{Name: "require-complete", Help: "exit non-zero unless every item passed (CI gate)"},
		},
		run:    runStatus,
		hidden: true,
	})
	register(command{
		name:    "setup verify",
		summary: "Watch the checklist until everything passes",
		usage:   "cashsdk setup verify [--wait] [--timeout <dur>] [--interval <dur>]",
		flags: []FlagSpec{
			{Name: "wait", Help: "poll until complete instead of checking once"},
			{Name: "timeout", Value: true, Example: "<dur>", Help: "give up after this long (default 20m)"},
			{Name: "interval", Value: true, Example: "<dur>", Help: "poll interval (default 3s)"},
		},
		run: runVerify,
	})
}

// checklistPayload matches GET /v1/apps/:id/setup/checklist. Statuses are
// exactly "passed" and "pending"; nothing else is ever returned.
type checklistPayload struct {
	AppID   string `json:"app_id"`
	Summary struct {
		Passed   int  `json:"passed"`
		Pending  int  `json:"pending"`
		Total    int  `json:"total"`
		Complete bool `json:"complete"`
	} `json:"summary"`
	Items []checklistItem `json:"items"`
}

type checklistItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Type   string `json:"type"` // "action" or "auto"
	Title  string `json:"title"`
}

func fetchChecklist(ctx *Ctx) (*checklistPayload, []byte, error) {
	var c checklistPayload
	raw, err := ctx.Client().Get("/v1/apps/"+ctx.Auth.App+"/setup/checklist", &c)
	return &c, raw, err
}

func summaryLine(c *checklistPayload) string {
	if c.Summary.Complete {
		return ui.Good.Render(fmt.Sprintf("%d of %d passed, setup complete", c.Summary.Passed, c.Summary.Total))
	}
	return fmt.Sprintf("%d of %d passed", c.Summary.Passed, c.Summary.Total)
}

func printChecklist(c *checklistPayload) {
	for _, it := range c.Items {
		switch it.Status {
		case "passed":
			ui.Out("  %s %s", ui.GlyphOK(), it.Title)
		default:
			note := ""
			if it.Type == "auto" {
				note = ui.Dim.Render("  verified automatically")
			}
			ui.Out("  %s %s%s", ui.GlyphDot(), ui.Dim.Render(it.Title), note)
		}
	}
	ui.Blank()
	ui.Out("  %s", summaryLine(c))
}

func runStatus(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	sp := ui.NewSpinner("computing checklist")
	c, raw, err := fetchChecklist(ctx)
	sp.Stop()
	if err != nil {
		return err
	}
	if ui.Current.JSON {
		ui.RawJSON(raw)
	} else {
		ui.Title("Setup checklist", ctx.Auth.App)
		ui.Blank()
		printChecklist(c)
	}
	if ctx.Args.Bool("require-complete") && !c.Summary.Complete {
		return &ui.ExitError{Code: 1, Message: fmt.Sprintf("checklist incomplete: %d of %d passed", c.Summary.Passed, c.Summary.Total)}
	}
	return nil
}

func runVerify(ctx *Ctx) error {
	if e := ctx.NeedToken(); e != nil {
		return e
	}
	if e := ctx.NeedApp(); e != nil {
		return e
	}
	c, _, err := fetchChecklist(ctx)
	if err != nil {
		return err
	}
	if c.Summary.Complete {
		ui.OK("setup complete: %s", summaryLine(c))
		if ui.Current.JSON {
			ui.JSON(map[string]any{"complete": true, "summary": c.Summary})
		}
		return nil
	}
	if !ctx.Args.Bool("wait") {
		pending := []string{}
		for _, it := range c.Items {
			if it.Status != "passed" {
				pending = append(pending, it.ID)
			}
		}
		if ui.Current.JSON {
			ui.JSON(map[string]any{"complete": false, "summary": c.Summary, "pending": pending})
			return &ui.ExitError{Code: 5, Message: "setup incomplete"}
		}
		ui.Title("Setup verify", ctx.Auth.App)
		ui.Blank()
		printChecklist(c)
		ui.Blank()
		ui.Note("run with --wait to watch until it completes")
		return &ui.ExitError{Code: 5, Message: fmt.Sprintf("%d item(s) still pending", c.Summary.Pending)}
	}

	timeout, err2 := time.ParseDuration(ctx.Args.StrOr("timeout", "20m"))
	if err2 != nil {
		return ui.Usage("--timeout: %v", err2)
	}
	interval, err2 := time.ParseDuration(ctx.Args.StrOr("interval", "3s"))
	if err2 != nil || interval < time.Second {
		return ui.Usage("--interval must be a duration of at least 1s")
	}

	state := map[string]string{}
	for _, it := range c.Items {
		state[it.ID] = it.Status
	}
	ui.Title("Setup verify", fmt.Sprintf("%s, watching until complete (timeout %s)", ctx.Auth.App, timeout))
	ui.Out("  %s", summaryLine(c))

	deadline := time.Now().Add(timeout)
	sp := ui.NewSpinner(fmt.Sprintf("waiting, %d of %d passed", c.Summary.Passed, c.Summary.Total))
	defer sp.Stop()
	for {
		if time.Now().After(deadline) {
			sp.Stop()
			pending := []string{}
			for _, it := range c.Items {
				if it.Status != "passed" {
					pending = append(pending, it.ID)
				}
			}
			if ui.Current.JSON {
				ui.JSON(map[string]any{"complete": false, "timeout": true, "pending": pending})
			}
			return &ui.ExitError{Code: 5, Message: fmt.Sprintf("timed out after %s with %d item(s) pending", timeout, len(pending)),
				Remediation: pending}
		}
		time.Sleep(interval)
		next, _, err := fetchChecklist(ctx)
		if err != nil {
			// Transient errors should not kill a 20 minute watch; report and keep going.
			sp.Update("retrying: " + err.Error())
			continue
		}
		c = next
		for _, it := range c.Items {
			if state[it.ID] != "passed" && it.Status == "passed" {
				sp.Stop()
				ui.OK("%s %s", it.Title, ui.Dim.Render("("+it.ID+")"))
				sp = ui.NewSpinner("")
			}
			state[it.ID] = it.Status
		}
		sp.Update(fmt.Sprintf("waiting, %d of %d passed", c.Summary.Passed, c.Summary.Total))
		if c.Summary.Complete {
			sp.Stop()
			ui.Blank()
			ui.OK("setup complete: %s", summaryLine(c))
			if ui.Current.JSON {
				ui.JSON(map[string]any{"complete": true, "summary": c.Summary})
			}
			return nil
		}
	}
}
