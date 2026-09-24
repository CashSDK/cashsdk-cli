// Package codegen renders typed catalog constants so product ids, entitlement
// ids and offering package slots become symbols in app code. A rename that is
// not propagated then fails to compile instead of failing silently at runtime.
package codegen

import (
	"fmt"
	"regexp"
	"strings"
)

var Langs = []string{"swift", "kotlin", "typescript"}

var nonIdent = regexp.MustCompile(`[^A-Za-z0-9]+`)
var leadingDigit = regexp.MustCompile(`^[0-9]`)

// CamelIdent turns "pro.annual_v2" into "proAnnualV2"; always a legal identifier.
func CamelIdent(raw string) string {
	cleaned := strings.TrimSpace(nonIdent.ReplaceAllString(raw, " "))
	parts := strings.Fields(cleaned)
	if len(parts) == 0 {
		parts = []string{"value"}
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(parts[0]))
	for _, p := range parts[1:] {
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	ident := b.String()
	if leadingDigit.MatchString(ident) {
		return "_" + ident
	}
	return ident
}

const headerLine1 = "CashSDK GENERATED FILE. Do not edit by hand."
const headerLine2 = "Regenerate with:  cashsdk codegen --lang <swift|kotlin|typescript> --out <file>"

// Catalog is the subset of the API catalog payload codegen needs.
type Catalog struct {
	Products     []Identified `json:"products"`
	Entitlements []Identified `json:"entitlements"`
}

type Identified struct {
	Identifier string `json:"identifier"`
}

type Offering struct {
	Packages []Identified `json:"packages"`
}

// Render emits the constants file for one language.
func Render(lang string, catalog Catalog, offerings []Offering) (string, error) {
	// Slot ids are shared across offerings; de-dupe so "$monthly" emits once.
	seen := map[string]bool{}
	slots := []string{}
	for _, o := range offerings {
		for _, p := range o.Packages {
			if !seen[p.Identifier] {
				seen[p.Identifier] = true
				slots = append(slots, p.Identifier)
			}
		}
	}
	products := identifiers(catalog.Products)
	entitlements := identifiers(catalog.Entitlements)

	switch lang {
	case "swift":
		return renderBlocks("// "+headerLine1+"\n// "+headerLine2,
			func(name string, vals []string) string {
				lines := []string{"public enum " + name + " {"}
				for _, v := range vals {
					lines = append(lines, fmt.Sprintf("    public static let %s = %q", CamelIdent(v), v))
				}
				return strings.Join(append(lines, "}"), "\n")
			}, products, entitlements, slots), nil
	case "kotlin":
		return renderBlocks("// "+headerLine1+"\n// "+headerLine2,
			func(name string, vals []string) string {
				lines := []string{"object " + name + " {"}
				for _, v := range vals {
					lines = append(lines, fmt.Sprintf("    const val %s = %q", CamelIdent(v), v))
				}
				return strings.Join(append(lines, "}"), "\n")
			}, products, entitlements, slots), nil
	case "typescript":
		obj := func(name string, vals []string) string {
			lines := []string{"export const " + name + " = {"}
			for _, v := range vals {
				lines = append(lines, fmt.Sprintf("  %s: %q,", CamelIdent(v), v))
			}
			lines = append(lines, "} as const;")
			singular := strings.TrimSuffix(name, "s")
			lines = append(lines, fmt.Sprintf("export type %sId = (typeof %s)[keyof typeof %s];", singular, name, name))
			return strings.Join(lines, "\n")
		}
		return "/*\n * " + headerLine1 + "\n * " + headerLine2 + "\n */\n\n" +
			obj("CatalogProducts", products) + "\n\n" +
			obj("CatalogEntitlements", entitlements) + "\n\n" +
			obj("CatalogPackages", slots) + "\n", nil
	default:
		return "", fmt.Errorf("unknown --lang %q, expected one of: %s", lang, strings.Join(Langs, ", "))
	}
}

func identifiers(items []Identified) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Identifier
	}
	return out
}

func renderBlocks(header string, block func(string, []string) string, products, entitlements, slots []string) string {
	return header + "\n\n" +
		block("CatalogProducts", products) + "\n\n" +
		block("CatalogEntitlements", entitlements) + "\n\n" +
		block("CatalogPackages", slots) + "\n"
}
