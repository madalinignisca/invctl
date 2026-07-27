// Command invctl serves the infrastructure inventory.
//
// This file is wiring only: parse configuration, open the database, migrate,
// build the dependency graph of components, serve. Any logic that appears here
// belongs in a package.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexedwards/scs/postgresstore"
	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"

	"github.com/gabriel/invctl/internal/auth"
	"github.com/gabriel/invctl/internal/config"
	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/seed"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web"
	"github.com/gabriel/invctl/internal/web/handlers"
	"github.com/gabriel/invctl/internal/web/render"
	webassets "github.com/gabriel/invctl/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		migrateOnly = flag.Bool("migrate", false, "apply migrations and exit")
		seedOnly    = flag.Bool("seed", false, "load the demo estate and exit")
		devMode     = flag.Bool("dev", false, "reparse templates on every request")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)

	db, err := store.Open(cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	slog.Info("database ready", "driver", cfg.DBDriver)

	if *migrateOnly {
		slog.Info("migrations applied; exiting as requested")
		return nil
	}

	st := store.New(db)

	if *seedOnly || cfg.SeedOnStart {
		if err := loadDemoData(ctx, st); err != nil {
			return err
		}
		if *seedOnly {
			return nil
		}
	}

	if err := ensureAdmin(ctx, st, cfg); err != nil {
		return err
	}

	sessions, err := newSessionManager(db, cfg)
	if err != nil {
		return err
	}

	authenticator, err := buildAuthenticator(st, cfg)
	if err != nil {
		return err
	}

	renderer, err := render.New(webassets.FS, *devMode)
	if err != nil {
		return err
	}
	staticFS, err := fs.Sub(webassets.FS, "static")
	if err != nil {
		return fmt.Errorf("opening embedded static assets: %w", err)
	}

	app := &handlers.App{
		Store:    st,
		Render:   renderer,
		Sessions: sessions,
		Auth:     authenticator,
		Authz:    auth.NewAuthorizer(cfg.AdminUsers),
		Config:   cfg,
	}

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.Routes(app, staticFS, app.Authz),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	return serve(server, cfg)
}

// serve runs the HTTP server and shuts it down cleanly on a signal, so an
// in-flight write transaction is not cut off mid-commit.
func serve(server *http.Server, cfg *config.Config) error {
	shutdownErr := make(chan error, 1)
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		sig := <-quit
		slog.Info("shutting down", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		shutdownErr <- server.Shutdown(ctx)
	}()

	slog.Info("listening", "addr", cfg.Listen, "admins", cfg.AdminUsers)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving: %w", err)
	}
	if err := <-shutdownErr; err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	slog.Info("stopped")
	return nil
}

func setupLogging(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}

// newSessionManager builds a database-backed session store.
//
// The store is the one piece of session handling that is engine-specific:
// scs ships a separate implementation per driver because the expiry column
// has a different type in each.
func newSessionManager(db *store.DB, cfg *config.Config) (*scs.SessionManager, error) {
	sessions := scs.New()
	sessions.Lifetime = cfg.SessionTimeout
	sessions.IdleTimeout = cfg.SessionTimeout
	sessions.Cookie.Name = "invctl_session"
	sessions.Cookie.HttpOnly = true
	sessions.Cookie.Path = "/"
	sessions.Cookie.SameSite = http.SameSiteLaxMode
	sessions.Cookie.Secure = cfg.SecureCookies

	switch db.Driver {
	case store.DriverPostgres:
		sessions.Store = postgresstore.New(db.SQLDB())
	default:
		sessions.Store = sqlite3store.New(db.SQLDB())
	}

	if config.SessionKeyGenerated() {
		slog.Warn("INV_SESSION_KEY is not set: a random key was generated, " +
			"so sessions will not survive a restart")
	}
	return sessions, nil
}

// buildAuthenticator assembles the configured authenticators into a chain.
func buildAuthenticator(st *store.SQLStore, cfg *config.Config) (auth.Authenticator, error) {
	var authenticators []auth.Authenticator
	if cfg.AuthLocal {
		authenticators = append(authenticators, auth.NewLocalAuthenticator(st))
	}
	if cfg.AuthLDAP {
		authenticators = append(authenticators, auth.NewLDAPAuthenticator(cfg.LDAP, st))
		slog.Info("ldap authentication enabled", "url", cfg.LDAP.URL)
	}
	if len(authenticators) == 0 {
		return nil, fmt.Errorf("building authenticator: none enabled")
	}
	return auth.NewChain(st, authenticators...), nil
}

// ensureAdmin seeds the first account so a fresh database is usable.
//
// When no password is configured one is generated and logged exactly once.
// The alternative -- a well-known default password -- is how demo deployments
// become incidents.
func ensureAdmin(ctx context.Context, st *store.SQLStore, cfg *config.Config) error {
	if !cfg.AuthLocal {
		return nil
	}
	count, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	password := cfg.DevAdminPassword
	generated := false
	if password == "" {
		raw := make([]byte, 18)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("generating admin password: %w", err)
		}
		password = base64.RawURLEncoding.EncodeToString(raw)
		generated = true
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	user, err := domain.NewAppUser(store.NewID(), cfg.AdminUsername, domain.UserSourceLocal, st.Now())
	if err != nil {
		return err
	}
	user.PasswordHash = &hash
	display := "Seeded administrator"
	user.DisplayName = &display

	if err := st.CreateUser(ctx, domain.SystemActor, user); err != nil {
		return err
	}

	if generated {
		slog.Warn("seeded the first account; this password is shown once",
			"username", user.Username, "password", password)
	} else {
		slog.Info("seeded the first account", "username", user.Username)
	}
	if !cfg.IsAdmin(user.Username) {
		slog.Warn("the seeded account is not in INV_ADMIN_USERS, so it has read-only access",
			"username", user.Username)
	}
	return nil
}

// loadDemoData populates the demo estate, and does nothing if data is already
// present -- re-seeding would collide on every unique code.
func loadDemoData(ctx context.Context, st *store.SQLStore) error {
	envs, err := st.ListEnvironments(ctx)
	if err != nil {
		return err
	}
	if len(envs) > 0 {
		slog.Info("demo data not loaded: the database already has environments")
		return nil
	}
	if _, err := seed.Load(ctx, st); err != nil {
		return fmt.Errorf("loading demo data: %w", err)
	}
	slog.Info("demo estate loaded")
	return nil
}
