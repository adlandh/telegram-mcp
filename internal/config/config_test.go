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
