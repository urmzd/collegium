// Command collegium runs the platform: API, sign-in, event stream,
// schedules and agent execution, in one process.
package main

import (
	"context"
	"encoding/json"
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

	"github.com/urmzd/mandatum/pkg/agentspec"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/registry"

	"github.com/urmzd/collegium/internal/httpapi"
	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/alerts"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/call"
)

// Set at release time with -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "collegium:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		if len(args) == 1 && args[0] == "version" {
			fmt.Printf("%s (%s, %s)\n", version, commit, date)
			return nil
		}
		return errors.New("usage: collegium serve [flags] | version")
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	webhookAddr := fs.String("webhook-addr", "", "optional separate listener exposing only signed connector webhooks")
	authMode := fs.String("auth", "oidc", `who may call: "oidc", or "dev" for a loopback-only preview with no sign-in`)
	tenant := fs.String("tenant", "default", "tenant every caller is confined to, unless -tenant-claim is set")
	tenantClaim := fs.String("tenant-claim", "", "token claim that carries the caller's tenant")
	groupsClaim := fs.String("groups-claim", "groups", "token claim that lists a person's groups")
	adminGroup := fs.String("admin-group", "collegium-admins", "identity provider group granted the admin role")
	operatorGroup := fs.String("operator-group", "collegium-operators", "identity provider group granted the operator role")
	viewerGroup := fs.String("viewer-group", "collegium-viewers", "identity provider group granted the viewer role")
	publicURL := fs.String("public-url", "http://localhost:8080", "where browsers reach this server")
	webDir := fs.String("web", "", "directory of a built web console to serve")
	maxDepth := fs.Int("max-depth", 4, "deepest an agent-to-agent call may land")
	maxFanOut := fs.Int("max-fan-out", 16, "most calls one run may make")
	seed := fs.Bool("seed", false, "create two example agents that need no model key")
	builtinProvider := fs.String("builtin-provider", "", `model provider for the agents the platform provides, such as "anthropic" or "ollama". Empty means none are provided`)
	builtinModel := fs.String("builtin-model", "", "model for the agents the platform provides")
	agentSpecs := fs.String("agent-specs", "", "comma-separated agent JSON files to register on startup")
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
			Issuer:      os.Getenv("COLLEGIUM_OIDC_ISSUER"),
			ClientID:    os.Getenv("COLLEGIUM_OIDC_CLIENT_ID"),
			Audience:    os.Getenv("COLLEGIUM_OIDC_AUDIENCE"),
			Tenant:      *tenant,
			TenantClaim: *tenantClaim,
			GroupsClaim: *groupsClaim,
			GroupRoles: map[string]auth.Role{
				*adminGroup: auth.Admin, *operatorGroup: auth.Operator, *viewerGroup: auth.Viewer,
			},
		})
		if err != nil {
			return fmt.Errorf("%w (set COLLEGIUM_OIDC_ISSUER, COLLEGIUM_OIDC_CLIENT_ID, COLLEGIUM_OIDC_CLIENT_SECRET and COLLEGIUM_OIDC_AUDIENCE, or use -auth=dev locally)", err)
		}
		authn, err = httpapi.NewAuthn(v, os.Getenv("COLLEGIUM_OIDC_CLIENT_ID"), os.Getenv("COLLEGIUM_OIDC_CLIENT_SECRET"), *publicURL,
			[]string{"openid", "profile", "email", *groupsClaim})
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("-auth must be oidc or dev, not %q", *authMode)
	}

	p, err := platform.New(platform.Config{
		// A client may keep its secrets in this process's environment.
		Env:             os.LookupEnv,
		Limits:          call.Limits{MaxDepth: *maxDepth, MaxFanOut: *maxFanOut},
		Log:             log,
		BuiltinProvider: *builtinProvider,
		BuiltinModel:    *builtinModel,
	})
	if err != nil {
		return err
	}
	for _, path := range list(*agentSpecs) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("agent spec %s: %w", path, err)
		}
		var spec agentspec.AgentSpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			return fmt.Errorf("agent spec %s: %w", path, err)
		}
		if spec.Model == "YOUR_MODEL_ID" {
			spec.Model = os.Getenv("COLLEGIUM_AGENT_MODEL")
		}
		if spec.Model == "" {
			return fmt.Errorf("agent spec %s needs a model or COLLEGIUM_AGENT_MODEL", path)
		}
		if _, err := p.Agents.Create(ctx, *tenant, spec, "startup"); err != nil {
			return fmt.Errorf("agent spec %s: %w", path, err)
		}
	}
	if err := seedClients(ctx, p, *tenant, os.Getenv); err != nil {
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

	errc := make(chan error, 3)
	var ingress *http.Server
	if *webhookAddr != "" {
		ingress = &http.Server{Addr: *webhookAddr, Handler: p.IngressHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 16}
		go func() {
			log.Info("webhook ingress listening", "addr", *webhookAddr)
			if err := ingress.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	go func() { errc <- p.Run(ctx) }()
	if *seed {
		go func() {
			// After the platform is running, so the conversation the files
			// belong to is a real one.
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			if err := seedFiles(ctx, p, *tenant); err != nil {
				log.Warn("seed files", "error", err)
			}
		}()
	}
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
	if ingress != nil {
		_ = ingress.Shutdown(shutdown)
	}
	return err
}

// seedFiles publishes a few example files, so the storage page has something
// on it before any agent with a model has run.
func seedFiles(ctx context.Context, p *platform.Platform, tenant string) error {
	if p.Artifacts.Usage(tenant).Count > 0 {
		return nil
	}
	run, err := p.Converse(ctx, tenant, platform.Turn{
		ID: "seed-files", Agent: "lead", Text: "Draft the launch checklist", Project: "Launch",
		Actor: envelope.Actor{ID: "seed", Display: "Seed", Bot: true},
	})
	if err != nil {
		return err
	}
	for _, f := range []struct{ path, content, description string }{
		{"launch-checklist.md", "# Launch checklist\n\n- [ ] Freeze the release branch\n- [ ] Run the smoke tests\n", "The first draft"},
		{"launch-checklist.md", "# Launch checklist\n\n- [x] Freeze the release branch\n- [ ] Run the smoke tests\n- [ ] Tell support\n", "With support added"},
		{"signups.csv", "week,signups\n1,120\n2,164\n3,210\n", "Signups by week"},
	} {
		if err := p.PutFile(tenant, run.Thread, f.path, f.content); err != nil {
			return err
		}
		if _, err := p.Publish(ctx, platform.Publication{
			Tenant: tenant, Thread: run.Thread, Run: run.ID, Agent: "lead", Project: "Launch", By: "seed",
			Path: f.path, Description: f.description,
		}); err != nil {
			return err
		}
	}
	return nil
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

// seedAgents creates two agents on mandatum's offline provider, so the console
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
	// The lead answers when nobody picked an agent and none matched.
	if err := p.SetSettings(ctx, tenant, platform.Settings{
		Baseline: "lead",
		Projects: []platform.Project{{Name: "Launch", Description: "Everything for the Q4 launch"}},
	}); err != nil {
		return fmt.Errorf("seed baseline: %w", err)
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
