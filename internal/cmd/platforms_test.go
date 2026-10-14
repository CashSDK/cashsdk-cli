package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSnippetsEveryPlatform(t *testing.T) {
	isolate(t)
	for _, platform := range []string{"ios", "macos", "android", "web", "macos-direct", "chrome-extension", "stripe"} {
		t.Run(platform, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.URL.Path != "/v1/apps/app_fixture/snippets" || r.URL.Query().Get("platform") != platform {
					t.Errorf("unexpected snippets request: %s", r.URL)
				}
				fmt.Fprint(w, `{"sdk":{"required":false,"docs_url":"https://docs.example/sdk/stripe"},"snippets":[]}`)
			}))
			defer server.Close()
			code, output := runCLI(t, "snippets", "--platform", platform, "--api-url", server.URL, "--token", "csk_mcp_fixture", "--app", "app_fixture", "--json")
			if code != 0 || !called || !strings.Contains(output, "required") {
				t.Fatalf("platform %s: exit %d, called %t, output %s", platform, code, called, output)
			}
		})
	}
}

func TestCommerceSetupNeverRequestsAppleCredentials(t *testing.T) {
	isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps/app_fixture" {
			t.Errorf("unexpected store setup request: %s", r.URL)
		}
		fmt.Fprint(w, `{"id":"app_fixture","name":"Stripe","platforms":["stripe"],"environment":"Sandbox"}`)
	}))
	defer server.Close()
	code, output := runCLI(t, "setup", "run", "--api-url", server.URL, "--token", "csk_mcp_fixture", "--app", "app_fixture", "--json")
	if code != 2 {
		t.Fatalf("exit %d: %s", code, output)
	}
}
