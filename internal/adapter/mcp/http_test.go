package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	handler, _ := HTTPHandler(t.Context(), New(executorFunc(func(_ context.Context, name string, _ app.Arguments) (string, error) {
		calls++
		return name, nil
	})), token)
	srv := httptest.NewServer(handler)
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

func TestHTTPHandlerShutdownCancelsAndDrainsCalls(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	life, stop := context.WithCancel(t.Context())
	started := make(chan struct{})
	var cancelled, cleaned atomic.Bool
	handler, wait := HTTPHandler(life, New(executorFunc(func(ctx context.Context, _ string, _ app.Arguments) (string, error) {
		close(started)
		select {
		case <-ctx.Done():
			cancelled.Store(true)
		case <-time.After(5 * time.Second):
		}
		time.Sleep(50 * time.Millisecond) // simulate removing an incomplete download
		cleaned.Store(true)
		return "", ctx.Err()
	})), token)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	go func() {
		_, _ = cs.CallTool(context.WithoutCancel(t.Context()), &mcp.CallToolParams{Name: "list_folders", Arguments: map[string]any{}})
	}()
	<-started
	stop()
	wait()
	if !cancelled.Load() || !cleaned.Load() {
		t.Fatalf("cancelled=%v cleaned=%v: shutdown must cancel and drain in-flight calls", cancelled.Load(), cleaned.Load())
	}
}

func TestHTTPHandlerRejectsCallsAfterWait(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	life, stop := context.WithCancel(t.Context())
	var running, finished atomic.Int32
	handler, wait := HTTPHandler(life, New(executorFunc(func(ctx context.Context, name string, _ app.Arguments) (string, error) {
		running.Add(1)
		defer finished.Add(1)
		<-ctx.Done()
		return "", ctx.Err()
	})), token)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	call := func() (*mcp.CallToolResult, error) {
		return cs.CallTool(context.WithoutCancel(t.Context()), &mcp.CallToolParams{Name: "list_folders", Arguments: map[string]any{}})
	}
	// Requests racing with shutdown must either be drained by wait or rejected, never both missed.
	for range 20 {
		go func() { _, _ = call() }()
	}
	stop()
	wait()
	if running.Load() != finished.Load() {
		t.Fatalf("wait returned with %d of %d accepted calls still running", running.Load()-finished.Load(), running.Load())
	}
	before := running.Load()
	if r, err := call(); err == nil && !r.IsError {
		t.Fatal("call after wait succeeded")
	}
	if running.Load() != before {
		t.Fatal("call after wait reached the executor")
	}
}
