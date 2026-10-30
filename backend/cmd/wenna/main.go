// Command wenna lets a user's personal agents share what the user tells them:
// an MCP server over a SQLite file. It is wiring and lifecycle only: configuration comes from
// internal/config, routes from internal/httpapi.
//
//	wenna stdio    serve MCP over stdin/stdout; an AI app starts this
//	wenna serve    serve MCP and the export over HTTP, behind WENNA_TOKEN or a configured OAuth provider
//	wenna export   write the whole history, gzipped JSON, to stdout
//	wenna version  print the version this binary was built as
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/uni-industries/wenna/backend/internal/authn"
	"github.com/uni-industries/wenna/backend/internal/config"
	"github.com/uni-industries/wenna/backend/internal/export"
	"github.com/uni-industries/wenna/backend/internal/gate"
	"github.com/uni-industries/wenna/backend/internal/httpapi"
	"github.com/uni-industries/wenna/backend/internal/mcpserver"
	"github.com/uni-industries/wenna/backend/internal/sqlitestore"
)

// Timeouts are properties of the server, not of an environment, so they are
// constants rather than configuration (CLAUDE.md § Environments). The export
// extends the write deadline per chunk.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// maxHeaderBytes bounds what an unauthenticated request can make the server
// hold: room for a few signed tokens, far under net/http's 1 MiB default.
const maxHeaderBytes = 64 << 10

const usage = `usage: wenna <command>

  stdio    serve MCP over stdin/stdout (what an AI app runs)
  serve    serve MCP over HTTP at /api/mcp; requires WENNA_TOKEN
  export   write all memory history, gzipped JSON, to stdout
  version  print the version

Settings are environment variables; see WENNA_DB, WENNA_TOKEN, PORT,
WENNA_LISTEN_HOST, WENNA_BEHIND_TLS_PROXY in the README.
`

// busyMsg is printed when another wenna process holds a new database while
// setting it up.
const busyMsg = "another wenna process is setting up the memory database at the same time; start wenna again"

// noDirMsg is printed when WENNA_DB's directory is missing: on a hosting
// platform, the volume is not mounted.
const noDirMsg = "the directory in WENNA_DB does not exist. Create it, or on a hosting platform mount a persistent volume there (HOSTING.md)"

// noSearchMsg is printed when this binary was built without full-text
// search: built with plain `go build` instead of make.
const noSearchMsg = "this wenna was built without memory search. Build it with `make build` (README.md)"

// version is set at release build time with -ldflags "-X main.version=<tag>".
var version = "dev"

// errUsage is a mistake in the command line, reported with usage.
var errUsage = errors.New("unknown command")

func main() {
	// Logs go to stderr: in stdio mode, stdout belongs to the protocol.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, logger)
	stop()
	switch {
	case errors.Is(err, errUsage):
		if _, err := fmt.Fprint(os.Stderr, usage); err != nil {
			logger.Error("write usage", "err", err)
		}
		os.Exit(2)
	case err != nil:
		// Plain text, not a log record: this is what a person reads when
		// wenna refuses to start, and it often says why and what to do.
		msg := err.Error()
		switch {
		case errors.Is(err, sqlitestore.ErrBusy):
			// Expected when apps start wenna together on a new database: the
			// person needs the instruction, not the driver's text.
			msg = busyMsg
		case errors.Is(err, sqlitestore.ErrNoDir):
			msg = noDirMsg
		case errors.Is(err, sqlitestore.ErrNoFTS5):
			msg = noSearchMsg
		}
		if _, werr := fmt.Fprintln(os.Stderr, "wenna:", msg); werr != nil {
			logger.Error("wenna failed", "err", err, "write_err", werr)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookup func(string) (string, bool), stdout io.Writer, logger *slog.Logger) error {
	if len(args) != 1 {
		return errUsage
	}
	switch args[0] {
	case "version":
		if _, err := fmt.Fprintln(stdout, version); err != nil {
			return fmt.Errorf("write version: %w", err)
		}
		return nil
	case "stdio":
		return withStore(ctx, lookup, func(store *sqlitestore.Store) error {
			err := mcpserver.Server(store, logger).Run(ctx, &mcp.StdioTransport{})
			// An AI app stops its server with a signal: that is a normal
			// shutdown, not a failure. An interrupted export stays an error.
			if stoppedBySignal(ctx, err) {
				return nil
			}
			return err
		})
	case "export":
		return withStore(ctx, lookup, func(store *sqlitestore.Store) error {
			return export.Write(ctx, store, time.Now(), stdout)
		})
	case "serve":
		cfg, err := config.LoadServe(lookup)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return withStore(ctx, lookup, func(store *sqlitestore.Store) error {
			return serve(ctx, cfg, store, logger)
		})
	}
	return errUsage
}

// stoppedBySignal reports whether err is only ctx's cancellation, which in
// main means SIGINT or SIGTERM arrived.
func stoppedBySignal(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, context.Canceled)
}

// withStore opens the configured database for fn and closes it after.
func withStore(ctx context.Context, lookup func(string) (string, bool), fn func(*sqlitestore.Store) error) (err error) {
	cfg, err := config.Load(lookup)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	open := sqlitestore.Open
	if cfg.DBDirMustExist {
		open = sqlitestore.OpenExistingDir
	}
	store, err := open(ctx, cfg.DBPath, sqlitestore.WithTrigram(cfg.Trigram))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if cErr := store.Close(); cErr != nil {
			err = errors.Join(err, fmt.Errorf("close store: %w", cErr))
		}
	}()
	return fn(store)
}

func serve(ctx context.Context, cfg config.Serve, store *sqlitestore.Store, logger *slog.Logger) error {
	// Listen before serving so a bind failure returns here, at start, and the
	// log names the address actually bound.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	protect, meta, err := protection(cfg, &http.Client{}, logger)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           newHandler(store, cfg.PublicHost, protect, meta, logger),
		ReadHeaderTimeout: readHeaderTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	// cfg logs through config.Serve.LogValue: the token redacted, and which
	// OAuth providers are on, so a deploy can see its settings took.
	logger.Info("server listening", "addr", ln.Addr().String(), "config", cfg)

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("server stopped")
	return nil
}

// newHandler assembles the HTTP API: the MCP connector and the export, both
// behind protect. meta, when non-nil, serves the protected-resource metadata.
// publicHost is the host of WENNA_PUBLIC_URL, or "".
func newHandler(store *sqlitestore.Store, publicHost string, protect func(http.Handler) http.Handler, meta http.Handler, logger *slog.Logger) http.Handler {
	return httpapi.New(logger, httpapi.Routes{
		MCP:              mcpserver.Handler(mcpserver.Server(store, logger), logger),
		Export:           export.Handler(store, "", time.Now, logger),
		Protect:          protect,
		ResourceMetadata: meta,
		PublicHost:       publicHost,
	})
}

// protection builds the gate from the configuration: the token always, and
// each OAuth provider only when configured. meta is non-nil only when WorkOS
// is on: Cloudflare Access answers discovery itself, in front of wenna.
// client fetches the providers' keys and metadata.
func protection(cfg config.Serve, client *http.Client, logger *slog.Logger) (protect func(http.Handler) http.Handler, meta http.Handler, err error) {
	o := gate.Options{Token: cfg.Token.Reveal(), Logger: logger}
	// Assigned only when built: a nil *authn.Verifier in the interface would
	// be called.
	if a := cfg.Access; a != nil {
		v, err := authn.NewAccess(authn.Access{TeamDomain: a.TeamDomain, Audience: a.Audience, OwnerEmail: a.OwnerEmail}, client, time.Now, logger)
		if err != nil {
			return nil, nil, fmt.Errorf("cloudflare access: %w", err)
		}
		o.Access = v
	}
	if w := cfg.WorkOS; w != nil {
		v, err := authn.NewWorkOS(authn.WorkOS{Issuer: w.Issuer, Resource: w.Resource, OwnerSubject: w.OwnerSubject}, client, time.Now, logger)
		if err != nil {
			return nil, nil, fmt.Errorf("workos: %w", err)
		}
		o.WorkOS = v
		metaURL, err := resourceMetadataURL(w.Resource)
		if err != nil {
			return nil, nil, fmt.Errorf("workos: %w", err)
		}
		o.ResourceMetadataURL = metaURL
		meta = auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
			Resource:               w.Resource,
			AuthorizationServers:   []string{w.Issuer},
			BearerMethodsSupported: []string{"header"},
		})
	}
	return gate.Require(o), meta, nil
}

// resourceMetadataURL is where RFC 9728 § 3.1 puts resource's metadata: the
// well-known path inserted between its origin and its path.
func resourceMetadataURL(resource string) (string, error) {
	u, err := url.Parse(resource)
	if err != nil {
		return "", fmt.Errorf("parse resource %q: %w", resource, err)
	}
	// httpapi mounts the metadata for its MCP path only; another path would
	// advertise a metadata URL that answers 404.
	if u.Path != httpapi.MCPPath {
		return "", fmt.Errorf("%s=%q must end at wenna's MCP path: use %q", config.EnvWorkOSResource, resource, u.Scheme+"://"+u.Host+httpapi.MCPPath)
	}
	return u.Scheme + "://" + u.Host + httpapi.ResourceMetadataPath + u.Path, nil
}
