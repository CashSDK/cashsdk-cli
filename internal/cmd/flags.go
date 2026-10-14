package cmd

import (
	"strings"

	"github.com/cashsdk/cashsdk-cli/internal/ui"
)

// FlagSpec declares one flag a command accepts. Bool flags take no value.
type FlagSpec struct {
	Name    string // long name without dashes
	Short   string // optional single letter
	Value   bool   // expects a value
	Multi   bool   // may be repeated; read it with Multi()
	Help    string
	Example string // shown in help instead of a generic placeholder
}

// Parsed holds the split argv for one command invocation.
type Parsed struct {
	Positional []string
	flags      map[string]string
	set        map[string]bool
}

func (p *Parsed) Str(name string) string { return p.flags[name] }
func (p *Parsed) Bool(name string) bool  { return p.set[name] && p.flags[name] != "false" }
func (p *Parsed) Has(name string) bool   { return p.set[name] }
func (p *Parsed) StrOr(name, d string) string {
	if p.set[name] {
		return p.flags[name]
	}
	return d
}

// Multi returns every value a repeated flag was given, also splitting each on
// commas so `--map a=pro --map b=pro` and `--map a=pro,b=pro` read the same.
func (p *Parsed) Multi(name string) []string {
	if !p.set[name] {
		return nil
	}
	out := []string{}
	for _, chunk := range strings.Split(p.flags[name], "\x1f") {
		for _, v := range strings.Split(chunk, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// parseArgs splits argv against the declared specs. Unknown flags are a usage
// error so typos never silently no-op (the old CLI treated every unknown flag
// as a key/value pair and dropped it).
func parseArgs(args []string, specs []FlagSpec) (*Parsed, *ui.ExitError) {
	byName := map[string]FlagSpec{}
	for _, s := range specs {
		byName["--"+s.Name] = s
		if s.Short != "" {
			byName["-"+s.Short] = s
		}
	}
	p := &Parsed{flags: map[string]string{}, set: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			p.Positional = append(p.Positional, a)
			continue
		}
		name, inline, hasInline := strings.Cut(a, "=")
		spec, ok := byName[name]
		if !ok {
			return nil, ui.Usage("unknown flag %s (see --help)", name)
		}
		switch {
		case !spec.Value:
			if hasInline {
				return nil, ui.Usage("flag --%s takes no value", spec.Name)
			}
			p.flags[spec.Name] = "true"
		case hasInline:
			if err := appendFlag(p, spec, inline); err != nil {
				return nil, err
			}
		default:
			if i+1 >= len(args) {
				return nil, ui.Usage("flag --%s needs a value", spec.Name)
			}
			i++
			if err := appendFlag(p, spec, args[i]); err != nil {
				return nil, err
			}
		}
		p.set[spec.Name] = true
	}
	return p, nil
}

// appendFlag stores a value flag. Only flags declared Multi may repeat (their
// values are joined for Multi() to split); repeating any other flag is a usage
// error rather than a silent last-wins or a joined value leaking into a URL.
func appendFlag(p *Parsed, spec FlagSpec, value string) *ui.ExitError {
	if prev, dup := p.flags[spec.Name]; dup && p.set[spec.Name] {
		if !spec.Multi {
			return ui.Usage("flag --%s was given more than once", spec.Name)
		}
		p.flags[spec.Name] = prev + "\x1f" + value
		return nil
	}
	p.flags[spec.Name] = value
	return nil
}
