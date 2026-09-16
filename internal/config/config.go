// Package config loads settings exclusively from environment variables.
package config

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	APIID                                    int
	APIHash, Phone, SessionPath, DownloadDir string
	MaxDownloadMB                            int64
	RequestTimeout                           time.Duration
}

func Load() (Config, error) {
	c := Config{APIHash: strings.TrimSpace(os.Getenv("TELEGRAM_API_HASH")), Phone: strings.TrimSpace(os.Getenv("TELEGRAM_PHONE"))}
	id, err := strconv.ParseInt(os.Getenv("TELEGRAM_API_ID"), 10, 32)
	if err != nil || id <= 0 {
		return c, fmt.Errorf("TELEGRAM_API_ID must be a positive 32-bit integer")
	}
	c.APIID = int(id)
	if _, err := hex.DecodeString(c.APIHash); err != nil || len(c.APIHash) != 32 {
		return c, fmt.Errorf("TELEGRAM_API_HASH must contain 32 hexadecimal characters")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return c, fmt.Errorf("home directory: %w", err)
	}
	c.SessionPath, err = expand(cmp.Or(os.Getenv("TELEGRAM_SESSION_PATH"), "~/.telegram-mcp/session.json"), home)
	if err != nil {
		return c, err
	}
	c.DownloadDir, err = expand(cmp.Or(os.Getenv("TELEGRAM_DOWNLOAD_DIR"), "~/.telegram-mcp/downloads"), home)
	if err != nil {
		return c, err
	}
	c.MaxDownloadMB, err = strconv.ParseInt(cmp.Or(os.Getenv("TELEGRAM_MAX_DOWNLOAD_MB"), "200"), 10, 64)
	if err != nil || c.MaxDownloadMB < 0 || c.MaxDownloadMB > (1<<63-1)/(1024*1024) {
		return c, fmt.Errorf("TELEGRAM_MAX_DOWNLOAD_MB must be a non-negative integer within int64 byte range")
	}
	c.RequestTimeout, err = time.ParseDuration(cmp.Or(os.Getenv("TELEGRAM_REQUEST_TIMEOUT"), "5m"))
	if err != nil || c.RequestTimeout <= 0 {
		return c, fmt.Errorf("TELEGRAM_REQUEST_TIMEOUT must be a positive duration, for example 5m")
	}
	return c, nil
}

func expand(path, home string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	return filepath.Abs(path)
}
