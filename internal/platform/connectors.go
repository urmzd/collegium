package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/urmzd/mandatum/pkg/address"
	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/connector"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/runtime"

	"github.com/urmzd/collegium/pkg/clients"
	"github.com/urmzd/collegium/pkg/runs"
)

// Clients on a surface.
//
// mandatum's connector registry holds one connector per address scheme, and a
// workspace may have several Slack apps or GitHub installations, as may every
// other workspace in the process. So the platform registers one surface per
// scheme and keeps the clients behind it:
//
//	webhook  →  surface  →  the client whose secret verifies it  →  tag
//	event    →  surface  →  the client that raised the run        →  Slack, GitHub
//
// A surface never decides who a request is from. It offers the request to the
// clients whose identity the request states, and the first whose own secret
// verifies the bytes takes it. The tenant, the client and the agents it may
// reach all come from that client's record, never from the payload.

// Peer is one client's connector: a trigger, a sink and an actor behind a
// signature-verifying HTTP handler.
type Peer interface {
	connector.Trigger
	connector.Sink
	connector.Actor
	http.Handler
	Ingesting() bool
	Flush(context.Context) error
}

// Connection binds a connector the embedding program built itself to one
// tenant. It is a client the API does not manage: it may route to any agent in
// its tenant, answers every request on its surface that no managed client's
// secret verified, and verifies that request itself. A program that wants
// several clients on a surface creates them through CreateClient.
type Connection struct {
	Tenant string
	Peer   Peer
}

// surfaceStopper is a peer whose surface has a stop control. The platform
// gives it the way to stop a run, bound to the client that owns the session.
type surfaceStopper interface {
	OnStop(func(ctx context.Context, runID, who string) error)
}

// verifier authenticates a raw request body against its headers.
type verifier interface {
	Verify(header http.Header, body []byte) error
}

// maxWebhookBody bounds a request before anything has verified it. It matches
// mandatum's own bound, which the client's connector applies again.
const maxWebhookBody = 4 << 20

// seat is one client on a surface: its record as it was when the connector was
// built, and the connector.
type seat struct {
	client clients.Client
	peer   Peer
	// verifier is nil for a Connection, which verifies inside its handler.
	verifier verifier

	cancel context.CancelFunc
	done   chan struct{}
}

// surface is every client of one address scheme, in every tenant. It is the
// one connector mandatum's registry and router see for that scheme.
type surface struct {
	name string
	p    *Platform

	mu sync.RWMutex
	// seats are oldest first, which is the order requests are offered in.
	seats []*seat
	// ctx and out are set while Ingest runs.
	ctx context.Context
	out chan<- envelope.Tag
}

var (
	_ connector.Trigger = (*surface)(nil)
	_ connector.Sink    = (*surface)(nil)
	_ http.Handler      = (*surface)(nil)
)

func (s *surface) Name() string { return s.name }

// Ingest runs every client's trigger and stamps what each raises. It parks
// until ctx is done, and clients added while it runs start at once.
func (s *surface) Ingest(ctx context.Context, out chan<- envelope.Tag) error {
	s.mu.Lock()
	if s.out != nil {
		s.mu.Unlock()
		return fmt.Errorf("platform: surface %s is already ingesting", s.name)
	}
	s.ctx, s.out = ctx, out
	for _, st := range s.seats {
		s.start(st)
	}
	s.mu.Unlock()

	<-ctx.Done()
	s.mu.Lock()
	s.ctx, s.out = nil, nil
	seats := slices.Clone(s.seats)
	s.mu.Unlock()
	for _, st := range seats {
		st.stop()
	}
	return nil
}

// start runs a seat's trigger. The caller holds s.mu.
func (s *surface) start(st *seat) {
	if s.ctx == nil || st.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	st.cancel, st.done = cancel, make(chan struct{})
	go s.pump(ctx, st, s.out)
}

func (st *seat) stop() {
	if st.cancel == nil {
		return
	}
	st.cancel()
	<-st.done
}

// pump carries one client's tags to the platform, stamped with where they
// came from.
func (s *surface) pump(ctx context.Context, st *seat, out chan<- envelope.Tag) {
	defer close(st.done)
	tags := make(chan envelope.Tag)
	failed := make(chan error, 1)
	go func() { failed <- st.peer.Ingest(ctx, tags) }()
	wait := func() {
		if err := <-failed; err != nil && !errors.Is(err, context.Canceled) {
			s.p.log.Error("platform: client trigger shutdown failed", "surface", s.name, "tenant", st.client.Tenant, "client", st.client.ID, "error", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			wait()
			return
		case err := <-failed:
			if err != nil && ctx.Err() == nil {
				s.p.log.Error("platform: client trigger stopped", "surface", s.name, "tenant", st.client.Tenant, "client", st.client.ID, "error", err)
			}
			return
		case tag := <-tags:
			tag, ok := s.stamp(st, tag)
			if !ok {
				continue
			}
			select {
			case out <- tag:
			case <-ctx.Done():
				wait()
				return
			}
		}
	}
}

// stamp binds a tag to the client that raised it. Whatever the connector or
// the payload said about tenant, origin or client is overwritten here, and a
// tag for an agent the client may not route to goes no further.
func (s *surface) stamp(st *seat, tag envelope.Tag) (envelope.Tag, bool) {
	c := st.client
	if !c.Routes(tag.Agent) {
		s.p.log.Warn("platform: client may not route to that agent", "surface", s.name, "tenant", c.Tenant, "client", c.ID, "agent", tag.Agent, "tag", tag.ID)
		return envelope.Tag{}, false
	}
	meta := make(map[string]string, len(tag.Meta)+1)
	for k, v := range tag.Meta {
		meta[k] = v
	}
	meta[runs.MetaClient] = c.ID
	tag.Tenant, tag.Origin, tag.Meta = c.Tenant, s.name, meta
	return tag, true
}

// seat returns a tenant's enabled client by id, or nil.
func (s *surface) seat(tenant, id string) *seat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, st := range s.seats {
		if st.client.Tenant == tenant && st.client.ID == id {
			return st
		}
	}
	return nil
}

func (s *surface) all() []*seat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.seats)
}

// put seats a client, in place of the one with its id if there is one. The
// old connector is stopped and flushed before the new one is offered anything.
func (s *surface) put(st *seat) {
	s.mu.Lock()
	var old *seat
	i := slices.IndexFunc(s.seats, func(x *seat) bool {
		return x.client.Tenant == st.client.Tenant && x.client.ID == st.client.ID
	})
	if i >= 0 {
		old = s.seats[i]
		s.seats = slices.Delete(s.seats, i, i+1)
	}
	s.mu.Unlock()
	s.retire(old)

	s.mu.Lock()
	// Oldest first, so a replaced client keeps its place in the order.
	at := len(s.seats)
	for j, x := range s.seats {
		if x.client.CreatedAt.After(st.client.CreatedAt) {
			at = j
			break
		}
	}
	s.seats = slices.Insert(s.seats, at, st)
	s.start(st)
	running := s.ctx != nil
	s.mu.Unlock()

	// A webhook that arrives right after a client is created should find it
	// ready rather than be told to retry.
	for deadline := time.Now().Add(time.Second); running && !st.peer.Ingesting() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
}

// remove unseats a client.
func (s *surface) remove(tenant, id string) {
	s.mu.Lock()
	var old *seat
	i := slices.IndexFunc(s.seats, func(x *seat) bool { return x.client.Tenant == tenant && x.client.ID == id })
	if i >= 0 {
		old = s.seats[i]
		s.seats = slices.Delete(s.seats, i, i+1)
	}
	s.mu.Unlock()
	s.retire(old)
}

func (s *surface) retire(st *seat) {
	if st == nil {
		return
	}
	st.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.peer.Flush(ctx); err != nil {
		s.p.log.Warn("platform: client flush", "surface", s.name, "tenant", st.client.Tenant, "client", st.client.ID, "error", err)
	}
}

// claim reads what a request says about who it is for, before anything has
// verified it. It only narrows which clients are offered the request: a claim
// that names the wrong client is offered to that client and fails its secret.
func (s *surface) claim(r *http.Request, body []byte) clients.Claim {
	var c clients.Claim
	if s.name == string(clients.Slack) {
		if strings.HasSuffix(r.URL.Path, "/commands") {
			form, err := url.ParseQuery(string(body))
			if err == nil {
				c.Team, c.AppID = form.Get("team_id"), form.Get("api_app_id")
			}
			return c
		}
		var env struct {
			Team string `json:"team_id"`
			App  string `json:"api_app_id"`
		}
		if json.Unmarshal(body, &env) == nil {
			c.Team, c.AppID = env.Team, env.App
		}
		return c
	}
	if r.Header.Get("X-GitHub-Hook-Installation-Target-Type") == "integration" {
		c.AppID = r.Header.Get("X-GitHub-Hook-Installation-Target-ID")
	}
	var hook struct {
		Installation *struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository *struct {
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &hook) == nil {
		if hook.Installation != nil && hook.Installation.ID != 0 {
			c.Installation = strconv.FormatInt(hook.Installation.ID, 10)
		}
		if hook.Repository != nil {
			c.Owner = hook.Repository.Owner.Login
		}
	}
	return c
}

// ServeHTTP gives a webhook to the client it is for.
//
// The request is offered, oldest first, to every client whose identity it
// states, and goes to the first whose secret verifies the exact bytes. A
// request no client claims is a 404, so an unknown client, a disabled one and
// a surface with no clients at all look the same. A request that clients claim
// and none can verify is a 401.
func (s *surface) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	claim := s.claim(r, body)
	var unmanaged *seat
	offered := 0
	for _, st := range s.all() {
		if !st.client.Claims(claim) {
			continue
		}
		offered++
		if st.verifier == nil {
			if unmanaged == nil {
				unmanaged = st
			}
			continue
		}
		if st.verifier.Verify(r.Header, body) == nil {
			s.hand(st, w, r, body)
			return
		}
	}
	switch {
	case unmanaged != nil:
		s.hand(unmanaged, w, r, body)
	case offered == 0:
		http.NotFound(w, r)
	default:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

// hand passes a request to one client's connector, which verifies it again
// with the same secret before it parses anything.
func (s *surface) hand(st *seat, w http.ResponseWriter, r *http.Request, body []byte) {
	r = r.Clone(r.Context())
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	st.peer.ServeHTTP(w, r)
}

// Deliver gives a run's event to the client that carries it.
func (s *surface) Deliver(ctx context.Context, to address.Address, e envelope.Event) error {
	st, err := s.carrier(to, e)
	if err != nil {
		return err
	}
	return st.peer.Deliver(ctx, to, e)
}

// carrier picks the client an event goes out through. It is the client that
// raised the run when that client is on this surface. Otherwise it is the one
// client in the run's tenant that serves the address and may route to the
// run's agent. No such client, or more than one, and the event goes nowhere.
func (s *surface) carrier(to address.Address, e envelope.Event) (*seat, error) {
	if e.Tenant == "" {
		return nil, fmt.Errorf("%w: event has no tenant", connector.ErrUndeliverable)
	}
	if id := s.p.clientOf(e.Tenant, e.RunID); id != "" {
		if st := s.seat(e.Tenant, id); st != nil {
			return st, nil
		}
		// The run's own client is on this surface and is switched off.
		if c, err := s.p.Clients.Get(e.Tenant, id); err == nil && string(c.Surface) == s.name {
			return nil, fmt.Errorf("%w: client %q is disabled", connector.ErrUndeliverable, id)
		}
	}
	var found *seat
	for _, st := range s.all() {
		c := st.client
		if c.Tenant != e.Tenant || !c.Routes(e.Agent) || (c.Managed != clients.ManagedStatic && !c.Serves(to.Workspace)) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%w: more than one %s client serves %s", connector.ErrUndeliverable, s.name, to.Workspace)
		}
		found = st
	}
	if found == nil {
		return nil, fmt.Errorf("%w: no %s client serves %s", connector.ErrUndeliverable, s.name, to.Workspace)
	}
	return found, nil
}

// actor picks the client whose actions a run gets on this surface: the client
// that raised it, or else the oldest in its tenant that may route to its
// agent.
func (s *surface) actor(tenant, client, agent string) *seat {
	if client != "" {
		if st := s.seat(tenant, client); st != nil {
			return st
		}
	}
	for _, st := range s.all() {
		if st.client.Tenant == tenant && st.client.Routes(agent) {
			return st
		}
	}
	return nil
}

// Webhook serves a surface's signed endpoint. What authorizes a request is a
// client's own secret, independent of browser sessions and of anything in the
// payload.
func (p *Platform) Webhook(name string) http.Handler {
	if s, ok := p.surfaces[name]; ok {
		return s
	}
	return http.NotFoundHandler()
}

// IngressHandler exposes only signed integration endpoints, never the console
// or its dev-auth API. Use this listener when forwarding a local trial.
func (p *Platform) IngressHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /connectors/github", p.Webhook("github"))
	mux.Handle("POST /connectors/slack", p.Webhook("slack"))
	mux.Handle("POST /connectors/slack/commands", p.Webhook("slack"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		seated := 0
		for _, s := range p.surfaces {
			for _, st := range s.all() {
				seated++
				if !st.peer.Ingesting() {
					http.Error(w, "not ready", http.StatusServiceUnavailable)
					return
				}
			}
		}
		if seated == 0 {
			http.Error(w, "no integrations configured", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// connectorTools gives a turn the actions of its tenant's clients, at most one
// client per surface, and none of another tenant's.
func (p *Platform) connectorTools(req runtime.Request, emit func(payload.Action) error) (agentrt.Tools, error) {
	reg := connector.NewRegistry()
	client := p.clientOf(req.Tenant, req.RunID)
	for _, surface := range clients.Surfaces() {
		st := p.surfaces[string(surface)].actor(req.Tenant, client, req.Agent())
		if st == nil {
			continue
		}
		if err := reg.Register(st.peer); err != nil {
			return nil, err
		}
	}
	return runtime.Connectors(reg)(req, emit)
}

// clientOf is the external client a run was raised by, or "". A run that has
// been admitted and is not recorded yet is known from the admission.
func (p *Platform) clientOf(tenant, runID string) string {
	if tenant == "" {
		return ""
	}
	if run, err := p.Runs.Get(tenant, runID); err == nil {
		return run.Client
	}
	if v, ok := p.admitting.Load(runID); ok {
		if a := v.(admitted); a.tenant == tenant {
			return a.client
		}
	}
	return ""
}

// admitted is what is known of a run between its admission and its record.
type admitted struct{ tenant, client string }

func (p *Platform) flushConnections(ctx context.Context) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			for name, s := range p.surfaces {
				for _, st := range s.all() {
					if err := st.peer.Flush(ctx); err != nil && !errors.Is(err, context.Canceled) {
						p.log.Warn("connector flush", "connector", name, "client", st.client.ID, "error", err)
					}
				}
			}
		}
	}
}
