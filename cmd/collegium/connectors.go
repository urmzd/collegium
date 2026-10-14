package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/integrations/github"
	"github.com/urmzd/collegium/pkg/integrations/slack"
	"github.com/urmzd/mandatum/pkg/connector"
	"github.com/urmzd/mandatum/pkg/connectors/mention"
	"github.com/urmzd/mandatum/pkg/registry"
	"github.com/urmzd/mandatum/pkg/signature"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type connectionConfig struct{ Log *slog.Logger }

func connections(store registry.Store, tenant string, log *slog.Logger) ([]platform.Connection, error) {
	names := agentNames{store: store, tenant: tenant}
	var out []platform.Connection
	for _, build := range []func(connectionConfig, mention.Set) (connector.Connector, error){newGitHub, newSlack} {
		c, err := build(connectionConfig{Log: log}, names)
		if err != nil {
			return nil, err
		}
		if c == nil {
			continue
		}
		peer, ok := c.(platform.Peer)
		if !ok {
			return nil, fmt.Errorf("connector %s lacks required faces", c.Name())
		}
		out = append(out, platform.Connection{Tenant: tenant, Peer: peer})
	}
	return out, nil
}

const (
	// Slack. A signing secret is what makes the trigger safe; a bot token is
	// what makes the sink able to write.
	EnvSlackToken   = "COLLEGIUM_SLACK_TOKEN"
	EnvSlackSecret  = "COLLEGIUM_SLACK_SIGNING_SECRET"
	EnvSlackTeam    = "COLLEGIUM_SLACK_TEAM"
	EnvSlackBots    = "COLLEGIUM_SLACK_BOTS"
	EnvSlackDefault = "COLLEGIUM_SLACK_DEFAULT_AGENT"
	// Sessions is a boolean; Commands is a JSON array of
	// {"name","description","argument_hint"} objects.
	EnvSlackSessions = "COLLEGIUM_SLACK_SESSIONS"
	EnvSlackCommands = "COLLEGIUM_SLACK_COMMANDS"

	// GitHub.
	EnvGitHubReadRepositories = "COLLEGIUM_GITHUB_READ_REPOSITORIES"
	EnvGitHubToken            = "COLLEGIUM_GITHUB_TOKEN"
	EnvGitHubSecret           = "COLLEGIUM_GITHUB_WEBHOOK_SECRET"
	EnvGitHubOwners           = "COLLEGIUM_GITHUB_OWNERS"
	EnvGitHubBots             = "COLLEGIUM_GITHUB_BOTS"
	EnvGitHubAutoLabels       = "COLLEGIUM_GITHUB_AUTO_LABELS"
	EnvGitHubChecks           = "COLLEGIUM_GITHUB_CHECKS"
	EnvGitHubGuides           = "COLLEGIUM_GITHUB_GUIDES"
	EnvGitHubMutations        = "COLLEGIUM_GITHUB_MUTATIONS"
)

func newSlack(cfg connectionConfig, agents mention.Set) (connector.Connector, error) {
	token, secret := os.Getenv(EnvSlackToken), os.Getenv(EnvSlackSecret)
	if token == "" && secret == "" {
		return nil, nil
	}
	if token == "" || secret == "" {
		return nil, fmt.Errorf("slack needs both $%s and $%s", EnvSlackToken, EnvSlackSecret)
	}
	if os.Getenv(EnvSlackTeam) == "" {
		return nil, fmt.Errorf("slack installation needs $%s", EnvSlackTeam)
	}
	var sessions bool
	if raw := os.Getenv(EnvSlackSessions); raw != "" {
		var err error
		sessions, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid $%s: %v", EnvSlackSessions, err)
		}
	}
	var commands []slack.Command
	if raw := os.Getenv(EnvSlackCommands); raw != "" {
		if err := json.Unmarshal([]byte(raw), &commands); err != nil {
			return nil, fmt.Errorf("invalid $%s: %v", EnvSlackCommands, err)
		}
	}
	c, err := slack.New(slack.Config{
		API:          slack.NewHTTPAPI(token, http.DefaultClient),
		Verifier:     signature.NewSlack(secret),
		Team:         os.Getenv(EnvSlackTeam),
		Bots:         pairs(os.Getenv(EnvSlackBots)),
		Agents:       agents,
		DefaultAgent: os.Getenv(EnvSlackDefault),
		Sessions:     sessions,
		Commands:     commands,
		Log:          cfg.Log,
	})
	if err != nil {
		return nil, fmt.Errorf("cli: slack: %w", err)
	}
	return c, nil
}

func newGitHub(cfg connectionConfig, agents mention.Set) (connector.Connector, error) {
	token, secret := os.Getenv(EnvGitHubToken), os.Getenv(EnvGitHubSecret)
	if token == "" && secret == "" {
		return nil, nil
	}
	if token == "" || secret == "" {
		return nil, fmt.Errorf("github needs both $%s and $%s", EnvGitHubToken, EnvGitHubSecret)
	}
	if len(list(os.Getenv(EnvGitHubOwners))) == 0 {
		return nil, fmt.Errorf("GitHub installation needs $%s", EnvGitHubOwners)
	}
	var mutations bool
	if raw := os.Getenv(EnvGitHubMutations); raw != "" {
		var err error
		mutations, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid $%s: %v", EnvGitHubMutations, err)
		}
	}
	var guides bool
	if raw := os.Getenv(EnvGitHubGuides); raw != "" {
		var err error
		guides, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid $%s: %v", EnvGitHubGuides, err)
		}
	}
	var checks bool
	if raw := os.Getenv(EnvGitHubChecks); raw != "" {
		var err error
		checks, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid $%s: %v", EnvGitHubChecks, err)
		}
	}
	var autoLabels map[string]map[string]string
	if raw := os.Getenv(EnvGitHubAutoLabels); raw != "" {
		if err := json.Unmarshal([]byte(raw), &autoLabels); err != nil {
			return nil, fmt.Errorf("invalid $%s: %v", EnvGitHubAutoLabels, err)
		}
	}
	c, err := github.New(github.Config{
		API:              github.NewHTTPAPI(token, http.DefaultClient),
		Verifier:         signature.NewGitHub(secret),
		Owners:           list(os.Getenv(EnvGitHubOwners)),
		Bots:             pairs(os.Getenv(EnvGitHubBots)),
		Agents:           agents,
		AutoLabels:       autoLabels,
		Checks:           checks,
		Guides:           guides,
		Mutations:        mutations,
		MutationSecret:   []byte(secret),
		ReadRepositories: list(os.Getenv(EnvGitHubReadRepositories)),
	})
	if err != nil {
		return nil, fmt.Errorf("cli: github: %w", err)
	}
	return c, nil
}

type agentNames struct {
	store  registry.Store
	tenant string
}

func (a agentNames) Lookup(token string) (string, bool) {
	if !mention.Valid(token) {
		return "", false
	}
	rev, err := a.store.Get(context.Background(), a.tenant, token, 0)
	if err != nil {
		return "", false
	}
	return rev.Spec.Name, true
}

// pairs parses "k=v,k=v" into a map. Empty entries are skipped so that a
// trailing comma is not a configuration error.
func pairs(s string) map[string]string {
	out := map[string]string{}
	for _, entry := range list(s) {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// list splits a comma-separated environment value.
func list(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
