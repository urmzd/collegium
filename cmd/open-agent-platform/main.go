// Command open-agent-platform runs the platform: API, sign-in, event stream,
// schedules and agent execution, in one process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urmzd/opentag/pkg/agentspec"
	"github.com/urmzd/opentag/pkg/registry"

	"github.com/urmzd/open-agent-platform/internal/httpapi"
	"github.com/urmzd/open-agent-platform/internal/platform"
	"github.com/urmzd/open-agent-platform/pkg/alerts"
	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/call"
)

// Set at release time with -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "open-agent-platform:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		if len(args) == 1 && args[0] == "version" {
			fmt.Printf("%s (%s, %s)\n", version, commit, date)
			return nil
		}
		return errors.New("usage: open-agent-platform serve [flags] | version")
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	authMode := fs.String("auth", "oidc", `who may call: "oidc", or "dev" for a loopback-only preview with no sign-in`)
	tenant := fs.String("tenant", "default", "tenant every caller is confined to, unless -tenant-claim is set")
	tenantClaim := fs.String("tenant-claim", "", "token claim that carries the caller's tenant")
	groupsClaim := fs.String("groups-claim", "groups", "token claim that lists a person's groups")
	adminGroup := fs.String("admin-group", "oap-admins", "identity provider group granted the admin role")
	operatorGroup := fs.String("operator-group", "oap-operators", "identity provider group granted the operator role")
	viewerGroup := fs.String("viewer-group", "oap-viewers", "identity provider group granted the viewer role")
	publicURL := fs.String("public-url", "http://localhost:8080", "where browsers reach this server")
	webDir := fs.String("web", "", "directory of a built web console to serve")
	maxDepth := fs.Int("max-depth", 4, "deepest an agent-to-agent call may land")
	maxFanOut := fs.Int("max-fan-out", 16, "most calls one run may make")
	seed := fs.Bool("seed", false, "create two example agents that need no model key")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Credentials come from the environment, never from flags: a flag ends up
	// in shell history and in ps output.
	var authn *httpapi.Authn
	switch *authMode {
	case "dev":
		if !loopback(*addr) {
			return fmt.Errorf("-auth=dev grants everyone admin, so it only listens on loopback, not %q", *addr)
		}
		log.Warn("dev auth: every request is an admin. Do not expose this server.")
		authn = &httpapi.Authn{Dev: &auth.Principal{
			Subject: "dev", Tenant: *tenant, Kind: auth.User, Name: "Developer", Roles: []auth.Role{auth.Admin},
		}}
	case "oidc":
		v, err := auth.NewVerifier(ctx, auth.OIDCConfig{
			Issuer:      os.Getenv("OAP_OIDC_ISSUER"),
			ClientID:    os.Getenv("OAP_OIDC_CLIENT_ID"),
			Audience:    os.Getenv("OAP_OIDC_AUDIENCE"),
			Tenant:      *tenant,
			TenantClaim: *tenantClaim,
			GroupsClaim: *groupsClaim,
			GroupRoles: map[string]auth.Role{
				*adminGroup: auth.Admin, *operatorGroup: auth.Operator, *viewerGroup: auth.Viewer,
			},
		})
		if err != nil {
			return fmt.Errorf("%w (set OAP_OIDC_ISSUER, OAP_OIDC_CLIENT_ID, OAP_OIDC_CLIENT_SECRET and OAP_OIDC_AUDIENCE, or use -auth=dev locally)", err)
		}
		authn, err = httpapi.NewAuthn(v, os.Getenv("OAP_OIDC_CLIENT_ID"), os.Getenv("OAP_OIDC_CLIENT_SECRET"), *publicURL,
			[]string{"openid", "profile", "email", *groupsClaim})
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("-auth must be oidc or dev, not %q", *authMode)
	}

	p, err := platform.New(platform.Config{
		Limits: call.Limits{MaxDepth: *maxDepth, MaxFanOut: *maxFanOut},
		Log:    log,
	})
	if err != nil {
		return err
	}
	if *seed {
		if err := seedAgents(ctx, p, *tenant); err != nil {
			return err
		}
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           (&httpapi.Server{P: p, Authn: authn, Log: log, WebDir: *webDir}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 16,
	}

	errc := make(chan error, 2)
	go func() { errc <- p.Run(ctx) }()
	go func() {
		log.Info("listening", "addr", *addr, "auth", *authMode, "tenant", *tenant)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	return err
}

// loopback reports whether addr can only be reached from this machine.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// seedAgents creates two agents on opentag's offline provider, so the console
// has something to run before a model key is configured.
func seedAgents(ctx context.Context, p *platform.Platform, tenant string) error {
	for _, spec := range []agentspec.AgentSpec{
		{
			Name: "lead", Description: "Takes a request and may call the helper.",
			Model: "offline", Provider: "offline", SystemPrompt: "You coordinate work.",
			Access: agentspec.Access{Spawn: []string{"helper"}},
		},
		{
			Name: "helper", Description: "Does one piece of work for the lead.",
			Model: "offline", Provider: "offline", SystemPrompt: "You do one task well.",
		},
	} {
		_, err := p.Agents.Create(ctx, tenant, spec, "seed")
		if err != nil && !errors.Is(err, registry.ErrExists) {
			return fmt.Errorf("seed %s: %w", spec.Name, err)
		}
	}
	// One rule, so a failure shows up somewhere before anyone configures alerts.
	err := p.Alerts.AddRule(tenant, alerts.Rule{Name: "any-run-fails", Kind: alerts.RunFailed, Severity: alerts.Critical, CreatedBy: "seed"})
	if err != nil && !errors.Is(err, alerts.ErrExists) {
		return fmt.Errorf("seed alert rule: %w", err)
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		slog.Default().Info("seeded agents use the offline provider and call no model")
	}
	return nil
}
