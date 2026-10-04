package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TELEGRAM_API_ID", "123456")
	t.Setenv("TELEGRAM_API_HASH", strings.Repeat("a", 32))
	for _, key := range []string{"TELEGRAM_SESSION_PATH", "TELEGRAM_DOWNLOAD_DIR", "TELEGRAM_MAX_DOWNLOAD_MB", "TELEGRAM_REQUEST_TIMEOUT"} {
		t.Setenv(key, "")
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxDownloadMB != 200 || c.RequestTimeout != 5*time.Minute || !filepath.IsAbs(c.SessionPath) {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	for key, value := range map[string]string{"TELEGRAM_API_ID": "-1", "TELEGRAM_API_HASH": "bad", "TELEGRAM_MAX_DOWNLOAD_MB": "8796093022208", "TELEGRAM_REQUEST_TIMEOUT": "0s"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected %s error, got %v", key, err)
			}
		})
	}
	t.Setenv("TELEGRAM_MAX_DOWNLOAD_MB", "0")
	t.Setenv("TELEGRAM_DOWNLOAD_DIR", "~/media")
	c, err = Load()
	if err != nil || c.MaxDownloadMB != 0 || filepath.Base(c.DownloadDir) != "media" {
		t.Fatalf("overrides: %+v, %v", c, err)
	}
}

func TestCheckHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TELEGRAM_API_ID", "123456")
	t.Setenv("TELEGRAM_API_HASH", strings.Repeat("a", 32))
	t.Setenv("TELEGRAM_MCP_HTTP_ADDR", "")
	t.Setenv("TELEGRAM_MCP_HTTP_TOKEN", "")
	c, err := Load()
	if err != nil || c.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("default address: %+v, %v", c, err)
	}
	// Invalid HTTP settings must not break stdio startup.
	t.Setenv("TELEGRAM_MCP_HTTP_ADDR", "bad")
	t.Setenv("TELEGRAM_MCP_HTTP_TOKEN", "short")
	if _, err := Load(); err != nil {
		t.Fatalf("stdio load failed on HTTP settings: %v", err)
	}
	token := strings.Repeat("s", 32)
	for _, tt := range []struct {
		addr, token, want string
	}{
		{"127.0.0.1:8080", "", "TELEGRAM_MCP_HTTP_TOKEN"},
		{"127.0.0.1:8080", token[:31], "TELEGRAM_MCP_HTTP_TOKEN"},
		{"8080", token, "TELEGRAM_MCP_HTTP_ADDR"},
		{":8080", token, "TELEGRAM_MCP_HTTP_ADDR"},
		{"0.0.0.0:8080", token, "TELEGRAM_MCP_HTTP_ADDR"},
		{"192.168.1.10:8080", token, "TELEGRAM_MCP_HTTP_ADDR"},
		{"example.com:8080", token, "TELEGRAM_MCP_HTTP_ADDR"},
		{"127.0.0.1:8080", token, ""},
		{"[::1]:8080", token, ""},
		{"localhost:8080", token, ""},
	} {
		err := Config{HTTPAddr: tt.addr, HTTPToken: tt.token}.CheckHTTP()
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: got %v, want %q", tt.addr, err, tt.want)
		}
		if err != nil && tt.token != "" && strings.Contains(err.Error(), tt.token) {
			t.Errorf("%s: error leaks token", tt.addr)
		}
	}
}
