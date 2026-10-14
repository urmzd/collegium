package platform

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/urmzd/mandatum/pkg/address"
	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/connector"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/runtime"
)

// Peer is an installed external integration. Its HTTP handler authenticates
// webhooks; it never trusts a tenant supplied in the external request.
type Peer interface {
	connector.Trigger
	connector.Sink
	connector.Actor
	http.Handler
	Ingesting() bool
	Flush(context.Context) error
}

// Connection binds an installed integration to one server-configured tenant.
// This alpha supports one installation per connector scheme per process.
type Connection struct {
	Tenant string
	Peer   Peer
}

// surfaceStopper is a peer whose surface has a stop control. The platform
// gives it the way to stop a run, bound to the installation's tenant.
type surfaceStopper interface {
	OnStop(func(ctx context.Context, runID, who string) error)
}

type boundPeer struct {
	Peer
	tenant string
}

func (b *boundPeer) Ingest(ctx context.Context, out chan<- envelope.Tag) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tags := make(chan envelope.Tag)
	done := make(chan error, 1)
	go func() { done <- b.Peer.Ingest(ctx, tags) }()
	for {
		select {
		case <-ctx.Done():
			return <-done
		case err := <-done:
			return err
		case tag := <-tags:
			tag.Tenant = b.tenant
			select {
			case out <- tag:
			case <-ctx.Done():
				return <-done
			}
		}
	}
}

func (b *boundPeer) Deliver(ctx context.Context, to address.Address, e envelope.Event) error {
	if e.Tenant != b.tenant {
		return fmt.Errorf("%w: connector tenant mismatch", connector.ErrUndeliverable)
	}
	return b.Peer.Deliver(ctx, to, e)
}

// Webhook serves only an explicitly installed, signature-verifying peer.
// Its credential-to-tenant binding is the webhook principal, independent of
// browser sessions and of any fields in the payload.
func (p *Platform) Webhook(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := p.connections[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b.ServeHTTP(w, r)
	})
}

// IngressHandler exposes only signed integration endpoints, never the console
// or its dev-auth API. Use this listener when forwarding a local trial.
func (p *Platform) IngressHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /connectors/github", p.Webhook("github"))
	mux.Handle("POST /connectors/slack", p.Webhook("slack"))
	mux.Handle("POST /connectors/slack/commands", p.Webhook("slack"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if len(p.connections) == 0 {
			http.Error(w, "no integrations configured", http.StatusServiceUnavailable)
			return
		}
		for _, b := range p.connections {
			if !b.Ingesting() {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func (p *Platform) connectorTools(req runtime.Request, emit func(payload.Action) error) (agentrt.Tools, error) {
	reg := p.connectionTools[req.Tenant]
	if reg == nil {
		reg = connector.NewRegistry()
	}
	return runtime.Connectors(reg)(req, emit)
}

func (p *Platform) flushConnections(ctx context.Context) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			for name, b := range p.connections {
				if err := b.Flush(ctx); err != nil {
					p.log.Warn("connector flush", "connector", name, "error", err)
				}
			}
		}
	}
}
