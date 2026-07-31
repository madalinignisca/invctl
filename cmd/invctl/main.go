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
	"strconv"
	"strings"
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

		// The retention prune (docs/AUDIT.md rule 10). Admin-invoked, here,
		// and nowhere else: never a handler, never a side effect of a write
		// path, never a timer inside the server. A deletion that can happen
		// without somebody asking for it is one nobody remembers asking for.
		pruneObserved = flag.Bool("prune-observed", false,
			"delete observed transitions older than -prune-keep-days and exit")
		pruneKeepDays = flag.Int("prune-keep-days", domain.MinInScopeRetentionDays,
			"days of observed transitions to keep; anything in an in_scope environment "+
				"keeps at least "+strconv.Itoa(domain.MinInScopeRetentionDays)+" regardless")
		pruneAs = flag.String("prune-as", "",
			"the operator account the prune is recorded against; required with -prune-observed")
		pruneDryRun = flag.Bool("prune-dry-run", false,
			"report what a prune would remove, and remove nothing")
		// A separate flag rather than a mode of -prune-observed. The drift
		// queue is a worklist, the transition ledger is evidence, and they
		// carry different safety arguments -- sharing one entry point would
		// mean one set of options guarding two different risks.
		pruneUnmatched = flag.Bool("prune-unmatched", false,
			"delete resolved drift-queue entries older than -prune-keep-days and exit")
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
	defer func() { _ = db.Close() }()

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

	// Presentation only: stage a set of demo observations through the real
	// recorder after the inventory. Never on by default -- an operator's first
	// run should show the honest empty state, not readings nobody sent.
	seed.ObserveDemo = cfg.SeedObservations

	if *pruneObserved {
		return pruneObservedTransitions(ctx, st, cfg, *pruneKeepDays, *pruneAs, *pruneDryRun)
	}
	if *pruneUnmatched {
		return pruneUnmatchedObservations(ctx, st, cfg, *pruneKeepDays, *pruneAs, *pruneDryRun)
	}

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

	// After ensureAdmin, because an override is declared state and needs a real
	// operator to attribute it to. Best-effort: a demo without an override is
	// still a demo, and refusing to start over presentation data would be
	// absurd.
	if cfg.SeedObservations && cfg.SeedOnStart {
		if err := seed.StageDemoOverride(ctx, st, cfg.AdminUsername); err != nil {
			slog.Warn("demo override not staged", "error", err)
		}
	}

	sessions, err := newSessionManager(db, cfg)
	if err != nil {
		return err
	}

	authenticator, err := buildAuthenticator(st, cfg)
	if err != nil {
		return err
	}

	renderer, err := render.New(webassets.FS, *devMode, cfg.Currency)
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

	// One recorder, shared between the webhook and the flusher below. Two
	// would each hold half of rule 4's buffer, and the half the flusher does
	// not own would never be written down -- so every quiet entity would age
	// into `unknown` while its collector reported normally.
	recorder := store.NewObservedRecorder(st)

	agents, err := buildAgentSurface(ctx, st, cfg, recorder, sessions.Cookie.Name)
	if err != nil {
		return err
	}
	// The dashboard needs the configured ids so a credential that has never
	// checked in is shown as silent rather than omitted.
	if agents != nil {
		app.Agents = agents.Registry
	}

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.Routes(app, staticFS, app.Authz, agents),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// The observation flusher (docs/AUDIT.md rule 4). It must outlive the
	// server's shutdown by one flush: a deployment that never runs it invents
	// outages, because an entity whose state never changes keeps an ageing
	// last_report_at and renders stale.
	flushCtx, stopFlushing := context.WithCancel(context.Background())
	ticker := time.NewTicker(store.FlushInterval)
	defer ticker.Stop()
	flusherDone := make(chan struct{})
	go func() {
		recorder.Run(flushCtx, ticker.C)
		close(flusherDone)
	}()

	serveErr := serve(server, cfg)
	stopFlushing()
	<-flusherDone
	return serveErr
}

// pruneUnmatchedObservations clears drift-queue entries nothing is reporting
// any more.
//
// The queue holds reports about entities the inventory does not have, which
// rule 6 wants surfaced as findings. A finding that has been dealt with -- the
// asset was entered, or the reporter was corrected -- should not stay in the
// list making the real backlog harder to read. It is also the one observed
// table an authenticated credential can grow deliberately, so it needs a way
// out as well as the per-reporter cap.
//
// Same operator requirement as the transition prune, for the same reason: an
// agent that could clear the record of entities it named could cover its own
// reconnaissance.
func pruneUnmatchedObservations(ctx context.Context, st *store.SQLStore, cfg *config.Config,
	keepDays int, username string, dryRun bool) error {
	user, err := resolvePruneOperator(ctx, st, cfg, username, keepDays)
	if err != nil {
		return err
	}
	report, err := st.PruneUnmatchedObservations(ctx, domain.UserActor(user), store.PruneOptions{
		Before: st.Now().AddDate(0, 0, -keepDays),
		DryRun: dryRun,
	})
	if err != nil {
		return err
	}
	verb := "pruned drift queue"
	if dryRun {
		verb = "dry run: would prune drift queue"
	}
	slog.Info(verb,
		"before", domain.FormatTime(report.Before),
		"keep_days", keepDays,
		"deleted", report.Deleted,
		"actor", user.ID)
	return nil
}

// resolvePruneOperator is the shared front half of both prunes: an operator
// with write access, named by opaque id.
func resolvePruneOperator(ctx context.Context, st *store.SQLStore, cfg *config.Config,
	username string, keepDays int) (*domain.AppUser, error) {
	if strings.TrimSpace(username) == "" {
		return nil, errors.New("pruning: -prune-as is required; the run records which operator asked for it")
	}
	if keepDays < 0 {
		return nil, errors.New("pruning: -prune-keep-days must not be negative")
	}
	user, err := st.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("pruning: %w", err)
	}
	if !auth.NewAuthorizer(cfg.AdminUsers).CanWrite(user) {
		return nil, fmt.Errorf("pruning: %s does not have write access, "+
			"so the audit entry would name somebody who could not have done this", user.Username)
	}
	return user, nil
}

// pruneObservedTransitions runs the retention prune (docs/AUDIT.md rule 10).
//
// Three things are deliberate about the shape of this.
//
// It resolves -prune-as to an app_user and passes that account's OPAQUE ID to
// the store. change_log.actor never holds a username, here or anywhere else, so
// the audit trail carries no personal data and scrubbing an account to answer
// an erasure request leaves the entry intact and simply stops resolving a name
// for it.
//
// It requires that account to have write access. "Admin-invoked" has to mean
// something, and an entry naming somebody who could not have performed the act
// is worse than no name: it is a wrong name that looks authoritative.
//
// It never runs on its own. There is no schedule here and no call to this from
// the server, because rule 10 says the prune is invoked, and because the one
// thing that must not happen quietly is history going away.
func pruneObservedTransitions(ctx context.Context, st *store.SQLStore, cfg *config.Config,
	keepDays int, username string, dryRun bool) error {
	if strings.TrimSpace(username) == "" {
		return errors.New("pruning observed transitions: -prune-as is required; " +
			"the run records which operator asked for it")
	}
	if keepDays < 0 {
		return errors.New("pruning observed transitions: -prune-keep-days must not be negative")
	}

	user, err := st.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("pruning observed transitions: %w", err)
	}
	if !auth.NewAuthorizer(cfg.AdminUsers).CanWrite(user) {
		return fmt.Errorf("pruning observed transitions: %s does not have write access, "+
			"so the audit entry would name somebody who could not have done this", user.Username)
	}

	before := st.Now().AddDate(0, 0, -keepDays)
	report, err := st.PruneObservedTransitions(ctx, domain.UserActor(user), store.PruneOptions{
		Before: before,
		DryRun: dryRun,
	})
	if err != nil {
		return err
	}

	verb := "pruned observed transitions"
	if dryRun {
		verb = "dry run: would prune observed transitions"
	}
	slog.Info(verb,
		"before", domain.FormatTime(report.Before),
		"keep_days", keepDays,
		"deleted", report.Deleted,
		"protected_in_scope", report.Protected,
		"run", report.RunID)

	if report.FloorApplied() {
		// Said out loud rather than left to the counts, and said whether or not
		// anything was actually held back today. An operator who set 30 days and
		// saw a number come back would otherwise conclude the estate now keeps
		// 30 days of history, which for the in_scope half is untrue and will
		// stay untrue on every future run.
		slog.Warn("the in_scope retention floor is in force: anything resolving to an in_scope "+
			"environment keeps at least the minimum regardless of -prune-keep-days",
			"floor", domain.FormatTime(report.InScopeFloor),
			"minimum_days", domain.MinInScopeRetentionDays,
			"kept_by_the_floor", report.Protected)
	}
	return nil
}

// buildAgentSurface assembles the machine-facing route, or nothing at all.
//
// docs/AUDIT.md rule 6. Every failure here refuses to start. The alternative --
// logging a warning and carrying on with a credential dropped or a collision
// unresolved -- means the deployment is running with an authorization model
// that differs from the one somebody wrote down, which is the state this whole
// rule exists to prevent.
func buildAgentSurface(ctx context.Context, st *store.SQLStore, cfg *config.Config,
	recorder store.ObservedStore, sessionCookie string) (*web.AgentSurface, error) {
	registry, err := auth.NewAgentRegistry(cfg.AgentCredentials)
	if err != nil {
		return nil, err
	}
	if !registry.Enabled() {
		slog.Info("no monitoring credentials configured; the observation route is not mounted")
		return nil, nil
	}

	// Rule 5: "Startup fails if an agent name collides with an app_user
	// username." Checked here rather than in config because it needs the
	// database, and checked before the listener opens rather than at first use.
	clashes, err := st.UsernamesMatching(ctx, registry.IDs())
	if err != nil {
		return nil, err
	}
	if len(clashes) > 0 {
		return nil, fmt.Errorf("configuring monitoring credentials: %s also names an operator account; "+
			"a credential and a person must never share a name in the audit trail",
			strings.Join(clashes, ", "))
	}

	// An environment code with no environment behind it is almost always a
	// typo, and its effect is a credential that authenticates and is then
	// refused on everything -- which reads as a broken collector. Environments
	// are created at runtime, so this is a warning rather than a refusal.
	warnUnknownScopes(ctx, st, cfg)

	for _, cred := range cfg.AgentCredentials {
		slog.Info("monitoring credential configured", "credential", cred)
	}
	return &web.AgentSurface{
		Registry:      registry,
		Handler:       handlers.NewObservationAPI(recorder),
		SessionCookie: sessionCookie,
	}, nil
}

// warnUnknownScopes reports scope entries that name no environment.
func warnUnknownScopes(ctx context.Context, st *store.SQLStore, cfg *config.Config) {
	envs, err := st.ListEnvironments(ctx)
	if err != nil {
		slog.Warn("could not check monitoring credential scopes against the environments", "error", err)
		return
	}
	known := make(map[string]bool, len(envs))
	for _, e := range envs {
		known[e.Code] = true
	}
	for _, cred := range cfg.AgentCredentials {
		for _, code := range cred.Environments {
			if !known[code] {
				slog.Warn("monitoring credential is scoped to an environment that does not exist",
					"credential", cred.ID, "environment", code)
			}
		}
	}
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
		return nil, errors.New("building authenticator: none enabled")
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
