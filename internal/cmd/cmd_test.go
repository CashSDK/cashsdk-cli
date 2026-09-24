package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// runCLI dispatches argv and captures stdout plus the exit code.
func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := Dispatch(args)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return code, string(out)
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CASHSDK_TOKEN", "")
	t.Setenv("CASHSDK_APP", "")
	t.Setenv("CASHSDK_API_URL", "")
	t.Setenv("NO_COLOR", "1")
}

// ── mock API implementing the extracted REST contract ────────────────────────

type mockProduct struct {
	ID                string   `json:"id"`
	Identifier        string   `json:"identifier"`
	BasePlanID        *string  `json:"basePlanId"`
	Type              string   `json:"type"`
	Store             string   `json:"store"`
	EntitlementIDs    []string `json:"entitlementIds"`
	Status            string   `json:"status"`
	SoldWithoutAccess bool     `json:"soldWithoutAccess"`
}

type mockEntitlement struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	Rank       *int   `json:"rank"`
}

type mockAPI struct {
	t  *testing.T
	mu sync.Mutex

	credPassed   bool
	assnDone     bool // url configured + sandbox test received
	synced       bool
	products     []mockProduct
	entitlements []mockEntitlement
	placements   []map[string]any
	paywalls     []map[string]any
	campaigns    []map[string]any
	writes       []string // every state-changing call, "METHOD path"
	deviceDone   bool     // sdk ping + purchase items
}

func (m *mockAPI) checklist() map[string]any {
	pass := func(b bool) string {
		if b {
			return "passed"
		}
		return "pending"
	}
	mapped := len(m.products) > 0
	for _, p := range m.products {
		if len(p.EntitlementIDs) == 0 && p.Type != "consumable" && !p.SoldWithoutAccess && (p.Status == "" || p.Status == "active") {
			mapped = false
		}
	}
	activeCampaign := false
	for _, c := range m.campaigns {
		if c["status"] == "active" {
			activeCampaign = true
		}
	}
	items := []map[string]any{
		{"id": "app_created", "status": "passed", "type": "action", "title": "App created"},
		{"id": "keys_issued", "status": "passed", "type": "action", "title": "SDK keys issued"},
		{"id": "asc_credentials_valid", "status": pass(m.credPassed), "type": "action", "title": "App Store Connect key validated"},
		{"id": "catalog_synced", "status": pass(m.synced), "type": "action", "title": "Catalog synced from App Store Connect"},
		{"id": "entitlements_mapped", "status": pass(mapped), "type": "action", "title": "Products mapped to entitlements"},
		{"id": "assn_url_configured", "status": pass(m.assnDone), "type": "action", "title": "App Store Server Notifications URL set"},
		{"id": "assn_sandbox_verified", "status": pass(m.assnDone), "type": "auto", "title": "ASSN test received (Sandbox)"},
		{"id": "paywall_published", "status": pass(activeCampaign), "type": "action", "title": "A paywall live in a placement"},
		{"id": "sdk_installed_first_ping", "status": pass(m.deviceDone), "type": "auto", "title": "SDK installed (first ping)"},
		{"id": "sandbox_purchase_verified", "status": pass(m.deviceDone), "type": "auto", "title": "Sandbox purchase verified"},
	}
	passed, pending := 0, 0
	for _, it := range items {
		if it["status"] == "passed" {
			passed++
		} else {
			pending++
		}
	}
	return map[string]any{
		"app_id": "app_1",
		"summary": map[string]any{
			"passed": passed, "pending": pending, "total": len(items), "complete": pending == 0,
		},
		"items": items,
	}
}

func (m *mockAPI) server() *httptest.Server {
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	read := func(r *http.Request, v any) {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, v)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "ts": "t", "sha": "abcdef1234"})
	})
	mux.HandleFunc("GET /v1/templates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []map[string]any{{"id": "tpl_1", "name": "Bold", "category": "recommended", "config": map[string]any{}}})
	})
	mux.HandleFunc("GET /v1/apps/app_1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"id": "app_1", "name": "Bloom", "bundleId": "com.acme.bloom",
			"packageName": nil, "appleAppId": "123", "defaultPlacement": "onboarding_finished",
			"environment": "Sandbox",
		})
	})
	mux.HandleFunc("GET /v1/apps/app_1/setup/checklist", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		writeJSON(w, 200, m.checklist())
	})
	mux.HandleFunc("GET /v1/apps/app_1/setup-diagnostics", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"diagnostics": []any{}, "blockers": 0, "warnings": 0, "checkedAt": "t"})
	})
	mux.HandleFunc("POST /v1/apps/app_1/catalog:sync", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Store  string `json:"store"`
			DryRun bool   `json:"dryRun"`
		}
		read(r, &body)
		if body.Store == "" {
			m.t.Error("catalog:sync must always carry store")
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		diff := map[string]any{
			"store": body.Store, "added": []any{}, "updated": []any{}, "unchanged": []any{},
			"orphaned": []any{}, "applied": !body.DryRun,
		}
		if !m.synced {
			diff["added"] = []map[string]any{
				{"identifier": "pro.monthly", "type": "auto_renewable"},
				{"identifier": "pro.annual", "type": "auto_renewable"},
			}
			if !body.DryRun {
				m.writes = append(m.writes, "SYNC")
				m.synced = true
				m.products = []mockProduct{
					{ID: "prod_1", Identifier: "pro.monthly", Type: "auto_renewable", Store: "app-store"},
					{ID: "prod_2", Identifier: "pro.annual", Type: "auto_renewable", Store: "app-store"},
				}
			}
		} else {
			diff["unchanged"] = []map[string]any{{"identifier": "pro.monthly"}, {"identifier": "pro.annual"}}
		}
		writeJSON(w, 200, diff)
	})
	mux.HandleFunc("GET /v1/apps/app_1/catalog", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		prods := m.products
		if prods == nil {
			prods = []mockProduct{}
		}
		ents := m.entitlements
		if ents == nil {
			ents = []mockEntitlement{}
		}
		writeJSON(w, 200, map[string]any{"entitlements": ents, "products": prods, "groups": []string{}})
	})
	mux.HandleFunc("GET /v1/apps/app_1/catalog/offerings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"offerings": []map[string]any{
			{"id": "off_1", "identifier": "default", "isCurrent": true, "packages": []map[string]any{
				{"id": "pkg_1", "identifier": "$monthly", "position": 0},
			}},
		}})
	})
	mux.HandleFunc("POST /v1/apps/app_1/catalog/entitlements", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Identifier string `json:"identifier"`
		}
		read(r, &body)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.writes = append(m.writes, "ENT "+body.Identifier)
		ent := mockEntitlement{ID: fmt.Sprintf("ent_%d", len(m.entitlements)+1), Identifier: body.Identifier, Name: body.Identifier}
		m.entitlements = append(m.entitlements, ent)
		writeJSON(w, 201, map[string]any{"id": ent.ID, "identifier": ent.Identifier, "name": ent.Name, "rank": nil})
	})
	mux.HandleFunc("PATCH /v1/apps/app_1/catalog/products/{pid}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			EntitlementIDs []string `json:"entitlementIds"`
		}
		read(r, &body)
		pid := r.PathValue("pid")
		m.mu.Lock()
		defer m.mu.Unlock()
		m.writes = append(m.writes, "MAP "+pid)
		for i := range m.products {
			if m.products[i].ID == pid {
				m.products[i].EntitlementIDs = body.EntitlementIDs
				writeJSON(w, 200, m.products[i])
				return
			}
		}
		writeJSON(w, 404, map[string]any{"statusCode": 404, "message": "product not found", "error": "Not Found"})
	})
	mux.HandleFunc("GET /v1/apps/app_1/assn", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"url": "https://api.example/webhooks/apple/tok", "steps": []string{"step one", "step two"}})
	})
	mux.HandleFunc("POST /v1/apps/app_1/notifications:test", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.writes = append(m.writes, "NOTIFTEST")
		m.mu.Unlock()
		writeJSON(w, 200, map[string]any{"store": "app-store", "manual": false, "environment": "Sandbox", "token": "probe_1"})
	})
	mux.HandleFunc("GET /v1/apps/app_1/webhooks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"rows": []any{}, "nextCursor": nil})
	})
	mux.HandleFunc("GET /v1/apps/app_1/placements", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := m.placements
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /v1/apps/app_1/placements", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			EventName string `json:"eventName"`
		}
		read(r, &body)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.writes = append(m.writes, "PLACEMENT "+body.EventName)
		p := map[string]any{"id": "plc_1", "eventName": body.EventName, "campaignCount": 0}
		m.placements = append(m.placements, p)
		writeJSON(w, 201, p)
	})
	mux.HandleFunc("GET /v1/apps/app_1/paywalls", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := m.paywalls
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /v1/apps/app_1/paywalls/from-template", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TemplateID string `json:"templateId"`
		}
		read(r, &body)
		if body.TemplateID == "" {
			m.t.Error("from-template requires templateId")
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		m.writes = append(m.writes, "PAYWALL "+body.TemplateID)
		pw := map[string]any{"id": "pw_1", "identifier": "paywall_x", "name": "Bold",
			"activeVersion": map[string]any{"id": "ver_1", "version": 1, "status": "draft"}}
		m.paywalls = append(m.paywalls, pw)
		writeJSON(w, 201, map[string]any{"id": "pw_1", "identifier": "paywall_x", "name": "Bold"})
	})
	mux.HandleFunc("GET /v1/apps/app_1/paywalls/pw_1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"id": "pw_1", "versions": []map[string]any{
			{"id": "ver_1", "version": 1, "status": "draft"},
		}})
	})
	mux.HandleFunc("POST /v1/apps/app_1/paywalls/pw_1/publish", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			VersionID string `json:"versionId"`
		}
		read(r, &body)
		if body.VersionID == "" {
			// The real API's ValidationPipe refuses a publish with no versionId.
			m.t.Error("publish must carry versionId in the body")
			writeJSON(w, 400, map[string]any{"statusCode": 400, "message": []string{"versionId must be a string"}, "error": "Bad Request"})
			return
		}
		m.mu.Lock()
		m.writes = append(m.writes, "PUBLISH "+body.VersionID)
		m.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "activeVersionId": body.VersionID})
	})
	mux.HandleFunc("GET /v1/apps/app_1/campaigns", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := m.campaigns
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /v1/apps/app_1/campaigns", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PlacementID string `json:"placementId"`
			Variants    []struct {
				PaywallID  string `json:"paywallId"`
				Name       string `json:"name"`
				TrafficPct int    `json:"trafficPct"`
			} `json:"variants"`
		}
		read(r, &body)
		if body.PlacementID == "" || len(body.Variants) == 0 {
			m.t.Errorf("campaign create needs placementId and variants, got %+v", body)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		m.writes = append(m.writes, "CAMPAIGN "+body.PlacementID)
		c := map[string]any{"id": "cmp_1", "status": "active", "placement": "onboarding_finished",
			"variants": []map[string]any{{"id": "var_1", "name": "Default", "trafficPct": 100, "paywall": "paywall_x"}}}
		m.campaigns = append(m.campaigns, c)
		writeJSON(w, 201, c)
	})
	mux.HandleFunc("GET /v1/apps/app_1/analytics/events", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"rows": []map[string]any{
			{"id": "ev_1", "ts": "2026-08-26T10:00:00Z", "event": "paywall_impression", "placement": "onboarding_finished"},
		}, "nextCursor": nil})
	})
	mux.HandleFunc("GET /v1/apps/app_1/transactions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") == "" {
			m.t.Error("transactions must be paged with limit, not pageSize")
		}
		writeJSON(w, 200, map[string]any{"rows": []any{}, "nextCursor": nil})
	})

	// Error-shape fixtures.
	mux.HandleFunc("GET /v1/apps/app_locked", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 423, map[string]any{"error": map[string]any{"code": "workspace_pending_deletion", "message": "workspace is scheduled for deletion"}})
	})

	auth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			if r.Header.Get("Authorization") != "Bearer csk_st_testtoken" {
				writeJSON(w, 401, map[string]any{"statusCode": 401, "message": "invalid, revoked or expired setup token", "error": "Unauthorized"})
				return
			}
			if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "cashsdk-cli/") {
				m.t.Errorf("User-Agent %q must identify the CLI", ua)
			}
		}
		mux.ServeHTTP(w, r)
	})
	return httptest.NewServer(auth)
}

func (m *mockAPI) writeCount(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, w := range m.writes {
		if strings.HasPrefix(w, prefix) {
			n++
		}
	}
	return n
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestSetupRunBlockedOnCredentials(t *testing.T) {
	isolate(t)
	m := &mockAPI{t: t, credPassed: false}
	srv := m.server()
	defer srv.Close()

	code, out := runCLI(t, "setup", "run", "--json",
		"--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_1")
	if code != 5 {
		t.Fatalf("expected exit 5 (blocked on human), got %d\n%s", code, out)
	}
	if len(m.writes) != 0 {
		t.Errorf("no writes may happen while blocked on credentials, saw %v", m.writes)
	}
	if !strings.Contains(out, "blocked_on") || !strings.Contains(out, "upload your App Store Connect API key") {
		t.Errorf("JSON report should carry the blocked_on ask, got: %s", out)
	}
}

func TestSetupRunHappyThenIdempotent(t *testing.T) {
	isolate(t)
	m := &mockAPI{t: t, credPassed: true, assnDone: true}
	srv := m.server()
	defer srv.Close()

	base := []string{"--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_1", "--json"}

	code, out := runCLI(t, append([]string{"setup", "run", "--map-all", "pro"}, base...)...)
	if code != 0 {
		t.Fatalf("first run should exit 0, got %d\n%s", code, out)
	}
	for _, want := range []string{"SYNC", "ENT pro", "MAP prod_1", "MAP prod_2", "PLACEMENT onboarding_finished", "PAYWALL tpl_1", "PUBLISH ver_1", "CAMPAIGN plc_1"} {
		if m.writeCount(want) != 1 {
			t.Errorf("expected exactly one %q write, log: %v", want, m.writes)
		}
	}
	var report struct {
		Steps []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("stdout must be one JSON document: %v\n%s", err, out)
	}

	writesAfterFirst := len(m.writes)
	code, out = runCLI(t, append([]string{"setup", "run"}, base...)...)
	if code != 0 {
		t.Fatalf("second run should exit 0, got %d\n%s", code, out)
	}
	if len(m.writes) != writesAfterFirst {
		t.Errorf("second run must be a no-op, new writes: %v", m.writes[writesAfterFirst:])
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	for _, s := range report.Steps {
		if s.Status != "unchanged" {
			t.Errorf("second run: step %s should be unchanged, was %s", s.ID, s.Status)
		}
	}
}

func TestSetupRunDryRunWritesNothing(t *testing.T) {
	isolate(t)
	m := &mockAPI{t: t, credPassed: true, assnDone: true}
	srv := m.server()
	defer srv.Close()
	code, _ := runCLI(t, "setup", "run", "--dry-run", "--map-all", "pro", "--json",
		"--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_1")
	// Unmapped products remain (dry), so the run reports partial work: exit 6.
	if code != 6 {
		t.Fatalf("dry run with pending mapping should exit 6, got %d", code)
	}
	if len(m.writes) != 0 {
		t.Errorf("dry run must not write, saw %v", m.writes)
	}
}

func TestSetupRunNeverGuessesMapping(t *testing.T) {
	isolate(t)
	m := &mockAPI{t: t, credPassed: true, assnDone: true}
	srv := m.server()
	defer srv.Close()
	code, out := runCLI(t, "setup", "run", "--json",
		"--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_1")
	if code != 6 {
		t.Fatalf("run without --map should exit 6 (mapping needs judgment), got %d\n%s", code, out)
	}
	if m.writeCount("ENT") != 0 || m.writeCount("MAP") != 0 {
		t.Errorf("mapping must never be guessed, saw %v", m.writes)
	}
}

func TestSetupMapAllSkipsMappingExemptProducts(t *testing.T) {
	isolate(t)
	m := &mockAPI{t: t, credPassed: true, assnDone: true, synced: true,
		products: []mockProduct{
			{ID: "prod_access", Identifier: "pro.monthly", Type: "auto_renewable", Status: "active"},
			{ID: "prod_coins", Identifier: "coins", Type: "consumable", Status: "active"},
			{ID: "prod_tip", Identifier: "tip", Type: "non_consumable", Status: "active", SoldWithoutAccess: true},
			{ID: "prod_old", Identifier: "old", Type: "auto_renewable", Status: "inactive"},
		},
	}
	srv := m.server()
	defer srv.Close()
	code, out := runCLI(t, "setup", "run", "--map-all", "pro", "--json",
		"--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_1")
	if code != 0 {
		t.Fatalf("setup failed: %d\n%s", code, out)
	}
	if m.writeCount("MAP prod_access") != 1 || m.writeCount("MAP") != 1 {
		t.Fatalf("only access products should be mapped: %v", m.writes)
	}
}

func TestSetupMappingCoversPlayBasePlansAndPreservesLinks(t *testing.T) {
	for _, specific := range []bool{false, true} {
		t.Run(fmt.Sprintf("specific=%v", specific), func(t *testing.T) {
			isolate(t)
			monthly, annual := "monthly", "annual"
			m := &mockAPI{t: t, credPassed: true, assnDone: true, synced: true,
				products: []mockProduct{
					{ID: "prod_monthly", Identifier: "pro", BasePlanID: &monthly, Type: "auto_renewable", Store: "play-store", Status: "active", EntitlementIDs: []string{"ent_existing"}},
					{ID: "prod_annual", Identifier: "pro", BasePlanID: &annual, Type: "auto_renewable", Store: "play-store", Status: "active"},
				},
				entitlements: []mockEntitlement{{ID: "ent_existing", Identifier: "existing", Name: "Existing"}},
			}
			srv := m.server()
			defer srv.Close()
			base := []string{"--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_1", "--json"}
			args := []string{"setup", "run", "--map", "pro=access"}
			if specific {
				args = append(args, "--map", "pro:annual=premium")
			}
			code, out := runCLI(t, append(args, base...)...)
			if code != 0 {
				t.Fatalf("mapping failed %d: %s", code, out)
			}
			if m.writeCount("MAP prod_monthly") != 1 || m.writeCount("MAP prod_annual") != 1 {
				t.Fatalf("both base plans must map: %v", m.writes)
			}
			if len(m.products[0].EntitlementIDs) != 2 || m.products[0].EntitlementIDs[0] != "ent_existing" {
				t.Fatalf("existing access lost: %v", m.products[0])
			}
			if len(m.products[1].EntitlementIDs) != 1 {
				t.Fatalf("annual must map: %v", m.products[1])
			}
			if specific && m.products[0].EntitlementIDs[1] == m.products[1].EntitlementIDs[0] {
				t.Fatal("specific base-plan tier must override the broad mapping")
			}
			writes := m.writeCount("MAP")
			code, out = runCLI(t, append(args, base...)...)
			if code != 0 || m.writeCount("MAP") != writes {
				t.Fatalf("re-run must not rewrite links: %d %s %v", code, out, m.writes)
			}
			_, listing := runCLI(t, "catalog", "--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_1")
			if !strings.Contains(listing, "pro:monthly") || !strings.Contains(listing, "pro:annual") {
				t.Fatalf("base plans must be distinguishable: %s", listing)
			}
		})
	}
}

func TestChecklistRequireComplete(t *testing.T) {
	isolate(t)
	m := &mockAPI{t: t, credPassed: false}
	srv := m.server()
	defer srv.Close()
	code, _ := runCLI(t, "checklist", "--require-complete", "--json",
		"--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_1")
	if code != 1 {
		t.Fatalf("incomplete checklist with --require-complete should exit 1, got %d", code)
	}

	m2 := &mockAPI{t: t, credPassed: true, assnDone: true, synced: true, deviceDone: true,
		products:     []mockProduct{{ID: "prod_1", Identifier: "pro.monthly", EntitlementIDs: []string{"ent_1"}}},
		entitlements: []mockEntitlement{{ID: "ent_1", Identifier: "pro", Name: "Pro"}},
		campaigns:    []map[string]any{{"status": "active", "placement": "onboarding_finished"}},
	}
	srv2 := m2.server()
	defer srv2.Close()
	code, _ = runCLI(t, "checklist", "--require-complete", "--json",
		"--api-url", srv2.URL, "--token", "csk_st_testtoken", "--app", "app_1")
	if code != 0 {
		t.Fatalf("complete checklist should exit 0, got %d", code)
	}
}

func TestAuthErrorsMapToExit3(t *testing.T) {
	isolate(t)
	m := &mockAPI{t: t}
	srv := m.server()
	defer srv.Close()
	code, _ := runCLI(t, "catalog", "--json",
		"--api-url", srv.URL, "--token", "csk_st_WRONG", "--app", "app_1")
	if code != 3 {
		t.Fatalf("401 should exit 3, got %d", code)
	}
}

func TestSecretKeyRefusedUpFront(t *testing.T) {
	isolate(t)
	code, _ := runCLI(t, "catalog", "--json", "--token", "csk_sk_abc", "--app", "app_1")
	if code != 3 {
		t.Fatalf("a csk_sk_ must be refused before any network call, got %d", code)
	}
}

func TestNestedErrorEnvelope(t *testing.T) {
	isolate(t)
	m := &mockAPI{t: t}
	srv := m.server()
	defer srv.Close()
	// Reach the 423 fixture through a raw client call via the apps command.
	code, _ := runCLI(t, "apps", "--json",
		"--api-url", srv.URL, "--token", "csk_st_testtoken", "--app", "app_locked")
	if code != 4 {
		t.Fatalf("423 should map to a remote error exit 4, got %d", code)
	}
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	isolate(t)
	code, _ := runCLI(t, "catalog", "--nope")
	if code != 2 {
		t.Fatalf("unknown flag should exit 2, got %d", code)
	}
}

func TestAuthSetRoundTrip(t *testing.T) {
	isolate(t)
	code, _ := runCLI(t, "auth", "set", "--app", "app_9", "--token", "csk_mcp_abcdefghijklmnop", "--json")
	if code != 0 {
		t.Fatalf("auth set failed: %d", code)
	}
	path := os.Getenv("XDG_CONFIG_HOME") + "/cashsdk/config.json"
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials file must be 0600, is %o", info.Mode().Perm())
	}
	code, out := runCLI(t, "auth", "status", "--json")
	if code != 0 || !strings.Contains(out, "app_9") {
		t.Fatalf("auth status should show the saved app: %s", out)
	}
	if strings.Contains(out, "csk_mcp_abcdefghijklmnop") {
		t.Error("auth status must never print the full token")
	}
}

func TestAuthSetRefusesSecretKey(t *testing.T) {
	isolate(t)
	code, _ := runCLI(t, "auth", "set", "--app", "app_9", "--token", "csk_sk_abc")
	if code != 2 {
		t.Fatalf("auth set must refuse a secret key with exit 2, got %d", code)
	}
}

func TestMultiFlagParsing(t *testing.T) {
	p, err := parseArgs([]string{"--map", "a=pro", "--map", "b=pro,c=plus", "--dry-run"},
		[]FlagSpec{{Name: "map", Value: true, Multi: true}, {Name: "dry-run"}})
	if err != nil {
		t.Fatal(err)
	}
	got := p.Multi("map")
	want := []string{"a=pro", "b=pro", "c=plus"}
	if len(got) != len(want) {
		t.Fatalf("Multi = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Multi = %v, want %v", got, want)
		}
	}
	if !p.Bool("dry-run") {
		t.Error("bool flag lost")
	}
}

func TestHelpListsEveryVisibleCommand(t *testing.T) {
	isolate(t)
	code, out := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("help exited %d", code)
	}
	for _, c := range commands {
		if c.hidden {
			continue
		}
		listed := false
		for _, sec := range helpSections {
			for _, n := range sec.names {
				if n == c.name {
					listed = true
				}
			}
		}
		if !listed {
			t.Errorf("command %q is not in any help section", c.name)
		}
	}
	if strings.Contains(out, "—") {
		t.Error("help output contains an em dash")
	}
}
