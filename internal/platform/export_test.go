package platform

import (
	"context"
	"os"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/runtime"

	"github.com/urmzd/collegium/pkg/runs"
)

// UseTool calls one of a turn's file tools the way the agent loop would, and
// returns what the tool reported on the run's stream.
func (p *Platform) UseTool(ctx context.Context, req runtime.Request, name string, args map[string]any) (string, []payload.Action, error) {
	var reported []payload.Action
	tools, err := p.fileTools(nil)(req, func(a payload.Action) error {
		reported = append(reported, a)
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	tool, err := tools.Lookup(name)
	if err != nil {
		return "", nil, err
	}
	out, err := tool.Execute(ctx, args)
	return out, reported, err
}

// SandboxDir is where a conversation's files are.
func (p *Platform) SandboxDir(tenant, thread string) string { return p.sandboxDir(tenant, thread) }

// SweepSandboxes runs the retention sweep now.
func (p *Platform) SweepSandboxes() { p.sweepSandboxes() }

// Age makes a conversation's files look untouched since a time.
func (p *Platform) Age(tenant, thread string, at time.Time) error {
	return os.Chtimes(p.sandboxDir(tenant, thread), at, at)
}

// ConnectorTools is the client actions a turn would be given.
func (p *Platform) ConnectorTools(req runtime.Request) (agentrt.Tools, error) {
	return p.connectorTools(req, func(payload.Action) error { return nil })
}

// Redeliver hands the platform an event again, as the bus may.
func (p *Platform) Redeliver(ctx context.Context, e envelope.Event) { p.observe(ctx, e) }

// Reconcile runs one pass of the ledger reconciler now.
func (p *Platform) Reconcile(ctx context.Context) { p.reconcile(ctx) }

// Ended acts on a run's ending again, as a second report of it would.
func (p *Platform) Ended(ctx context.Context, run runs.Run) { p.ended(ctx, run) }
