// Package ui is the terminal output layer for the cashsdk CLI.
//
// Rules it enforces everywhere so commands never have to think about them:
//   - Color and glyphs only when stdout is a TTY, NO_COLOR is unset, and TERM != dumb.
//   - Spinners render on stderr, never stdout, and only when stderr is a TTY.
//   - --json mode prints machine output on stdout and nothing decorative anywhere.
//   - One accent color (brand jade), red for failures, yellow for warnings, dim for
//     secondary text. No rainbow output.
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// Mode is decided once at startup from flags + environment.
type Mode struct {
	JSON    bool // --json: machine output only
	Color   bool // styled output allowed
	TTY     bool // stdout is a terminal
	ErrTTY  bool // stderr is a terminal (spinners)
	Verbose bool
}

var Current = Mode{}

// Init decides the output mode. Call once from main before any output.
func Init(jsonFlag, noColorFlag, verbose bool) {
	Current.JSON = jsonFlag
	Current.TTY = term.IsTerminal(int(os.Stdout.Fd()))
	Current.ErrTTY = term.IsTerminal(int(os.Stderr.Fd()))
	Current.Verbose = verbose
	_, noColorEnv := os.LookupEnv("NO_COLOR")
	Current.Color = Current.TTY && !noColorFlag && !noColorEnv && os.Getenv("TERM") != "dumb" && !jsonFlag
	if !Current.Color {
		// termenv.Ascii, spelled out: Profile's zero value is TrueColor, so a
		// bare 0 here silently forced color INTO pipes instead of out of them.
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}

// Brand palette. Jade is the CashSDK brand green; evergreen anchors headings.
var (
	cJade = lipgloss.AdaptiveColor{Light: "#1A8A6A", Dark: "#2FBF93"}
	cErr  = lipgloss.AdaptiveColor{Light: "#C4442A", Dark: "#F0705A"}
	cWarn = lipgloss.AdaptiveColor{Light: "#9A6B00", Dark: "#E5B454"}
	cDim  = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8B9299"}

	Accent = lipgloss.NewStyle().Foreground(cJade)
	Good   = lipgloss.NewStyle().Foreground(cJade)
	Bad    = lipgloss.NewStyle().Foreground(cErr)
	Warn   = lipgloss.NewStyle().Foreground(cWarn)
	Dim    = lipgloss.NewStyle().Foreground(cDim)
	Bold   = lipgloss.NewStyle().Bold(true)
	Head   = lipgloss.NewStyle().Bold(true).Foreground(cJade)
)

// Glyphs degrade to ASCII when color (and therefore fancy output) is off.
func gOK() string {
	if Current.Color {
		return Good.Render("✓")
	}
	return "ok"
}
func gFail() string {
	if Current.Color {
		return Bad.Render("✗")
	}
	return "x"
}
func gWarn() string {
	if Current.Color {
		return Warn.Render("!")
	}
	return "!"
}
func gSkip() string {
	if Current.Color {
		return Dim.Render("◦")
	}
	return "-"
}
func gDot() string {
	if Current.Color {
		return Dim.Render("•")
	}
	return "*"
}

func GlyphOK() string   { return gOK() }
func GlyphFail() string { return gFail() }
func GlyphWarn() string { return gWarn() }
func GlyphSkip() string { return gSkip() }
func GlyphDot() string  { return gDot() }

// Out prints a line to stdout (suppressed in JSON mode).
func Out(format string, a ...any) {
	if Current.JSON {
		return
	}
	fmt.Fprintf(os.Stdout, format+"\n", a...)
}

// Blank prints an empty line (suppressed in JSON mode).
func Blank() { Out("") }

// Note prints dim secondary text.
func Note(format string, a ...any) { Out("%s", Dim.Render(fmt.Sprintf(format, a...))) }

// OK prints a success line.
func OK(format string, a ...any) { Out("%s %s", gOK(), fmt.Sprintf(format, a...)) }

// Failline prints a failure line (does not exit).
func Failline(format string, a ...any) { Out("%s %s", gFail(), fmt.Sprintf(format, a...)) }

// Warnline prints a warning line.
func Warnline(format string, a ...any) { Out("%s %s", gWarn(), fmt.Sprintf(format, a...)) }

// Title prints a command heading: bold name plus dim context.
func Title(name, context string) {
	if Current.JSON {
		return
	}
	if context != "" {
		Out("%s %s", Head.Render(name), Dim.Render(context))
	} else {
		Out("%s", Head.Render(name))
	}
}

// KV prints an aligned key/value block.
func KV(pairs [][2]string) {
	w := 0
	for _, p := range pairs {
		if len(p[0]) > w {
			w = len(p[0])
		}
	}
	for _, p := range pairs {
		Out("  %s  %s", Dim.Render(pad(p[0], w)), p[1])
	}
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// Columns renders rows with aligned columns and a two-space gutter.
func Columns(rows [][]string) {
	if len(rows) == 0 {
		return
	}
	widths := make([]int, 0)
	for _, r := range rows {
		for i, c := range r {
			cw := lipgloss.Width(c)
			if i >= len(widths) {
				widths = append(widths, cw)
			} else if cw > widths[i] {
				widths[i] = cw
			}
		}
	}
	for _, r := range rows {
		parts := make([]string, len(r))
		for i, c := range r {
			if i == len(r)-1 {
				parts[i] = c
				continue
			}
			parts[i] = c + strings.Repeat(" ", widths[i]-lipgloss.Width(c))
		}
		Out("  %s", strings.Join(parts, "  "))
	}
}

// JSON prints v as indented JSON on stdout. The one printer JSON mode uses.
func JSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// RawJSON re-indents already-encoded JSON and prints it.
func RawJSON(raw []byte) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Fprintln(os.Stdout, string(raw))
		return
	}
	JSON(v)
}

// ── spinner ──────────────────────────────────────────────────────────────────

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner renders progress on stderr while a network call runs. Safe to use
// unconditionally: it is a no-op unless stderr is a TTY and JSON mode is off.
type Spinner struct {
	mu    sync.Mutex
	label string
	stop  chan struct{}
	done  chan struct{}
	live  bool
	start time.Time
}

func NewSpinner(label string) *Spinner {
	s := &Spinner{label: label, start: time.Now()}
	if !Current.ErrTTY || Current.JSON {
		return s
	}
	s.live = true
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.loop()
	return s
}

func (s *Spinner) loop() {
	defer close(s.done)
	i := 0
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			fmt.Fprint(os.Stderr, "\r\033[2K")
			return
		case <-tick.C:
			s.mu.Lock()
			label := s.label
			s.mu.Unlock()
			frame := spinFrames[i%len(spinFrames)]
			elapsed := ""
			if d := time.Since(s.start); d > 3*time.Second {
				elapsed = Dim.Render(fmt.Sprintf("  %ds", int(d.Seconds())))
			}
			fmt.Fprintf(os.Stderr, "\r\033[2K%s %s%s", Accent.Render(frame), label, elapsed)
			i++
		}
	}
}

// Update changes the label while spinning.
func (s *Spinner) Update(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

// Stop clears the spinner line. Print the outcome yourself afterwards.
func (s *Spinner) Stop() {
	if !s.live {
		return
	}
	s.live = false
	close(s.stop)
	<-s.done
}

// ── errors ───────────────────────────────────────────────────────────────────

// ExitError carries the process exit code alongside the message.
type ExitError struct {
	Code        int // 2 usage, 3 auth, 4 remote, 5 blocked on a human, 6 partial
	Message     string
	Remediation []string
}

func (e *ExitError) Error() string { return e.Message }

func Usage(format string, a ...any) *ExitError {
	return &ExitError{Code: 2, Message: fmt.Sprintf(format, a...)}
}

// Render prints an ExitError the way every command should: a red cross, the
// message, then dim remediation lines the user can act on. Always on stderr,
// so JSON mode keeps stdout as exactly one machine-readable document.
func (e *ExitError) Render() {
	if Current.JSON {
		raw, _ := json.Marshal(map[string]any{"error": e.Message, "code": e.Code, "remediation": e.Remediation})
		fmt.Fprintln(os.Stderr, string(raw))
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", gFail(), e.Message)
	for _, r := range e.Remediation {
		fmt.Fprintf(os.Stderr, "  %s\n", Dim.Render(r))
	}
}
