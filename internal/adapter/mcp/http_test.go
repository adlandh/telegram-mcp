package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestHTTPHandler(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	calls := 0
	srv := httptest.NewServer(HTTPHandler(New(executorFunc(func(_ context.Context, name string, _ app.Arguments) (string, error) {
		calls++
		return name, nil
	})), token))
	t.Cleanup(srv.Close)
	endpoint := srv.URL + "/mcp"
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	post := func(t *testing.T, edit func(*http.Request)) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(initialize))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		edit(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for name, tt := range map[string]struct {
		edit func(*http.Request)
		want int
	}{
		"no header":   {func(*http.Request) {}, http.StatusUnauthorized},
		"basic":       {func(r *http.Request) { r.SetBasicAuth("u", token) }, http.StatusUnauthorized},
		"wrong token": {func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.ToUpper(token)) }, http.StatusUnauthorized},
		"cross site": {func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}, http.StatusForbidden},
		"proxied public host": {func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Host = "tg.example.com"
		}, http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			if got := post(t, tt.edit); got != tt.want {
				t.Fatalf("status %d, want %d", got, tt.want)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("rejected requests reached the executor %d times", calls)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearer{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 14 {
		t.Fatalf("tools: %v, %v", tools, err)
	}
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_folders", Arguments: map[string]any{}})
	if err != nil || r.IsError || r.Content[0].(*mcp.TextContent).Text != "list_folders" || calls != 1 {
		t.Fatalf("call: %+v, %v, calls=%d", r, err, calls)
	}
}
