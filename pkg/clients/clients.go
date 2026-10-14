// Package clients models the external clients a workspace answers through.
//
// A client is one installation of the platform on a surface: a Slack app in a
// team, or a GitHub App installation on some accounts. A workspace may have
// one, or several that route to different agents, down to one per agent.
//
// A client is a record and nothing else. It names the external identity it
// answers for, where its credentials are, which agents it may raise runs for,
// and the options of its surface. This package validates the record and keeps
// it; building a connector from it is the composition root's job.
//
// # Default deny
//
// A client routes to its default agent and to the agents in its allow-list.
// An empty allow-list with no default agent routes to nothing. The single
// entry "*" means any agent in the workspace, and has to be written.
//
// # Secrets
//
// A record never holds a secret. Credentials is two references: each says
// whether a secret is set and, when the secret lives in the server's
// environment, under which name. Values are written through SecretInput, kept
// in a Secrets store beside the records, and have no field on anything this
// package returns.
package clients

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Errors.
var (
	ErrInvalid  = errors.New("clients: invalid")
	ErrNotFound = errors.New("clients: not found")
	ErrExists   = errors.New("clients: already exists")
	// ErrLimit reports a workspace that already has as many clients as it may.
	ErrLimit = errors.New("clients: limit reached")
	// ErrManaged reports a change to a client the server's configuration owns.
	ErrManaged = errors.New("clients: managed by the server's configuration")
)

// Surface is where a client answers.
type Surface string

const (
	Slack  Surface = "slack"
	GitHub Surface = "github"
)

// Surfaces lists every surface a client can be on.
func Surfaces() []Surface { return []Surface{GitHub, Slack} }

// AnyAgent in an allow-list means every agent in the workspace.
const AnyAgent = "*"

// How many clients a workspace may have when its policy sets no number, and
// the most a policy may allow. Ceiling also bounds every list of clients.
const (
	DefaultMax           = 10
	DefaultMaxPerSurface = 5
	Ceiling              = 100
)

// Bounds on one record.
const (
	maxName     = 80
	maxAgents   = 64
	maxBots     = 64
	maxOwners   = 32
	maxRepos    = 64
	maxCommands = 10
	maxLabels   = 32
)

// Managed says who owns a client that the API cannot change.
const (
	// ManagedEnv is the default client built from the server's environment.
	ManagedEnv = "env"
	// ManagedStatic is a connector the embedding program built itself.
	ManagedStatic = "static"
)

// Identity is the external identity a client answers for. An inbound request
// is offered to a client only when every field the request states agrees with
// it, and is believed only once the client's own secret has verified it.
type Identity struct {
	// AppID is the Slack app id ("A0123"), or the GitHub App id.
	AppID string `json:"app_id,omitempty"`
	// Team is the Slack team id ("T0123"). Required for Slack.
	Team string `json:"team,omitempty"`
	// Installation is the GitHub App installation id.
	Installation string `json:"installation,omitempty"`
	// Owners are the GitHub accounts served. Required for GitHub.
	Owners []string `json:"owners,omitempty"`
}

// SecretRef says where one secret is, never what it is.
type SecretRef struct {
	// Set is true once a secret has been written or referenced.
	Set bool `json:"set"`
	// Ref names a secret held outside the store, as "env:NAME".
	Ref string `json:"ref,omitempty"`
}

// Credentials are the two secrets a surface needs: a token to write with and
// a secret to verify inbound requests with.
type Credentials struct {
	Token         SecretRef `json:"token"`
	SigningSecret SecretRef `json:"signing_secret"`
}

// Names of the secrets a client holds, as a Secrets store keys them.
const (
	SecretToken   = "token"
	SecretSigning = "signing_secret"
)

// SecretInput writes one secret: a value, or a reference to one held in the
// server's environment. It is accepted and never returned.
type SecretInput struct {
	Value string `json:"value,omitempty"`
	Ref   string `json:"ref,omitempty"`
}

// Empty reports an input that changes nothing.
func (s SecretInput) Empty() bool { return s.Value == "" && s.Ref == "" }

// String keeps a value out of anything that formats the input.
func (s SecretInput) String() string {
	if s.Ref != "" {
		return s.Ref
	}
	if s.Value != "" {
		return "[secret]"
	}
	return ""
}

// GoString keeps a value out of %#v.
func (s SecretInput) GoString() string { return s.String() }

// EnvPrefix is what the name in an "env:NAME" reference must start with. A
// reference is sent to the surface as a credential, so it may not name an
// arbitrary variable of the server's, such as a model key.
const EnvPrefix = "COLLEGIUM_"

var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// EnvRef returns the variable an "env:NAME" reference names.
func EnvRef(ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, "env:")
	if !ok {
		return "", fmt.Errorf("%w: a secret reference is written env:NAME", ErrInvalid)
	}
	if !envName.MatchString(name) || !strings.HasPrefix(name, EnvPrefix) {
		return "", fmt.Errorf("%w: a secret reference names a variable starting with %s", ErrInvalid, EnvPrefix)
	}
	return name, nil
}

// Command is a slash command a Slack client offers in code channels.
type Command struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argument_hint,omitempty"`
}

// SlackOptions are the options of a Slack client.
type SlackOptions struct {
	// Sessions reports run lifecycle as agent-session status.
	Sessions bool      `json:"sessions,omitempty"`
	Commands []Command `json:"commands,omitempty"`
}

// GitHubOptions are the options of a GitHub client.
type GitHubOptions struct {
	// AutoLabels maps "owner/repo" to label to agent.
	AutoLabels map[string]map[string]string `json:"auto_labels,omitempty"`
	Checks     bool                         `json:"checks,omitempty"`
	Guides     bool                         `json:"guides,omitempty"`
	Mutations  bool                         `json:"mutations,omitempty"`
	// ReadRepositories are "owner/repo" names a run from another surface may
	// read through this client.
	ReadRepositories []string `json:"read_repositories,omitempty"`
}

// Client is one installation of the platform on a surface.
type Client struct {
	// ID names the client within its workspace and is what a run records.
	ID      string  `json:"id"`
	Tenant  string  `json:"-"`
	Name    string  `json:"name"`
	Surface Surface `json:"surface"`

	Identity    Identity    `json:"identity"`
	Credentials Credentials `json:"credentials"`

	// DefaultAgent answers a request that named no agent.
	DefaultAgent string `json:"default_agent,omitempty"`
	// Agents is the allow-list of agents the client may route to, besides its
	// default. Empty means the default only. "*" alone means any agent.
	Agents []string `json:"agents,omitempty"`
	// Bots maps an external bot identity to the agent it stands for: a Slack
	// bot user id, or a GitHub login.
	Bots map[string]string `json:"bots,omitempty"`

	Slack  *SlackOptions  `json:"slack,omitempty"`
	GitHub *GitHubOptions `json:"github,omitempty"`

	// Enabled is false for a client that exists and answers nothing.
	Enabled bool `json:"enabled"`
	// Managed is set on a client the API cannot change.
	Managed string `json:"managed,omitempty"`

	// Version counts changes, from 1.
	Version   int       `json:"version"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Routes reports whether the client may raise or carry a run of an agent.
func (c Client) Routes(agent string) bool {
	if slices.Contains(c.Agents, AnyAgent) {
		return true
	}
	return agent != "" && (agent == c.DefaultAgent || slices.Contains(c.Agents, agent))
}

// Serves reports whether an address workspace on the client's surface is one
// it answers for: its Slack team, or one of its GitHub owners.
func (c Client) Serves(workspace string) bool {
	if c.Surface == Slack {
		return c.Identity.Team != "" && c.Identity.Team == workspace
	}
	return slices.ContainsFunc(c.Identity.Owners, func(o string) bool { return strings.EqualFold(o, workspace) })
}

// Claim is the identity an inbound request states about itself before it has
// been verified. It selects which clients are offered the request and is
// trusted for nothing else. An empty field means the request did not say.
type Claim struct {
	AppID        string
	Team         string
	Installation string
	Owner        string
}

// Claims reports whether a request stating claim could be for this client:
// nothing it states disagrees with the client's identity.
func (c Client) Claims(claim Claim) bool {
	agree := func(have, said string) bool { return have == "" || said == "" || have == said }
	if !agree(c.Identity.AppID, claim.AppID) || !agree(c.Identity.Installation, claim.Installation) {
		return false
	}
	if c.Surface == Slack {
		return agree(c.Identity.Team, claim.Team)
	}
	return claim.Owner == "" || len(c.Identity.Owners) == 0 || c.Serves(claim.Owner)
}

// overlaps reports two clients that could both be offered the same request,
// which would leave it with no single client to go to. An identity field left
// empty matches anything, so two Slack apps in one team must each say which
// app they are.
func (c Client) overlaps(o Client) bool {
	either := func(a, b string) bool { return a == "" || b == "" || a == b }
	if c.Surface != o.Surface || !either(c.Identity.AppID, o.Identity.AppID) {
		return false
	}
	if c.Surface == Slack {
		return c.Identity.Team == o.Identity.Team
	}
	if !either(c.Identity.Installation, o.Identity.Installation) {
		return false
	}
	return slices.ContainsFunc(c.Identity.Owners, o.Serves)
}

var (
	idPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	agentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	extPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\[\]-]{0,99}$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_.-]{1,100}$`)
	digits       = regexp.MustCompile(`^[0-9]{1,20}$`)
	slackID      = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,31}$`)
	commandName  = regexp.MustCompile(`^[^\s/][^\s]{0,30}$`)
)

// Slug makes an id out of a name: "Docs Bot" becomes "docs-bot".
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// Validate checks a record on its own. It does not look at credentials: those
// are checked where they are written.
func (c Client) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
	}
	switch {
	case !idPattern.MatchString(c.ID):
		return bad("an id is 1 to 40 lowercase letters, digits and dashes")
	case strings.TrimSpace(c.Name) == "" || len(c.Name) > maxName:
		return bad("a client has a name of at most %d characters", maxName)
	case c.Surface != Slack && c.Surface != GitHub:
		return bad("the surface is slack or github")
	case c.DefaultAgent != "" && !agentPattern.MatchString(c.DefaultAgent):
		return bad("the default agent %q is not an agent name", c.DefaultAgent)
	case len(c.Agents) > maxAgents:
		return bad("a client names at most %d agents", maxAgents)
	case len(c.Bots) > maxBots:
		return bad("a client maps at most %d bots", maxBots)
	}
	if slices.Contains(c.Agents, AnyAgent) && len(c.Agents) != 1 {
		return bad("%q stands alone in the list of agents", AnyAgent)
	}
	for i, a := range c.Agents {
		if a != AnyAgent && !agentPattern.MatchString(a) {
			return bad("%q is not an agent name", a)
		}
		if slices.Contains(c.Agents[:i], a) {
			return bad("agent %q is listed twice", a)
		}
	}
	for ext, agent := range c.Bots {
		if !extPattern.MatchString(ext) {
			return bad("%q is not a bot identity", ext)
		}
		if !c.Routes(agent) {
			return bad("bot %q stands for %q, which this client may not route to", ext, agent)
		}
	}
	id := c.Identity
	if id.AppID != "" && !extPattern.MatchString(id.AppID) {
		return bad("the app id is not an identifier")
	}
	if c.Surface == Slack {
		return c.validateSlack()
	}
	return c.validateGitHub()
}

func (c Client) validateSlack() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
	}
	id := c.Identity
	switch {
	case c.GitHub != nil:
		return bad("a slack client has no github options")
	case !slackID.MatchString(id.Team):
		return bad("a slack client names the team id it serves")
	case id.Installation != "" || len(id.Owners) > 0:
		return bad("a slack client has no installation or owners")
	}
	if c.Slack == nil {
		return nil
	}
	if len(c.Slack.Commands) > maxCommands {
		return bad("slack allows %d commands in a channel", maxCommands)
	}
	seen := map[string]bool{}
	for _, cmd := range c.Slack.Commands {
		switch {
		case !commandName.MatchString(cmd.Name):
			return bad("command name %q must be 1 to 31 characters with no leading slash and no whitespace", cmd.Name)
		case strings.TrimSpace(cmd.Description) == "":
			return bad("command %q needs a description", cmd.Name)
		case seen[cmd.Name]:
			return bad("command %q is configured twice", cmd.Name)
		}
		seen[cmd.Name] = true
	}
	return nil
}

func (c Client) validateGitHub() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
	}
	id := c.Identity
	switch {
	case c.Slack != nil:
		return bad("a github client has no slack options")
	case id.Team != "":
		return bad("a github client has no team")
	case len(id.Owners) == 0 || len(id.Owners) > maxOwners:
		return bad("a github client names 1 to %d accounts it serves", maxOwners)
	case id.Installation != "" && !digits.MatchString(id.Installation):
		return bad("the installation id is a number")
	}
	for i, o := range id.Owners {
		if !extPattern.MatchString(o) {
			return bad("%q is not a github account", o)
		}
		if slices.ContainsFunc(id.Owners[:i], func(x string) bool { return strings.EqualFold(x, o) }) {
			return bad("account %q is listed twice", o)
		}
	}
	if c.GitHub == nil {
		return nil
	}
	g := c.GitHub
	if len(g.ReadRepositories) > maxRepos || len(g.AutoLabels) > maxRepos {
		return bad("a client names at most %d repositories", maxRepos)
	}
	for _, repo := range g.ReadRepositories {
		if !repoPattern.MatchString(repo) {
			return bad("%q is not an owner/repo name", repo)
		}
	}
	for repo, labels := range g.AutoLabels {
		if !repoPattern.MatchString(repo) {
			return bad("%q is not an owner/repo name", repo)
		}
		if owner, _, _ := strings.Cut(repo, "/"); !c.Serves(owner) {
			return bad("auto-labels name %q, which is outside the accounts this client serves", repo)
		}
		if len(labels) > maxLabels {
			return bad("a repository maps at most %d labels", maxLabels)
		}
		for label, agent := range labels {
			if strings.TrimSpace(label) == "" {
				return bad("an auto-label has a name")
			}
			if !c.Routes(agent) {
				return bad("label %q starts %q, which this client may not route to", label, agent)
			}
		}
	}
	if g.Guides && len(c.Bots) == 0 {
		return bad("guides need the bot login mapped to an agent")
	}
	return nil
}

// clone returns a copy that shares nothing with the record it was made from.
func (c Client) clone() Client {
	c.Identity.Owners = slices.Clone(c.Identity.Owners)
	c.Agents = slices.Clone(c.Agents)
	if c.Bots != nil {
		bots := make(map[string]string, len(c.Bots))
		for k, v := range c.Bots {
			bots[k] = v
		}
		c.Bots = bots
	}
	if c.Slack != nil {
		s := *c.Slack
		s.Commands = slices.Clone(s.Commands)
		c.Slack = &s
	}
	if c.GitHub != nil {
		g := *c.GitHub
		g.ReadRepositories = slices.Clone(g.ReadRepositories)
		if g.AutoLabels != nil {
			labels := make(map[string]map[string]string, len(g.AutoLabels))
			for repo, m := range g.AutoLabels {
				inner := make(map[string]string, len(m))
				for k, v := range m {
					inner[k] = v
				}
				labels[repo] = inner
			}
			g.AutoLabels = labels
		}
		c.GitHub = &g
	}
	return c
}
