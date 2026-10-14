package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRefusesRedirectWithoutLeakingToken(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	_, err := New(origin.URL, "csk_st_test").Get("/redirect", nil)
	if err == nil || !strings.Contains(err.Error(), "Temporary Redirect") {
		t.Fatalf("expected redirect to be refused as an API error, got %v", err)
	}
	if leaked {
		t.Fatal("authorization header reached the redirect target")
	}
}

func TestClientRejectsInsecureRemoteBaseURL(t *testing.T) {
	_, err := New("http://api.example.com", "csk_st_test").Get("/v1/apps", nil)
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("expected insecure remote URL to be rejected, got %v", err)
	}
}

func TestClientLimitsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxResponseBytes+1))
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "").Get("/large", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds 10 MiB") {
		t.Fatalf("expected oversized response to be rejected, got %v", err)
	}
}

func TestAPIErrorMessageStripsTerminalControls(t *testing.T) {
	err := decodeError(http.StatusBadRequest,
		[]byte(`{"message":"safe\u001b]52;c;clipboard\u0007\ntext\u202eevil"}`))
	if strings.ContainsAny(err.Message, "\x1b\x07\n") || strings.ContainsRune(err.Message, '\u202e') {
		t.Fatalf("terminal controls survived sanitization: %q", err.Message)
	}
	if !strings.Contains(err.Message, "safe") || !strings.Contains(err.Message, "text") {
		t.Fatalf("useful error text was lost: %q", err.Message)
	}
}
