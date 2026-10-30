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
	err := run(ctx, os.Args[1:])

	cancel()

	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

const usage = "Usage: telegram-mcp [setup [qr] | http]\nWithout arguments, serves MCP over stdio; http serves it at http://TELEGRAM_MCP_HTTP_ADDR/mcp.\nSettings: TELEGRAM_API_ID, TELEGRAM_API_HASH, TELEGRAM_PHONE (setup only),\nTELEGRAM_MCP_HTTP_TOKEN and TELEGRAM_MCP_HTTP_ADDR (http only).\nSee .env.example for optional paths, download limit and timeout."

// mode is the command selected on the command line.
type mode struct {
	help, setup, qr, http bool
}

func parseArgs(args []string) (mode, error) {
	var m mode

	if len(args) == 0 {
		return m, nil
	}

	switch args[0] {
	case "-h", "--help", "help":
		m.help = true
		return m, nil
	case "http":
		if len(args) > 1 {
			return m, fmt.Errorf("unexpected arguments")
		}

		m.http = true
	case "setup":
		m.setup = true

		if len(args) > 1 {
			if args[1] != "qr" {
				return m, fmt.Errorf("unknown argument %q; use setup or setup qr", args[1])
			}

			m.qr = true
		}
	default:
		return m, fmt.Errorf("unknown command %q; use --help", args[0])
	}

	if len(args) > 2 {
		return m, fmt.Errorf("unexpected arguments")
	}

	return m, nil
}

// instance is one run's wired dependencies.
type instance struct {
	client     *telegram.Client
	dispatcher tg.UpdateDispatcher
	server     *mcp.Server
	cfg        config.Config
	mode       mode
}

func run(ctx context.Context, args []string) error {
	m, err := parseArgs(args)
	if err != nil {
		return err
	}

	if m.help {
		fmt.Fprintln(os.Stderr, usage)
		return nil
	}

	in := &instance{mode: m}

	opts := fx.Options(providers(!m.setup), fx.Populate(&in.cfg, &in.client, &in.dispatcher))
	if !m.setup {
		opts = fx.Options(opts, fx.Populate(&in.server))
	}

	if err = fx.New(opts).Err(); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	if err = in.check(); err != nil {
		return err
	}

	if err = prepareSession(in.cfg.SessionPath, m.setup); err != nil {
		return err
	}

	err = in.client.Run(ctx, func(ctx context.Context) error {
		if m.setup {
			return in.login(ctx)
		}

		return in.serve(ctx)
	})
	if err != nil {
		return fmt.Errorf("telegram client: %w", err)
	}

	return nil
}

// check validates the settings the selected mode needs.
func (in *instance) check() error {
	if in.mode.setup && !in.mode.qr && in.cfg.Phone == "" {
		return fmt.Errorf("TELEGRAM_PHONE is required for setup")
	}

	if in.mode.http {
		if err := in.cfg.CheckHTTP(); err != nil {
			return fmt.Errorf("http settings: %w", err)
		}
	}

	return nil
}

// prepareSession creates the session directory for setup, or requires an existing session otherwise.
func prepareSession(path string, setup bool) error {
	if setup {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return fmt.Errorf("create session directory: %w", err)
		}
	} else if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("session unavailable; run telegram-mcp setup first: %w", err)
	}

	if err := os.Chmod(path, 0600); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("protect session file: %w", err)
	}

	return nil
}

func (in *instance) login(ctx context.Context) error {
	if in.mode.qr {
		if err := setupQR(ctx, in.client.QR(), in.client.Auth(), qrlogin.OnLoginToken(in.dispatcher), os.Stderr, prompt); err != nil {
			return fmt.Errorf("qr login: %w", err)
		}
	} else if err := setupLogin(ctx, in.client.Auth(), in.cfg.Phone, prompt, os.Stderr, time.Now); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Session saved. Start telegram-mcp without arguments to serve MCP.")

	return nil
}

func (in *instance) serve(ctx context.Context) error {
	statusCtx, cancel := context.WithTimeout(ctx, in.cfg.RequestTimeout)
	status, err := in.client.Auth().Status(statusCtx)

	cancel()

	if err != nil {
		return fmt.Errorf("check session: %w", err)
	}

	if !status.Authorized {
		return fmt.Errorf("session is not authorized; run telegram-mcp setup")
	}

	if !in.mode.http {
		if err = in.server.Run(ctx, &mcp.StdioTransport{}); err != nil {
			return fmt.Errorf("serve stdio: %w", err)
		}

		return nil
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", in.cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	fmt.Fprintf(os.Stderr, "serving MCP on http://%s/mcp\n", ln.Addr())

	return serveHTTP(ctx, ln, in.cfg.HTTPToken, in.server)
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

	shutdownErr := srv.Shutdown(shutdownCtx)

	waitCalls()

	if serveErr == nil {
		serveErr = <-errc // Shutdown closed the listener, so Serve has returned.
	}

	if !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}

	if shutdownErr != nil {
		return fmt.Errorf("shut down http server: %w", shutdownErr)
	}

	if err := parent.Err(); err != nil {
		return fmt.Errorf("serve http: %w", err)
	}

	return nil
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
		return fmt.Errorf("create session file: %w", err)
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
		return fmt.Errorf("write session file: %w", err)
	}

	if err := os.Rename(f.Name(), s.Path); err != nil {
		return fmt.Errorf("replace session file: %w", err)
	}

	return nil
}

var errInteractiveTerminal = errors.New("setup requires an interactive terminal")

func prompt(ctx context.Context, label string, trim bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("setup interrupted: %w", err)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errInteractiveTerminal
	}

	fmt.Fprint(os.Stderr, label)

	b, err := term.ReadPassword(int(os.Stdin.Fd()))

	fmt.Fprintln(os.Stderr)

	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}

	value := string(b)
	if trim {
		value = strings.TrimSpace(value)
	}

	return value, nil
}
