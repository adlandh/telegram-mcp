package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	mcpadapter "github.com/adlandh/telegram-mcp/internal/adapter/mcp"
	tgadapter "github.com/adlandh/telegram-mcp/internal/adapter/telegram"
	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/adlandh/telegram-mcp/internal/config"
	"github.com/adlandh/telegram-mcp/internal/port"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
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

// job is the selected mode's work, run while the Telegram client is connected.
type job func(context.Context) error

// runner runs f while the Telegram client is connected; it is (*telegram.Client).Run in production.
type runner func(ctx context.Context, f func(context.Context) error) error

func run(ctx context.Context, args []string) error {
	m, err := parseArgs(args)
	if err != nil {
		return err
	}

	if m.help {
		fmt.Fprintln(os.Stderr, usage)
		return nil
	}

	var newJob any = serveJob
	if m.setup {
		newJob = loginJob
	}

	return execute(ctx, providers(!m.setup), fx.Supply(m), fx.Invoke(prepare), fx.Provide(newJob),
		fx.Provide(func(c *telegram.Client) runner { return c.Run }))
}

// execute starts the job in the background through the Fx lifecycle and stops it on a signal,
// on ctx cancellation or when the job ends, returning the job's error.
func execute(ctx context.Context, opts ...fx.Option) error {
	var runErr error

	app := fx.New(fx.NopLogger, fx.Options(opts...), fx.Invoke(func(lc fx.Lifecycle, sd fx.Shutdowner, r runner, j job) {
		runCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		lc.Append(fx.Hook{
			OnStart: func(context.Context) error {
				go func() {
					defer close(done)

					code := 0
					if runErr = r(runCtx, j); runErr != nil {
						code = 1
					}

					_ = sd.Shutdown(fx.ExitCode(code))
				}()

				return nil
			},
			OnStop: func(context.Context) error {
				cancel()
				<-done

				return nil
			},
		})
	}))
	if err := app.Err(); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	// OnStart only spawns the job, so ctx cancellation is handled by the select below, not by Start.
	if err := app.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	select {
	case <-ctx.Done():
	case <-app.Wait():
	}

	if err := app.Stop(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("stop: %w", err)
	}

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("telegram client: %w", runErr)
	}

	return nil
}

// prepare validates the settings the selected mode needs and prepares the session file.
func prepare(cfg config.Config, m mode) error {
	if m.setup && !m.qr && cfg.Phone == "" {
		return fmt.Errorf("TELEGRAM_PHONE is required for setup")
	}

	if m.http {
		if err := cfg.CheckHTTP(); err != nil {
			return fmt.Errorf("http settings: %w", err)
		}
	}

	return prepareSession(cfg.SessionPath, m.setup)
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

func loginJob(m mode, cfg config.Config, client *telegram.Client, dispatcher tg.UpdateDispatcher) job {
	return func(ctx context.Context) error {
		if m.qr {
			if err := setupQR(ctx, client.QR(), client.Auth(), qrlogin.OnLoginToken(dispatcher), os.Stderr, prompt); err != nil {
				return fmt.Errorf("qr login: %w", err)
			}
		} else if err := setupLogin(ctx, client.Auth(), cfg.Phone, prompt, os.Stderr, time.Now); err != nil {
			return fmt.Errorf("login: %w", err)
		}

		fmt.Fprintln(os.Stderr, "Session saved. Start telegram-mcp without arguments to serve MCP.")

		return nil
	}
}

func serveJob(m mode, cfg config.Config, client *telegram.Client, server *mcp.Server) job {
	return func(ctx context.Context) error {
		statusCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
		status, err := client.Auth().Status(statusCtx)

		cancel()

		if err != nil {
			return fmt.Errorf("check session: %w", err)
		}

		if !status.Authorized {
			return fmt.Errorf("session is not authorized; run telegram-mcp setup")
		}

		if !m.http {
			if err = server.Run(ctx, &mcp.StdioTransport{}); err != nil {
				return fmt.Errorf("serve stdio: %w", err)
			}

			return nil
		}

		var lc net.ListenConfig

		ln, err := lc.Listen(ctx, "tcp", cfg.HTTPAddr)
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}

		fmt.Fprintf(os.Stderr, "serving MCP on http://%s/mcp\n", ln.Addr())

		return serveHTTP(ctx, ln, cfg.HTTPToken, server)
	}
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
		SessionStorage: tgadapter.NewSessionStorage(cfg.SessionPath), UpdateHandler: dispatcher,
	}
	if serving {
		opts.Middlewares = []telegram.Middleware{tgadapter.FloodWait()}
	}

	return opts
}
