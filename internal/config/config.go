// Package config loads settings exclusively from environment variables.
package config

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/adlandh/telegram-mcp/internal/domain"
)

type Config struct {
	APIHash        string
	Phone          string
	SessionPath    string
	DownloadDir    string
	HTTPAddr       string
	HTTPToken      string
	APIID          int
	MaxDownloadMB  int64
	RequestTimeout time.Duration
}

func Load() (Config, error) {
	c := Config{APIHash: strings.TrimSpace(os.Getenv("TELEGRAM_API_HASH")), Phone: strings.TrimSpace(os.Getenv("TELEGRAM_PHONE"))}

	for _, load := range []func() error{c.loadAPI, c.loadPaths, c.loadLimits} {
		if err := load(); err != nil {
			return c, err
		}
	}

	c.HTTPAddr = cmp.Or(strings.TrimSpace(os.Getenv("TELEGRAM_MCP_HTTP_ADDR")), "127.0.0.1:8080")
	c.HTTPToken = strings.TrimSpace(os.Getenv("TELEGRAM_MCP_HTTP_TOKEN"))

	return c, nil
}

func (c *Config) loadAPI() error {
	id, err := strconv.ParseInt(os.Getenv("TELEGRAM_API_ID"), 10, 32)
	if err != nil || id <= 0 {
		return fmt.Errorf("TELEGRAM_API_ID must be a positive 32-bit integer")
	}

	c.APIID = int(id)
	if _, err = hex.DecodeString(c.APIHash); err != nil || len(c.APIHash) != 32 {
		return fmt.Errorf("TELEGRAM_API_HASH must contain 32 hexadecimal characters")
	}

	return nil
}

func (c *Config) loadPaths() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home directory: %w", err)
	}

	c.SessionPath, err = expand(cmp.Or(os.Getenv("TELEGRAM_SESSION_PATH"), "~/.telegram-mcp/session.json"), home)
	if err != nil {
		return err
	}

	c.DownloadDir, err = expand(cmp.Or(os.Getenv("TELEGRAM_DOWNLOAD_DIR"), "~/.telegram-mcp/downloads"), home)

	return err
}

func (c *Config) loadLimits() error {
	var err error

	c.MaxDownloadMB, err = strconv.ParseInt(cmp.Or(os.Getenv("TELEGRAM_MAX_DOWNLOAD_MB"), "200"), 10, 64)
	if err != nil || c.MaxDownloadMB < 0 || c.MaxDownloadMB > domain.MaxDownloadMB {
		return fmt.Errorf("TELEGRAM_MAX_DOWNLOAD_MB must be a non-negative integer within int64 byte range")
	}

	c.RequestTimeout, err = time.ParseDuration(cmp.Or(os.Getenv("TELEGRAM_REQUEST_TIMEOUT"), "5m"))
	if err != nil || c.RequestTimeout <= 0 {
		return fmt.Errorf("TELEGRAM_REQUEST_TIMEOUT must be a positive duration, for example 5m")
	}

	return nil
}

// CheckHTTP validates settings used only by the http command.
func (c Config) CheckHTTP() error {
	if len(c.HTTPToken) < 32 {
		return fmt.Errorf("TELEGRAM_MCP_HTTP_TOKEN must contain at least 32 characters, for example from openssl rand -hex 32")
	}

	host, _, err := net.SplitHostPort(c.HTTPAddr)
	if err != nil {
		return fmt.Errorf("TELEGRAM_MCP_HTTP_ADDR must be host:port: %w", err)
	}

	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("TELEGRAM_MCP_HTTP_ADDR must use a loopback host such as 127.0.0.1; publish it through a TLS reverse proxy")
	}

	return nil
}

func expand(path, home string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}

	return abs, nil
}
