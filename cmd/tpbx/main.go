// Command tpbx is the TPBX GUI backend: it serves the web console, exposes a
// JSON API, and bridges Asterisk (ARI + AMI) live events to connected browsers.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	tpbx "github.com/td425/tpbx"
	"github.com/td425/tpbx/internal/ami"
	"github.com/td425/tpbx/internal/api"
	"github.com/td425/tpbx/internal/ari"
	"github.com/td425/tpbx/internal/config"
	"github.com/td425/tpbx/internal/db"
	"github.com/td425/tpbx/internal/dialer"
	"github.com/td425/tpbx/internal/migrate"
	"github.com/td425/tpbx/internal/store"
	"github.com/td425/tpbx/internal/ws"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Subcommand dispatch. With no subcommand we run the server, so existing
	// `tpbx` invocations keep working.
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	var err error
	switch cmd {
	case "migrate":
		err = runMigrate()
	case "create-admin":
		err = runCreateAdmin()
	case "version", "--version", "-v":
		fmt.Println("tpbx", version)
		return
	case "serve", "":
		err = run()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\nusage: tpbx [serve|migrate|create-admin|version]\n", cmd)
		os.Exit(2)
	}

	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// runMigrate applies pending database migrations and exits. It is what
// install.sh and upgrade.sh call; running it repeatedly is safe.
func runMigrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	res, err := migrate.Run(ctx, database.Pool, tpbx.MigrationsFS)
	if err != nil {
		return err
	}
	if len(res.Applied) == 0 {
		slog.Info("migrations up to date", "skipped", res.Skipped)
	} else {
		slog.Info("migrations applied", "applied", res.Applied, "skipped", res.Skipped)
	}
	return nil
}

// runCreateAdmin creates the initial admin account from TPBX_ADMIN_USER /
// TPBX_ADMIN_PASSWORD if it does not already exist. Idempotent: install.sh
// calls it on every run and it never overwrites an existing account.
func runCreateAdmin() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	user := os.Getenv("TPBX_ADMIN_USER")
	if user == "" {
		user = "admin"
	}
	pass := os.Getenv("TPBX_ADMIN_PASSWORD")
	if pass == "" {
		return fmt.Errorf("TPBX_ADMIN_PASSWORD must be set to create the admin account")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	created, err := store.NewUsers(database.Pool).EnsureAdmin(ctx, user, pass)
	if err != nil {
		return err
	}
	if created {
		slog.Info("admin account created", "user", user)
	} else {
		slog.Info("admin account already exists, left unchanged", "user", user)
	}
	return nil
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	slog.Info("connected to postgres")

	hub := ws.NewHub()
	ariClient := ari.New(cfg.ARI.BaseURL, cfg.ARI.Username, cfg.ARI.Password, cfg.ARI.AppName)

	work := store.NewAgentWork(database.Pool)
	hopper := store.NewHopper(database.Pool)
	leads := store.NewLeads(database.Pool)
	leadCalls := store.NewLeadCalls(database.Pool)

	// The outbound dialer. It runs in-process alongside the event loops and
	// only acts on campaigns a supervisor has explicitly started, so a
	// deployment that does no outbound dialing never notices it.
	engine := dialer.New(ariClient, work, hopper, leads, leadCalls)
	go engine.Run(ctx)

	// Bridge Asterisk events into the browser hub, and into the dialer, which
	// owns the channels it originated. Both loops reconnect forever so a
	// restart of Asterisk does not take the console down.
	go runARIEvents(ctx, ariClient, hub, engine)
	go runAMIEvents(ctx, cfg, hub)

	transports := store.NewTransports(database.Pool)
	pjsipSettings := store.NewPJSIPSettings(database.Pool)

	// Global system/branding settings live in the DB so the domain, brand name
	// and default theme are admin-editable. Seed the domain from the env value
	// on first boot (only while the stored value is still empty).
	system := store.NewSystem(database.Pool)
	system.SeedDomain(ctx, cfg.Domain)

	// Regenerate the PJSIP includes (global settings + transports) from the
	// database on startup so the files Asterisk loads always reflect stored
	// state (the DB is the source of truth). Best-effort: the installer seeds
	// valid files for Asterisk's first boot, so a failure here is non-fatal.
	regeneratePJSIPGlobals(ctx, pjsipSettings, cfg.PJSIPFile)
	regenerateTransports(ctx, transports, pjsipSettings, cfg.TransportsFile)

	// Ensure the IVR prompt directory exists so uploads work out of the box.
	if cfg.SoundsDir != "" {
		if err := os.MkdirAll(cfg.SoundsDir, 0o775); err != nil {
			slog.Warn("create sounds dir", "err", err, "path", cfg.SoundsDir)
		}
	}
	// Teach the dialplan generator where uploaded prompts live, so it can
	// reference them by absolute path (language-independent playback).
	store.SetSoundLocation(cfg.SoundsDir, cfg.SoundsPrefix)

	srv := &api.Server{
		DB:             database,
		ARI:            ariClient,
		Hub:            hub,
		Ext:            store.NewExtensions(database.Pool),
		Trunks:         store.NewTrunks(database.Pool),
		Routes:         store.NewRoutes(database.Pool),
		IVRs:           store.NewIVRs(database.Pool),
		Lists:          store.NewLists(database.Pool),
		Leads:          leads,
		Campaigns:      store.NewCampaigns(database.Pool),
		AgentAccounts:  store.NewAgentAccounts(database.Pool),
		LeadCalls:      leadCalls,
		Work:           work,
		Hopper:         hopper,
		Ingroups:       store.NewIngroups(database.Pool),
		Transports:     transports,
		PJSIP:          pjsipSettings,
		Users:          store.NewUsers(database.Pool),
		Roles:          store.NewRoles(database.Pool),
		Agents:         store.NewAgents(database.Pool),
		Settings:       store.NewSettings(database.Pool),
		System:         system,
		Analytics:      store.NewAnalytics(database.Pool),
		Softphone:      store.NewSoftphone(database.Pool),
		Dashboard:      store.NewDashboard(database.Pool),
		CDR:            store.NewCDR(database.Pool),
		ApiTokens:      store.NewApiTokens(database.Pool),
		DialplanFile:   cfg.DialplanFile,
		TransportsFile: cfg.TransportsFile,
		PJSIPFile:      cfg.PJSIPFile,
		WebDir:         webDir(),
		AgentWebDir:    agentWebDir(),
		SoundsDir:      cfg.SoundsDir,
		SoundsPrefix:   cfg.SoundsPrefix,
		Domain:         cfg.Domain,
		WSSPort:        cfg.WebRTC.WSSPort,
		TURNSecret:     cfg.WebRTC.TURNSecret,
		TURNTTL:        cfg.WebRTC.TURNTTL,
		// Infra values are shown read-only (masked) on the System tab so admins
		// can see what the service is bound to without being able to edit
		// bootstrap/credential config from the UI.
		Infra: api.InfraInfo{
			HTTPAddr:       cfg.HTTPAddr,
			DatabaseURL:    cfg.DatabaseURL,
			ARIURL:         cfg.ARI.BaseURL,
			ARIUser:        cfg.ARI.Username,
			AMIAddr:        cfg.AMI.Addr,
			AMIUser:        cfg.AMI.Username,
			AsteriskConf:   cfg.AsteriskConfDir,
			DialplanFile:   cfg.DialplanFile,
			TransportsFile: cfg.TransportsFile,
			PJSIPFile:      cfg.PJSIPFile,
			SoundsDir:      cfg.SoundsDir,
			WSSPort:        cfg.WebRTC.WSSPort,
		},
		// Realtime queue membership is already live in the database; this only
		// asks app_queue to notice sooner.
		ReloadQueues: func(ctx context.Context) error {
			_, err := ami.Exec(ctx, cfg.AMI.Addr, cfg.AMI.Username, cfg.AMI.Password,
				cfg.AMI.Timeout, "queue reload members")
			return err
		},
		RestartAsterisk: func(ctx context.Context) error {
			_, err := ami.Exec(ctx, cfg.AMI.Addr, cfg.AMI.Username, cfg.AMI.Password,
				cfg.AMI.Timeout, "core restart now")
			return err
		},
	}

	// Regenerate the routing/IVR dialplan on startup so changes to sound-path
	// resolution (absolute prompt paths) take effect without a manual edit.
	// Best-effort: a failure here just leaves the previous generated file.
	go func() {
		rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.ApplyDialplan(rctx); err != nil {
			slog.Warn("startup dialplan regenerate failed", "err", err)
		} else {
			slog.Info("dialplan regenerated on startup")
		}
	}()

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("http listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// regenerateTransports writes the transports include from the database. Errors
// are logged, not fatal: the installer seeds a valid file for first boot.
func regenerateTransports(ctx context.Context, t *store.Transports, p *store.PJSIPSettingsStore, path string) {
	if path == "" {
		return
	}
	content, err := t.GenerateConfig(ctx, p.TLSDefaults(ctx))
	if err != nil {
		slog.Warn("generate transports config", "err", err)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		slog.Warn("write transports file", "err", err, "path", path)
		return
	}
	slog.Info("transports include regenerated", "path", path)
}

// regeneratePJSIPGlobals writes the PJSIP [global]/[system] include from the
// database. Best-effort like the transports include.
func regeneratePJSIPGlobals(ctx context.Context, p *store.PJSIPSettingsStore, path string) {
	if path == "" {
		return
	}
	content, err := p.GenerateConfig(ctx)
	if err != nil {
		slog.Warn("generate pjsip globals config", "err", err)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		slog.Warn("write pjsip globals file", "err", err, "path", path)
		return
	}
	slog.Info("pjsip globals include regenerated", "path", path)
}

func webDir() string {
	if d := os.Getenv("TPBX_WEB_DIR"); d != "" {
		return d
	}
	return "web/dist"
}

func agentWebDir() string {
	if d := os.Getenv("TPBX_AGENT_WEB_DIR"); d != "" {
		return d
	}
	return "web/dist-agent"
}

// runARIEvents keeps a Stasis event subscription alive, forwarding each event
// to the browser hub, and reconnects with backoff on failure.
func runARIEvents(ctx context.Context, client *ari.Client, hub *ws.Hub, engine *dialer.Engine) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := client.StreamEvents(ctx, func(ev ari.Event) {
			hub.Broadcast(ws.Envelope{Kind: "ari", Data: map[string]any{
				"type": ev.Type,
				"raw":  ev.Raw,
			}})
			// The dialer owns the channels it originated; it ignores the rest.
			engine.HandleEvent(ctx, ev)
		})
		if ctx.Err() != nil {
			return
		}
		slog.Warn("ari events disconnected, retrying", "err", err, "in", backoff)
		sleep(ctx, backoff)
		backoff = nextBackoff(backoff)
	}
}

// runAMIEvents keeps an AMI session alive, forwarding events to the hub.
func runAMIEvents(ctx context.Context, cfg *config.Config, hub *ws.Hub) {
	backoff := time.Second
	for ctx.Err() == nil {
		client, err := ami.Dial(ctx, cfg.AMI.Addr, cfg.AMI.Username, cfg.AMI.Password, cfg.AMI.Timeout)
		if err != nil {
			slog.Warn("ami connect failed, retrying", "err", err, "in", backoff)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		slog.Info("ami connected")
		backoff = time.Second
		for {
			select {
			case <-ctx.Done():
				client.Close()
				return
			case msg, ok := <-client.Events():
				if !ok {
					break
				}
				if ev := msg.Get("Event"); ev != "" {
					hub.Broadcast(ws.Envelope{Kind: "ami", Data: msg})
				}
				continue
			}
			break
		}
		client.Close()
		slog.Warn("ami session ended, reconnecting")
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
