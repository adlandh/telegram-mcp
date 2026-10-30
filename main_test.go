package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpadapter "github.com/adlandh/telegram-mcp/internal/adapter/mcp"
	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/adlandh/telegram-mcp/internal/config"
	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
)

func TestRunStartup(t *testing.T) {
	for _, tt := range []struct {
		name          string
		args          []string
		invalidConfig bool
		want          string
		env           map[string]string
	}{
		{"help", []string{"--help"}, true, "", nil},
		{"short help", []string{"-h"}, true, "", nil},
		{"help command", []string{"help"}, true, "", nil},
		{"unknown", []string{"unknown"}, true, "unknown command", nil},
		{"extra", []string{"setup", "extra"}, true, "use setup or setup qr", nil},
		{"qr extra", []string{"setup", "qr", "extra"}, true, "unexpected arguments", nil},
		{"configuration", nil, true, "TELEGRAM_API_ID", nil},
		{"phone", []string{"setup"}, false, "TELEGRAM_PHONE is required", nil},
		{"session", nil, false, "session unavailable", nil},
		{"http extra", []string{"http", "extra"}, true, "unexpected arguments", nil},
		{"http token", []string{"http"}, false, "TELEGRAM_MCP_HTTP_TOKEN", nil},
		{"http public", []string{"http"}, false, "TELEGRAM_MCP_HTTP_ADDR", map[string]string{"TELEGRAM_MCP_HTTP_ADDR": ":8080", "TELEGRAM_MCP_HTTP_TOKEN": "0123456789abcdef0123456789abcdef"}},
		{"http session", []string{"http"}, false, "session unavailable", map[string]string{"TELEGRAM_MCP_HTTP_TOKEN": "0123456789abcdef0123456789abcdef"}},
		{"stdio ignores http", nil, false, "session unavailable", map[string]string{"TELEGRAM_MCP_HTTP_ADDR": ":8080", "TELEGRAM_MCP_HTTP_TOKEN": "short"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessionPath := testConfig(t)
			if tt.invalidConfig {
				t.Setenv("TELEGRAM_API_ID", "")
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			stdout, err := os.CreateTemp(t.TempDir(), "stdout")
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stdout
			os.Stdout = stdout
			t.Cleanup(func() { os.Stdout = previous; _ = stdout.Close() })
			err = run(t.Context(), tt.args)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
			info, err := stdout.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() != 0 {
				t.Fatal("startup wrote to stdout")
			}
			if _, err := os.Stat(sessionPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("startup created a session: %v", err)
			}
		})
	}
}

func TestValidateProviders(t *testing.T) {
	// Validate the production graph without running constructors or reading credentials.
	t.Setenv("TELEGRAM_API_ID", "")
	var client *telegram.Client
	var server *mcp.Server
	if err := fx.ValidateApp(providers(true), fx.Populate(&client, &server)); err != nil {
		t.Fatal(err)
	}
	if client != nil || server != nil {
		t.Fatal("validation constructed runtime dependencies")
	}
}

func TestProviders(t *testing.T) {
	for _, serving := range []bool{false, true} {
		name := "setup"
		if serving {
			name = "serve"
		}
		t.Run(name, func(t *testing.T) {
			sessionPath := testConfig(t)
			var cfg config.Config
			var client *telegram.Client
			var server *mcp.Server
			constructed := false
			opts := fx.Options(providers(serving), fx.Populate(&cfg, &client),
				fx.Decorate(func(s *mcp.Server) *mcp.Server { constructed = true; return s }))
			if serving {
				opts = fx.Options(opts, fx.Populate(&server))
			}
			if err := fx.New(opts).Err(); err != nil {
				t.Fatal(err)
			}
			if client == nil || cfg.SessionPath != sessionPath {
				t.Fatal("missing client or configuration")
			}
			if constructed != serving || (server != nil) != serving {
				t.Fatal("incorrect MCP construction for mode")
			}
			if n := len(clientOptions(cfg, tg.NewUpdateDispatcher(), serving).Middlewares); (n == 1) != serving {
				t.Fatalf("%d middlewares; FLOOD_WAIT retry must apply only while serving", n)
			}
			if _, err := os.Stat(sessionPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("graph created a session: %v", err)
			}
		})
	}
}

func testConfig(t *testing.T) string {
	t.Helper()
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	for key, value := range map[string]string{
		"TELEGRAM_API_ID":          "12345",
		"TELEGRAM_API_HASH":        strings.Repeat("a", 32),
		"TELEGRAM_PHONE":           "",
		"TELEGRAM_SESSION_PATH":    sessionPath,
		"TELEGRAM_DOWNLOAD_DIR":    t.TempDir(),
		"TELEGRAM_MAX_DOWNLOAD_MB": "200",
		"TELEGRAM_REQUEST_TIMEOUT": "5m",
		"TELEGRAM_MCP_HTTP_ADDR":   "",
		"TELEGRAM_MCP_HTTP_TOKEN":  "",
	} {
		t.Setenv(key, value)
	}
	return sessionPath
}

type executorFunc func(context.Context, string, app.Arguments) (string, error)

func (f executorFunc) Execute(ctx context.Context, name string, args app.Arguments) (string, error) {
	return f(ctx, name, args)
}

func (executorFunc) ViewThumbnail(context.Context, app.Arguments) (domain.Image, error) {
	return domain.Image{}, errors.New("unused")
}

var errListenerBroken = errors.New("listener broken")

// brokenListener reports errListenerBroken from Accept once broken is closed.
type brokenListener struct {
	net.Listener
	broken chan struct{}
}

func (l *brokenListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	select {
	case <-l.broken:
		if c != nil {
			_ = c.Close()
		}
		return nil, errListenerBroken
	default:
		return c, err
	}
}

type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func TestServeHTTPDrainsCallsOnEveryExit(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	for name, want := range map[string]error{"listener error": errListenerBroken, "signal": context.Canceled} {
		t.Run(name, func(t *testing.T) {
			inner, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ln := &brokenListener{Listener: inner, broken: make(chan struct{})}
			started := make(chan struct{})
			var cancelled, cleaned atomic.Bool
			server := mcpadapter.New(executorFunc(func(ctx context.Context, _ string, _ app.Arguments) (string, error) {
				close(started)
				select {
				case <-ctx.Done():
					cancelled.Store(true)
				case <-time.After(5 * time.Second):
				}
				time.Sleep(50 * time.Millisecond) // simulate removing an incomplete download
				cleaned.Store(true)
				return "", ctx.Err()
			}))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- serveHTTP(ctx, ln, token, server) }()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(),
				&mcp.StreamableClientTransport{Endpoint: "http://" + inner.Addr().String() + "/mcp", HTTPClient: &http.Client{Transport: bearer(token)}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cs.Close() })
			go func() {
				_, _ = cs.CallTool(context.WithoutCancel(t.Context()), &mcp.CallToolParams{Name: "list_folders", Arguments: map[string]any{}})
			}()
			<-started
			if want == errListenerBroken {
				close(ln.broken)
				_ = inner.Close()
			} else {
				cancel()
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("serveHTTP returned %v, want %v", err, want)
			}
			if !cancelled.Load() || !cleaned.Load() {
				t.Fatalf("cancelled=%v cleaned=%v: serveHTTP must cancel and drain in-flight calls", cancelled.Load(), cleaned.Load())
			}
		})
	}
}

func TestExecuteLifecycle(t *testing.T) {
	errJob := errors.New("job failed")
	connected := runner(func(ctx context.Context, f func(context.Context) error) error { return f(ctx) })
	for _, tt := range []struct {
		name   string
		cancel bool
		job    job
		want   error
	}{
		{"job error", false, func(context.Context) error { return errJob }, errJob},
		{"job done", false, func(context.Context) error { return nil }, nil},
		{"cancelled", true, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			j := tt.job
			if tt.cancel { // cancel mid-run, as a signal would; the job must see its context end
				j = func(jobCtx context.Context) error { cancel(); <-jobCtx.Done(); return jobCtx.Err() }
			}
			err := execute(ctx, fx.Supply(connected, j))
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}
