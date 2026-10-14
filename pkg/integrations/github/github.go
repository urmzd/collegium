// Package github composes transport capabilities with review and fix policy.
// API calls stay in mandatum; presentation is supplied by vultus.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/urmzd/mandatum/pkg/connector"
	transport "github.com/urmzd/mandatum/pkg/connectors/github"
	"github.com/urmzd/mandatum/pkg/connectors/mention"
	"github.com/urmzd/mandatum/pkg/envelope"
	presentation "github.com/urmzd/vultus/go/github"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config combines transport configuration with explicit product policy.
type Config struct {
	API              transport.API
	Verifier         transport.Verifier
	Owners           []string
	ReadRepositories []string
	Bots             map[string]string
	Agents           mention.Set
	Checks           bool
	Deliver          []envelope.Route
	Interval         time.Duration
	Clock            func() time.Time
	MaxBody          int64
	AutoLabels       map[string]map[string]string
	Guides           bool
	Mutations        bool
	MutationSecret   []byte
	// Tenant is the workspace whose runs this connector's tags become. A fix
	// grant names the run it is good for, and the runtime derives a run's id
	// from the tenant and the tag id together, so the grant cannot be signed
	// without it. Mutations without a Tenant are refused by New.
	Tenant string
}

// Connector decorates the transport without introducing another execution path.
type Connector struct {
	*transport.Connector
	cfg     Config
	guideMu sync.Mutex
}

func New(cfg Config) (*Connector, error) {
	c := &Connector{cfg: cfg}
	c.cfg.MutationSecret = append([]byte(nil), cfg.MutationSecret...)
	c.cfg.AutoLabels = map[string]map[string]string{}
	for repo, labels := range cfg.AutoLabels {
		if len(strings.Split(repo, "/")) != 2 {
			return nil, fmt.Errorf("github: invalid auto-label repository")
		}
		key := strings.ToLower(repo)
		if _, ok := c.cfg.AutoLabels[key]; ok {
			return nil, fmt.Errorf("github: duplicate repository")
		}
		c.cfg.AutoLabels[key] = map[string]string{}
		for label, agent := range labels {
			if strings.TrimSpace(label) == "" || !mention.Valid(agent) {
				return nil, fmt.Errorf("github: invalid auto label or agent")
			}
			c.cfg.AutoLabels[key][label] = agent
		}
	}
	if cfg.Guides || cfg.Mutations {
		if _, ok := cfg.API.(transport.ContextAPI); !ok {
			return nil, fmt.Errorf("github: policy needs ContextAPI")
		}
	}
	if cfg.Guides {
		if _, ok := cfg.API.(transport.GuideAPI); !ok || len(cfg.Bots) == 0 {
			return nil, fmt.Errorf("github: guides need GuideAPI and bot mapping")
		}
	}
	if cfg.Mutations {
		if _, ok := cfg.API.(transport.MutationAPI); !ok || len(cfg.MutationSecret) < 16 {
			return nil, fmt.Errorf("github: mutations need MutationAPI and signing secret of at least 16 bytes")
		}
		if strings.TrimSpace(cfg.Tenant) == "" {
			return nil, fmt.Errorf("github: mutations need the tenant the connector's runs belong to")
		}
	}
	base := transport.Config{API: cfg.API, Verifier: cfg.Verifier, Owners: cfg.Owners, ReadRepositories: cfg.ReadRepositories, Bots: cfg.Bots, Agents: cfg.Agents, Checks: cfg.Checks, Deliver: cfg.Deliver, Interval: cfg.Interval, Clock: cfg.Clock, MaxBody: cfg.MaxBody}
	base.Automation = c.automatic
	base.Prepare = c.prepare
	base.Render = presentation.RenderReview
	base.Conclusion = conclusion
	var err error
	c.Connector, err = transport.New(base)
	if err != nil {
		return nil, err
	}
	validator, err := transport.New(transport.Config{API: cfg.API})
	if err != nil {
		return nil, err
	}
	for repo := range c.cfg.AutoLabels {
		parts := strings.Split(repo, "/")
		if _, err := validator.Reference(refAddress(Ref{Owner: parts[0], Repo: parts[1], Number: 1}, "pull")); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Connector) Actions() []connector.Action {
	actions := c.Connector.Actions()
	if c.cfg.Mutations {
		actions = append(actions, c.mutationAction(), c.runChecksAction())
	}
	return actions
}

func (c *Connector) prepare(ctx context.Context, tag *envelope.Tag) error {
	if tag.Source.Path[1] == "pull" {
		if c.cfg.Guides {
			if err := c.prepareGuide(ctx, tag); err != nil {
				return err
			}
		}
		if c.cfg.Mutations && tag.Source.Params["head_sha"] == "" {
			ref, err := c.Reference(tag.Source)
			if err != nil {
				return err
			}
			evidence, err := c.cfg.API.(ContextAPI).ReadContext(ctx, ref, 1)
			if err != nil {
				return err
			}
			if !transport.ValidSHA(evidence.Subject.Head.SHA) {
				return fmt.Errorf("github: missing pinned head")
			}
			tag.Source = tag.Source.WithParam("head_sha", evidence.Subject.Head.SHA)
			tag.Meta["github_head_sha"] = evidence.Subject.Head.SHA
		}
	}
	return c.grantMutation(ctx, tag)
}

func (c *Connector) automatic(h transport.Hook) (string, string, bool) {
	labels := c.cfg.AutoLabels[strings.ToLower(h.Repository.Owner.Login+"/"+h.Repository.Name)]
	title, body, sha := "", "", ""
	var names []string
	if h.PullRequest != nil {
		title, body, sha = h.PullRequest.Title, h.PullRequest.Body, h.PullRequest.Head.SHA
		for _, l := range h.PullRequest.Labels {
			names = append(names, l.Name)
		}
	} else if h.Issue != nil {
		title, body = h.Issue.Title, h.Issue.Body
	}
	if h.Action == "labeled" && h.Label != nil {
		names = []string{h.Label.Name}
	} else if h.Action != "synchronize" {
		return "", "", false
	}
	sort.Strings(names)
	for _, name := range names {
		if agent, ok := labels[name]; ok {
			text := strings.TrimSpace(title + "\n\n" + body)
			if h.Action == "synchronize" {
				text = "Review commit " + sha + ".\n\n" + text
			}
			return agent, text, true
		}
	}
	return "", "", false
}

func conclusion(v transport.View) string {
	var r presentation.ReviewReport
	if json.Unmarshal([]byte(v.Text), &r) == nil && r.Format == presentation.ReviewFormat {
		if _, err := presentation.FormatReview(r); err != nil {
			return "action_required"
		}
		for _, f := range r.Findings {
			if f.Severity == "error" {
				return "failure"
			}
		}
	} else if strings.Contains(v.Text, presentation.ReviewFormat) {
		return "action_required"
	}
	return "neutral"
}
