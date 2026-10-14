package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/urmzd/mandatum/pkg/connectors/mention"
	"github.com/urmzd/mandatum/pkg/registry"
	"github.com/urmzd/mandatum/pkg/signature"

	"github.com/urmzd/collegium/pkg/clients"
	"github.com/urmzd/collegium/pkg/integrations/github"
	"github.com/urmzd/collegium/pkg/integrations/slack"
	"github.com/urmzd/collegium/pkg/policy"
)

// ClientInput is a client as someone writes it: the record, and the two
// secrets. A secret left empty on an update keeps what is already there.
type ClientInput struct {
	clients.Client
	Token         clients.SecretInput
	SigningSecret clients.SecretInput
}

// ClientCaps is how many clients a tenant may have: the defaults, or what its
// own policy says.
func (p *Platform) ClientCaps(tenant string) clients.Caps {
	caps := clients.DefaultCaps()
	for _, pol := range p.Policies.For(tenant, "", "", "") {
		if pol.MaxClients > 0 {
			caps.Max = pol.MaxClients
		}
		if pol.MaxClientsPerSurface > 0 {
			caps.MaxPerSurface = pol.MaxClientsPerSurface
		}
	}
	return caps
}

// CreateClient adds a client to a tenant and starts answering through it.
//
// The connector is built before the record is kept, so a client whose options
// its surface would refuse, or whose secrets cannot be found, is never stored.
func (p *Platform) CreateClient(_ context.Context, tenant, by string, in ClientInput) (clients.Client, error) {
	p.clientsMu.Lock()
	defer p.clientsMu.Unlock()

	c := in.Client
	if c.ID == "" {
		c.ID = clients.Slug(c.Name)
	}
	c.Tenant, c.CreatedBy, c.Version = tenant, by, 0
	if c.Managed == clients.ManagedStatic {
		return clients.Client{}, fmt.Errorf("%w: a static client is not created", clients.ErrInvalid)
	}
	if err := c.Validate(); err != nil {
		return clients.Client{}, err
	}
	if s := p.surfaces[string(c.Surface)]; s.seat(tenant, c.ID) != nil {
		return clients.Client{}, fmt.Errorf("%w: %q", clients.ErrExists, c.ID)
	}
	creds, secrets, err := p.credentials(tenant, c.ID, clients.Credentials{}, in)
	if err != nil {
		return clients.Client{}, err
	}
	c.Credentials = creds
	st, err := p.dial(c, secrets)
	if err != nil {
		return clients.Client{}, err
	}
	saved, err := p.Clients.Create(tenant, c, p.ClientCaps(tenant), p.now())
	if err != nil {
		return clients.Client{}, err
	}
	p.keep(saved, in)
	p.seat(saved, st)
	p.log.Info("platform: client created", "tenant", tenant, "client", saved.ID, "surface", saved.Surface, "by", by)
	return saved, nil
}

// UpdateClient replaces a client's record. Its connector is rebuilt, so the
// runs it was carrying lose their place on the surface: a Slack session's stop
// button no longer finds them, and an answer still streaming starts a new
// message.
func (p *Platform) UpdateClient(_ context.Context, tenant, id, by string, in ClientInput) (clients.Client, error) {
	p.clientsMu.Lock()
	defer p.clientsMu.Unlock()

	old, err := p.Clients.Get(tenant, id)
	if err != nil {
		return clients.Client{}, err
	}
	if old.Managed != "" {
		return clients.Client{}, clients.ErrManaged
	}
	c := in.Client
	c.ID, c.Tenant, c.Managed = id, tenant, ""
	if c.Surface == "" {
		c.Surface = old.Surface
	}
	if err := c.Validate(); err != nil {
		return clients.Client{}, err
	}
	creds, secrets, err := p.credentials(tenant, id, old.Credentials, in)
	if err != nil {
		return clients.Client{}, err
	}
	c.Credentials = creds
	st, err := p.dial(c, secrets)
	if err != nil {
		return clients.Client{}, err
	}
	saved, err := p.Clients.Update(tenant, c, p.now())
	if err != nil {
		return clients.Client{}, err
	}
	p.keep(saved, in)
	p.seat(saved, st)
	p.log.Info("platform: client updated", "tenant", tenant, "client", id, "version", saved.Version, "enabled", saved.Enabled, "by", by)
	return saved, nil
}

// DeleteClient removes a client, its secrets and its limits. Runs it raised
// keep their record of it.
func (p *Platform) DeleteClient(_ context.Context, tenant, id, by string) error {
	p.clientsMu.Lock()
	defer p.clientsMu.Unlock()

	old, err := p.Clients.Get(tenant, id)
	if err != nil {
		return err
	}
	if old.Managed != "" {
		return clients.ErrManaged
	}
	if err := p.Clients.Delete(tenant, id); err != nil {
		return err
	}
	p.surfaces[string(old.Surface)].remove(tenant, id)
	p.secrets.Drop(tenant, id)
	if err := p.Policies.Delete(tenant, policy.Scope{Kind: policy.Client, Name: id}, by, p.now()); err != nil && !errors.Is(err, policy.ErrNotFound) {
		return err
	}
	p.log.Info("platform: client deleted", "tenant", tenant, "client", id, "by", by)
	return nil
}

// resolved is a client's two secrets, in hand only while its connector is
// being built.
type resolved struct{ token, signing string }

// credentials settles where a client's secrets are and reads them. An empty
// input keeps what the client had.
func (p *Platform) credentials(tenant, id string, had clients.Credentials, in ClientInput) (clients.Credentials, resolved, error) {
	one := func(name, what string, had clients.SecretRef, in clients.SecretInput) (clients.SecretRef, string, error) {
		switch {
		case in.Value != "" && in.Ref != "":
			return clients.SecretRef{}, "", fmt.Errorf("%w: give the %s as a value or a reference, not both", clients.ErrInvalid, what)
		case in.Value != "":
			return clients.SecretRef{Set: true}, in.Value, nil
		case in.Ref != "":
			had = clients.SecretRef{Set: true, Ref: in.Ref}
		case !had.Set:
			return clients.SecretRef{}, "", fmt.Errorf("%w: a client needs a %s", clients.ErrInvalid, what)
		}
		if had.Ref == "" {
			v, ok := p.secrets.Get(tenant, id, name)
			if !ok || v == "" {
				return clients.SecretRef{}, "", fmt.Errorf("%w: the %s is no longer held and has to be written again", clients.ErrInvalid, what)
			}
			return had, v, nil
		}
		env, err := clients.EnvRef(had.Ref)
		if err != nil {
			return clients.SecretRef{}, "", err
		}
		var v string
		if p.cfg.Env != nil {
			v, _ = p.cfg.Env(env)
		}
		if v == "" {
			return clients.SecretRef{}, "", fmt.Errorf("%w: %s is not set in the server's environment", clients.ErrInvalid, had.Ref)
		}
		return had, v, nil
	}
	var creds clients.Credentials
	var secrets resolved
	var err error
	if creds.Token, secrets.token, err = one(clients.SecretToken, "token", had.Token, in.Token); err != nil {
		return clients.Credentials{}, resolved{}, err
	}
	if creds.SigningSecret, secrets.signing, err = one(clients.SecretSigning, "signing secret", had.SigningSecret, in.SigningSecret); err != nil {
		return clients.Credentials{}, resolved{}, err
	}
	return creds, secrets, nil
}

// keep stores the secret values that were written, and forgets a stored value
// that a reference has replaced.
func (p *Platform) keep(c clients.Client, in ClientInput) {
	for name, pair := range map[string]struct {
		ref clients.SecretRef
		in  clients.SecretInput
	}{
		clients.SecretToken:   {c.Credentials.Token, in.Token},
		clients.SecretSigning: {c.Credentials.SigningSecret, in.SigningSecret},
	} {
		switch {
		case pair.in.Value != "":
			p.secrets.Put(c.Tenant, c.ID, name, pair.in.Value)
		case pair.ref.Ref != "":
			p.secrets.Put(c.Tenant, c.ID, name, "")
		}
	}
}

// seat puts a client's connector on its surface, in place of the one it had.
// A disabled client is unseated: it answers nothing and carries nothing.
func (p *Platform) seat(c clients.Client, st *seat) {
	s := p.surfaces[string(c.Surface)]
	if !c.Enabled {
		s.remove(c.Tenant, c.ID)
		return
	}
	st.client = c
	p.bind(s, st)
	s.put(st)
}

// bind gives a client's connector the way to stop a run. It can stop only runs
// its own client raised, in its own tenant.
func (p *Platform) bind(s *surface, st *seat) {
	stopper, ok := st.peer.(surfaceStopper)
	if !ok {
		return
	}
	tenant, client, name := st.client.Tenant, st.client.ID, s.name
	stopper.OnStop(func(ctx context.Context, runID, who string) error {
		return p.stopFromSurface(ctx, tenant, client, name, runID, who)
	})
}

// dial builds the connector for a client. Nothing is started.
func (p *Platform) dial(c clients.Client, secrets resolved) (*seat, error) {
	agents := clientAgents{store: p.Agents, client: c}
	refused := func(err error) error { return fmt.Errorf("%w: %v", clients.ErrInvalid, err) }
	switch c.Surface {
	case clients.Slack:
		var api slack.API = slack.NewHTTPAPI(secrets.token, http.DefaultClient)
		if p.cfg.SlackAPI != nil {
			api = p.cfg.SlackAPI(secrets.token)
		}
		cfg := slack.Config{
			API: api, Verifier: signature.NewSlack(secrets.signing), Team: c.Identity.Team,
			Bots: c.Bots, Agents: agents, DefaultAgent: c.DefaultAgent,
			Interval: p.cfg.ClientInterval, Log: p.log,
		}
		if c.Slack != nil {
			cfg.Sessions = c.Slack.Sessions
			for _, cmd := range c.Slack.Commands {
				cfg.Commands = append(cfg.Commands, slack.Command{Name: cmd.Name, Description: cmd.Description, ArgumentHint: cmd.ArgumentHint})
			}
		}
		peer, err := slack.New(cfg)
		if err != nil {
			return nil, refused(err)
		}
		return &seat{client: c, peer: peer, verifier: cfg.Verifier}, nil
	case clients.GitHub:
		var api github.API = github.NewHTTPAPI(secrets.token, http.DefaultClient)
		if p.cfg.GitHubAPI != nil {
			api = p.cfg.GitHubAPI(secrets.token)
		}
		cfg := github.Config{
			API: api, Verifier: signature.NewGitHub(secrets.signing), Owners: c.Identity.Owners,
			Bots: c.Bots, Agents: agents, Interval: p.cfg.ClientInterval,
			// A fix is authorized by a grant signed with the client's own
			// webhook secret, so one client's grant is worth nothing to another.
			MutationSecret: []byte(secrets.signing),
		}
		if g := c.GitHub; g != nil {
			cfg.AutoLabels, cfg.Checks, cfg.Guides, cfg.Mutations, cfg.ReadRepositories = g.AutoLabels, g.Checks, g.Guides, g.Mutations, g.ReadRepositories
		}
		peer, err := github.New(cfg)
		if err != nil {
			return nil, refused(err)
		}
		return &seat{client: c, peer: peer, verifier: cfg.Verifier}, nil
	}
	return nil, fmt.Errorf("%w: unknown surface %q", clients.ErrInvalid, c.Surface)
}

// clientAgents is the agents a client may name in text: those in its tenant's
// registry that it may route to.
type clientAgents struct {
	store  registry.Store
	client clients.Client
}

func (a clientAgents) Lookup(token string) (string, bool) {
	if !mention.Valid(token) {
		return "", false
	}
	rev, err := a.store.Get(context.Background(), a.client.Tenant, token, 0)
	if err != nil || !a.client.Routes(rev.Spec.Name) {
		return "", false
	}
	return rev.Spec.Name, true
}

// restore seats every enabled client a store already holds. One that cannot be
// built is left unseated and said so: it answers nothing until it is fixed.
func (p *Platform) restore() {
	for _, surface := range clients.Surfaces() {
		for _, c := range p.Clients.On(surface) {
			_, secrets, err := p.credentials(c.Tenant, c.ID, c.Credentials, ClientInput{})
			var st *seat
			if err == nil {
				st, err = p.dial(c, secrets)
			}
			if err == nil {
				p.seat(c, st)
			} else {
				p.log.Error("platform: client could not be started", "tenant", c.Tenant, "client", c.ID, "surface", c.Surface, "error", err)
			}
		}
	}
}

// static seats a connector the embedding program built. It may route to any
// agent in its tenant and verifies its own requests.
func (p *Platform) static(conn Connection) error {
	if conn.Tenant == "" || conn.Peer == nil {
		return fmt.Errorf("platform: connection needs tenant and peer")
	}
	s, ok := p.surfaces[conn.Peer.Name()]
	if !ok {
		return fmt.Errorf("platform: no surface for connector %q", conn.Peer.Name())
	}
	id := "static-" + s.name
	if s.seat(conn.Tenant, id) != nil {
		return fmt.Errorf("platform: tenant %q already has a static %s connection", conn.Tenant, s.name)
	}
	st := &seat{peer: conn.Peer, client: clients.Client{
		ID: id, Tenant: conn.Tenant, Name: s.name, Surface: clients.Surface(s.name),
		Agents: []string{clients.AnyAgent}, Enabled: true, Managed: clients.ManagedStatic,
	}}
	p.bind(s, st)
	s.put(st)
	return nil
}
