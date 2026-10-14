package platform

import (
	"context"
	"os"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/runtime"
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

// StopWith replaces how runs are asked to stop, for a test of a runtime that
// cannot interrupt one.
func (p *Platform) StopWith(fn func(context.Context, runtime.StopRequest) error) {
	p.stops = stopFunc(fn)
}

type stopFunc func(context.Context, runtime.StopRequest) error

func (f stopFunc) Stop(ctx context.Context, req runtime.StopRequest) error { return f(ctx, req) }
