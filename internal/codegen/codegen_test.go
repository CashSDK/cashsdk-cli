package codegen

import (
	"strings"
	"testing"
)

var catalog = Catalog{
	Products:     []Identified{{"pro.monthly"}, {"pro.annual_v2"}},
	Entitlements: []Identified{{"pro"}, {"pro.plus"}},
}

// Two offerings share "$monthly"; the emitted constant must appear once.
var offerings = []Offering{
	{Packages: []Identified{{"$monthly"}, {"$annual"}}},
	{Packages: []Identified{{"$monthly"}}},
}

func TestCamelIdent(t *testing.T) {
	cases := map[string]string{
		"pro.annual_v2": "proAnnualV2",
		"$monthly":      "monthly",
		"2fast":         "_2fast",
		"a-b_c d":       "aBCD",
		"":              "value",
	}
	for in, want := range cases {
		if got := CamelIdent(in); got != want {
			t.Errorf("CamelIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSwift(t *testing.T) {
	out, err := Render("swift", catalog, offerings)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`public static let proMonthly = "pro.monthly"`,
		"public enum CatalogEntitlements",
		"public enum CatalogPackages",
		"GENERATED FILE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("swift output missing %q", want)
		}
	}
	if n := strings.Count(out, `monthly = "$monthly"`); n != 1 {
		t.Errorf("duplicate slot not de-duped, count=%d", n)
	}
}

func TestKotlin(t *testing.T) {
	out, err := Render("kotlin", catalog, offerings)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`const val proMonthly = "pro.monthly"`, "object CatalogProducts"} {
		if !strings.Contains(out, want) {
			t.Errorf("kotlin output missing %q", want)
		}
	}
}

func TestTypescript(t *testing.T) {
	out, err := Render("typescript", catalog, offerings)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`proMonthly: "pro.monthly"`,
		"as const;",
		"export type CatalogProductId",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("typescript output missing %q", want)
		}
	}
}

func TestEmptyCatalogStillValid(t *testing.T) {
	out, err := Render("swift", Catalog{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "public enum CatalogProducts {") {
		t.Error("empty catalog should still emit valid scaffolding")
	}
}

func TestUnknownLang(t *testing.T) {
	if _, err := Render("rust", catalog, offerings); err == nil {
		t.Error("unknown language must error")
	}
}

func TestNoEmDashInOutput(t *testing.T) {
	for _, lang := range Langs {
		out, _ := Render(lang, catalog, offerings)
		if strings.Contains(out, "—") {
			t.Errorf("%s output contains an em dash", lang)
		}
	}
}
