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
	"time"

	mcpadapter "github.com/adlandh/telegram-mcp/internal/adapter/mcp"
	tgadapter "github.com/adlandh/telegram-mcp/internal/adapter/telegram"
	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/adlandh/telegram-mcp/internal/config"
	"github.com/adlandh/telegram-mcp/internal/port"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
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
	setup, qrLogin := false, false
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Fprintln(os.Stderr, "Usage: telegram-mcp [setup [qr]]\nSettings: TELEGRAM_API_ID, TELEGRAM_API_HASH, TELEGRAM_PHONE (setup only).\nSee .env.example for optional paths, download limit and timeout.")
			return nil
		case "setup":
			setup = true
			if len(args) > 1 {
				if args[1] != "qr" {
					return fmt.Errorf("unknown argument %q; use setup or setup qr", args[1])
				}
				qrLogin = true
			}
		default:
			return fmt.Errorf("unknown command %q; use --help", args[0])
		}
		if len(args) > 2 {
			return fmt.Errorf("unexpected arguments")
		}
	}
	var cfg config.Config
	var client *telegram.Client
	var dispatcher tg.UpdateDispatcher
	var server *mcp.Server
	opts := fx.Options(providers(), fx.Populate(&cfg, &client, &dispatcher))
	if !setup {
		opts = fx.Options(opts, fx.Populate(&server))
	}
	if err := fx.New(opts).Err(); err != nil {
		return err
	}
	if setup && !qrLogin && cfg.Phone == "" {
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
	return client.Run(ctx, func(ctx context.Context) error {
		if setup {
			if qrLogin {
				if err := setupQR(ctx, client.QR(), client.Auth(), qrlogin.OnLoginToken(dispatcher), os.Stderr, prompt); err != nil {
					return fmt.Errorf("qr login: %w", err)
				}
			} else if err := setupLogin(ctx, client.Auth(), cfg.Phone, prompt, os.Stderr, time.Now); err != nil {
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
		return server.Run(ctx, &mcp.StdioTransport{})
	})
}

func providers() fx.Option {
	return fx.Options(fx.NopLogger, fx.Provide(
		config.Load,
		tg.NewUpdateDispatcher,
		func(cfg config.Config, dispatcher tg.UpdateDispatcher) *telegram.Client {
			return telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
				SessionStorage: &session.FileStorage{Path: cfg.SessionPath}, UpdateHandler: dispatcher,
			})
		},
		func(client *telegram.Client, cfg config.Config) port.Telegram {
			return tgadapter.New(client, cfg.DownloadDir)
		},
		func(client port.Telegram, cfg config.Config) mcpadapter.Executor {
			return app.New(client, cfg.MaxDownloadMB, cfg.RequestTimeout)
		},
		mcpadapter.New,
	))
}

var errInteractiveTerminal = errors.New("setup requires an interactive terminal")

func prompt(ctx context.Context, label string, trim bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errInteractiveTerminal
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
