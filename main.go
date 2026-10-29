package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
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
	setup, qrLogin, httpMode := false, false, false
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Fprintln(os.Stderr, "Usage: telegram-mcp [setup [qr] | http]\nWithout arguments, serves MCP over stdio; http serves it at http://TELEGRAM_MCP_HTTP_ADDR/mcp.\nSettings: TELEGRAM_API_ID, TELEGRAM_API_HASH, TELEGRAM_PHONE (setup only),\nTELEGRAM_MCP_HTTP_TOKEN and TELEGRAM_MCP_HTTP_ADDR (http only).\nSee .env.example for optional paths, download limit and timeout.")
			return nil
		case "http":
			if len(args) > 1 {
				return fmt.Errorf("unexpected arguments")
			}
			httpMode = true
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
	opts := fx.Options(providers(!setup), fx.Populate(&cfg, &client, &dispatcher))
	if !setup {
		opts = fx.Options(opts, fx.Populate(&server))
	}
	if err := fx.New(opts).Err(); err != nil {
		return err
	}
	if setup && !qrLogin && cfg.Phone == "" {
		return fmt.Errorf("TELEGRAM_PHONE is required for setup")
	}
	if httpMode {
		if err := cfg.CheckHTTP(); err != nil {
			return err
		}
	}
	if setup {
		if err := os.MkdirAll(filepath.Dir(cfg.SessionPath), 0700); err != nil {
			return err
		}
	} else if _, err := os.Stat(cfg.SessionPath); err != nil {
		return fmt.Errorf("session unavailable; run telegram-mcp setup first: %w", err)
	}
	if err := os.Chmod(cfg.SessionPath, 0600); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
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
		if httpMode {
			ln, err := net.Listen("tcp", cfg.HTTPAddr)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "serving MCP on http://%s/mcp\n", ln.Addr())
			return serveHTTP(ctx, ln, cfg.HTTPToken, server)
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	})
}

// serveHTTP serves until ctx ends or the listener fails. Either way it cancels in-flight tool calls
// and drains them while the Telegram client is still running, so download cleanup completes.
func serveHTTP(parent context.Context, ln net.Listener, token string, server *mcp.Server) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	handler, waitCalls := mcpadapter.HTTPHandler(ctx, server, token)
	srv := &http.Server{
		Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	var serveErr error
	select {
	case serveErr = <-errc:
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer stop()
	err := srv.Shutdown(shutdownCtx)
	waitCalls()
	if serveErr == nil {
		serveErr = <-errc // Shutdown closed the listener, so Serve has returned.
	}
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	if err != nil {
		return err
	}
	return parent.Err()
}

func providers(serving bool) fx.Option {
	return fx.Options(fx.NopLogger, fx.Provide(
		config.Load,
		tg.NewUpdateDispatcher,
		func(cfg config.Config, dispatcher tg.UpdateDispatcher) *telegram.Client {
			return telegram.NewClient(cfg.APIID, cfg.APIHash, clientOptions(cfg, dispatcher, serving))
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

// clientOptions retries short FLOOD_WAITs only while serving: setup must not repeat
// login requests, because Telegram may silently drop codes after repeated requests.
func clientOptions(cfg config.Config, dispatcher tg.UpdateDispatcher, serving bool) telegram.Options {
	opts := telegram.Options{
		SessionStorage: &atomicSessionStorage{FileStorage: session.FileStorage{Path: cfg.SessionPath}}, UpdateHandler: dispatcher,
	}
	if serving {
		opts.Middlewares = []telegram.Middleware{tgadapter.FloodWait()}
	}
	return opts
}

// atomicSessionStorage replaces the session file via rename, so a crash or full disk
// mid-write keeps the previous session instead of a truncated one.
type atomicSessionStorage struct {
	session.FileStorage
	mu sync.Mutex
}

func (s *atomicSessionStorage) StoreSession(_ context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.CreateTemp(filepath.Dir(s.Path), "."+filepath.Base(s.Path)+"-*") // created 0600
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }() // no-op after a successful rename
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), s.Path)
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
	value := string(b)
	if trim {
		value = strings.TrimSpace(value)
	}
	return value, nil
}
