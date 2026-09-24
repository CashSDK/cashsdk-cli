// Package api is the HTTP client every command goes through: one place for
// auth, the User-Agent, timeouts, JSON decoding, and error mapping.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// Version is stamped by the build (main sets it from its ldflags value).
var Version = "dev"

const maxResponseBytes = 10 << 20

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	initErr error
}

func New(baseURL, token string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	initErr := ValidateEndpointURL(baseURL)
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		// API calls never need redirects. Refusing them also ensures a bearer
		// token cannot be forwarded to a redirect target.
		HTTP: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		initErr: initErr,
	}
}

// ValidateEndpointURL keeps credentials on TLS, with a narrow HTTP exception for
// local development and httptest. Query strings, fragments, and embedded
// credentials have no valid meaning in an API origin.
func ValidateEndpointURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid endpoint URL %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("endpoint URL must not contain credentials, a query, or a fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("endpoint URL must use HTTPS (HTTP is allowed only for localhost)")
}

// Error is a non-2xx API response, mapped so commands can exit precisely:
// 401/403 exit 3, everything else remote exit 4.
type Error struct {
	Status      int
	Message     string
	Remediation string
	Body        json.RawMessage
}

func (e *Error) Error() string { return fmt.Sprintf("API %d: %s", e.Status, e.Message) }

// ExitCode maps an API failure onto the CLI exit-code contract.
func (e *Error) ExitCode() int {
	if e.Status == 401 || e.Status == 403 {
		return 3
	}
	return 4
}

// Do performs a request. `out`, when non-nil, receives the decoded response;
// the raw body is returned either way so --json can reprint it verbatim.
func (c *Client) Do(method, path string, reqBody any, out any) (json.RawMessage, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}
	var buf *bytes.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}
		buf = bytes.NewReader(raw)
	} else {
		buf = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cashsdk-cli/"+Version)
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.BaseURL, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("API response exceeds %d MiB limit", maxResponseBytes>>20)
	}
	raw := json.RawMessage(body)

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return raw, decodeError(res.StatusCode, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, fmt.Errorf("unexpected response shape from %s %s: %w", method, path, err)
		}
	}
	return raw, nil
}

func (c *Client) Get(path string, out any) (json.RawMessage, error) {
	return c.Do(http.MethodGet, path, nil, out)
}

func (c *Client) Post(path string, body, out any) (json.RawMessage, error) {
	return c.Do(http.MethodPost, path, body, out)
}

func (c *Client) Patch(path string, body, out any) (json.RawMessage, error) {
	return c.Do(http.MethodPatch, path, body, out)
}

// decodeError pulls the useful parts out of an error body. The API answers in
// two shapes: Nest's {statusCode, message, error} where message is a string or
// an array of validation strings, and the guard/interceptor envelope
// {error: {code, message}} (423 workspace lifecycle, idempotency conflicts).
func decodeError(status int, raw json.RawMessage) *Error {
	e := &Error{Status: status, Body: raw, Message: http.StatusText(status)}
	var parsed struct {
		Message    any             `json:"message"`
		ErrorField json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		switch m := parsed.Message.(type) {
		case string:
			if m != "" {
				e.Message = m
			}
		case []any:
			parts := make([]string, 0, len(m))
			for _, p := range m {
				parts = append(parts, fmt.Sprint(p))
			}
			if len(parts) > 0 {
				e.Message = strings.Join(parts, "; ")
			}
		}
		if e.Message == http.StatusText(status) && len(parsed.ErrorField) > 0 {
			var nested struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			var label string
			if json.Unmarshal(parsed.ErrorField, &nested) == nil && nested.Message != "" {
				e.Message = nested.Message
				if nested.Code != "" {
					e.Message += " (" + nested.Code + ")"
				}
			} else if json.Unmarshal(parsed.ErrorField, &label) == nil && label != "" {
				e.Message = label
			}
		}
	} else if len(raw) > 0 && len(raw) < 300 {
		e.Message = strings.TrimSpace(string(raw))
	}
	if status == 401 {
		e.Remediation = "run `cashsdk auth set` with a setup token (dashboard: app page, Generate prompt) or an MCP token (Settings, MCP)"
	}
	e.Message = sanitizeMessage(e.Message)
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

// sanitizeMessage prevents an API-controlled error from moving the cursor,
// changing the terminal title, writing the clipboard, or hiding text.
func sanitizeMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
