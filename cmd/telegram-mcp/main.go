package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	mcpadapter "github.com/adlandh/telegram-mcp/internal/adapter/mcp"
	tgadapter "github.com/adlandh/telegram-mcp/internal/adapter/telegram"
	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/adlandh/telegram-mcp/internal/config"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	setup := false
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Fprintln(os.Stderr, "Usage: telegram-mcp [setup]\nSettings: TELEGRAM_API_ID, TELEGRAM_API_HASH, TELEGRAM_PHONE (setup only).\nSee .env.example for optional paths, download limit and timeout.")
			return nil
		case "setup":
			setup = true
		default:
			return fmt.Errorf("unknown command %q; use --help", args[0])
		}
		if len(args) > 1 {
			return fmt.Errorf("unexpected arguments")
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if setup && cfg.Phone == "" {
		return fmt.Errorf("TELEGRAM_PHONE is required for setup")
	}
	if setup {
		if err := os.MkdirAll(filepath.Dir(cfg.SessionPath), 0700); err != nil {
			return err
		}
	} else if _, err := os.Stat(cfg.SessionPath); err != nil {
		return fmt.Errorf("session unavailable; run telegram-mcp setup first: %w", err)
	}
	if _, err := os.Stat(cfg.SessionPath); err == nil {
		if err := os.Chmod(cfg.SessionPath, 0600); err != nil {
			return err
		}
	}
	client := telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{SessionStorage: &session.FileStorage{Path: cfg.SessionPath}, NoUpdates: true})
	return client.Run(ctx, func(ctx context.Context) error {
		if setup {
			flow := auth.NewFlow(terminalAuth{UserAuthenticator: auth.CodeOnly(cfg.Phone, auth.CodeAuthenticatorFunc(func(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
				return prompt(ctx, "Telegram login code: ", true)
			}))}, auth.SendCodeOptions{})
			if err := client.Auth().IfNecessary(ctx, flow); err != nil {
				return fmt.Errorf("login: %w", err)
			}
			fmt.Fprintln(os.Stderr, "Session saved. Start telegram-mcp without arguments to serve MCP.")
			return nil
		}
		statusCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
		status, err := client.Auth().Status(statusCtx)
		cancel()
		if err != nil {
			return err
		}
		if !status.Authorized {
			return fmt.Errorf("session is not authorized; run telegram-mcp setup")
		}
		service := app.New(tgadapter.New(client, cfg.DownloadDir), cfg.MaxDownloadMB, cfg.RequestTimeout)
		return mcpadapter.New(service).Run(ctx, &mcp.StdioTransport{})
	})
}

type terminalAuth struct{ auth.UserAuthenticator }

func (terminalAuth) Password(ctx context.Context) (string, error) {
	return prompt(ctx, "Telegram 2FA password: ", false)
}

func prompt(ctx context.Context, label string, trim bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("setup requires an interactive terminal")
	}
	fmt.Fprint(os.Stderr, label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	defer clear(b)
	value := string(b)
	if trim {
		value = strings.TrimSpace(value)
	}
	return value, nil
}
