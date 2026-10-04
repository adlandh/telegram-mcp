package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adlandh/telegram-mcp/internal/config"
	"github.com/gotd/td/telegram"
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
	if err := fx.ValidateApp(providers(), fx.Populate(&client, &server)); err != nil {
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
			opts := fx.Options(providers(), fx.Populate(&cfg, &client),
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
			if _, err := os.Stat(sessionPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("graph created a session: %v", err)
			}
		})
	}
}

func TestPrompt(t *testing.T) {
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = previous; _ = stdin.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prompt(ctx, "Code: ", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if _, err := prompt(t.Context(), "Code: ", true); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("got %v, want terminal error", err)
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
