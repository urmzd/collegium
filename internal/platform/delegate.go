package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/runtime"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/runs"
)

// Agents calling agents.
//
// Every run belongs to one origin: the run a person, a schedule or a trigger
// started. Under it runs nest as deep as the limits allow, and each one a run
// calls is a run in its own right, with an id, a stream and a record.
//
// A called run has one of two lifetimes, chosen when the call is made.
//
// Attached: the caller asked and is waiting for the answer. The run lives
// inside its caller's lifetime. The caller is not done until it is, and if
// the caller fails or is stopped, it is stopped too, and so is everything
// attached to it in turn. An agent attaches another by tagging it.
//
// Delegated: the caller handed work over and carried on. The run's lifetime is
// its own. It finishes whatever becomes of its caller, and its answer goes to
// the inbox, because the caller may be gone by then. What it attaches dies
// with it, not with whoever delegated to it.
//
// Stopping is done at the platform's edge. The runtime cannot interrupt a
// model call in flight, so a stopped run is marked stopped at once: it can
// start nothing more, its answer is not taken, and whoever was waiting on it
// is told. The call it was in the middle of runs to its end unheard.

// ToolDelegate is the tool an agent starts another agent with and does not
// wait. An agent has it only if its spec names it, and may only name agents
// in its spawn allowlist.
const ToolDelegate = "delegate_task"

// defaultConcurrency is how many runs execute at once when none is set. It is
// more than one because a run waiting on an attached run holds a place while
// the attached run needs another.
const defaultConcurrency = 4

// ErrBusy reports that no place is free for another run to wait in.
var ErrBusy = errors.New("platform: too many runs are waiting on other runs")

// slots is how many runs execute at once.
func (p *Platform) slots() int {
	if p.cfg.Concurrency > 0 {
		return p.cfg.Concurrency
	}
	return defaultConcurrency
}

// callKey names a call so that making it again joins the run it started. A
// turn that is retried asks the same thing of the same agent and gets the
// same run, not a second one.
func callKey(kind, target, text string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + target + "\x00" + text))
	return kind + "-" + hex.EncodeToString(h[:8])
}

type calledRun struct {
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Run        string         `json:"run"`
	Agent      string         `json:"agent"`
	Lifecycle  runs.Lifecycle `json:"lifecycle"`
}

func tell(ctx context.Context, report func(payload.Action) error, tool string, child runs.Run, summary string) {
	info, _ := saigetypes.ToolCallInfoFromContext(ctx)
	data, _ := json.Marshal(calledRun{Run: child.ID, Agent: child.Agent, Lifecycle: child.Lifecycle, ToolCallID: info.ID})
	// Saying which run was started is a courtesy to whoever is watching. The
	// call stands whether or not it is heard.
	_ = report(payload.Action{Name: tool, Summary: clip(summary, 200), Address: "run:" + child.ID, Data: data})
}

// attach is the runtime's Delegator: a turn tagged another agent and is
// waiting for its answer. The agent runs as a run attached to the caller.
func (p *Platform) attach(ctx context.Context, parent runtime.Request, target, text string, report func(payload.Action) error) (string, error) {
	return p.waitForCall(ctx, parent, call.Request{
		Key: callKey("attach", target, text), Agent: target, Text: text, Mode: call.Subagent,
	}, runtime.DelegateToolName(target), report)
}

func (p *Platform) waitForCall(ctx context.Context, parent runtime.Request, req call.Request, tool string, report func(payload.Action) error) (string, error) {
	// Keep one execution slot for work that can answer waiting callers.
	if int(p.waiting.Add(1)) >= p.slots() {
		p.waiting.Add(-1)
		return "", fmt.Errorf("%w, so %s cannot be asked right now", ErrBusy, req.Agent)
	}
	defer p.waiting.Add(-1)
	child, err := p.Call(ctx, parent.Tenant, parent.RunID, req)
	if err != nil {
		return "", err
	}
	tell(ctx, report, tool, child, "Helper started")
	target := req.Agent
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		run, err := p.Runs.Get(parent.Tenant, child.ID)
		if err != nil {
			return "", err
		}
		switch run.Status {
		case runs.Completed:
			return run.Answer, nil
		case runs.Failed:
			return "", fmt.Errorf("%s failed: %s", target, oneLine(run.Error, 300))
		case runs.Stopped:
			return "", fmt.Errorf("%s was stopped: %s", target, run.Cause)
		}
		// A caller that is itself over has nobody to give the answer to.
		if caller, err := p.Runs.Get(parent.Tenant, parent.RunID); err == nil && caller.Status.Terminal() {
			return "", fmt.Errorf("%w: the run that asked %s has ended", call.ErrInvalid, target)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
		}
	}
}

// delegateTool is the tool an agent hands work to another agent with, without
// waiting for it.
func (p *Platform) delegateTool(req runtime.Request, action func(payload.Action) error) saigetypes.Tool {
	return &saigetypes.ToolFunc{
		Def: saigetypes.ToolDef{
			Name: ToolDelegate,
			Description: "Hand a piece of work to another agent and carry on without waiting. " +
				"It works independently and finishes even if you do not. Its answer goes to the inbox, not back to you. " +
				"To ask an agent something and wait for the answer, tag it instead.",
			Parameters: schema([]string{"agent", "text"}, map[string]string{
				"agent": "The agent to hand the work to",
				"text":  "What it should do, with everything it needs to know: it sees none of this conversation",
			}),
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			if req.RunID == "" {
				return "", fmt.Errorf("%w: a turn with no run of its own cannot hand work on", call.ErrInvalid)
			}
			target, text := arg(args, "agent"), arg(args, "text")
			child, err := p.Call(ctx, req.Tenant, req.RunID, call.Request{
				Key: callKey("delegate", target, text), Agent: target, Text: text, Mode: call.Delegate,
			})
			if err != nil {
				return "", err
			}
			tell(ctx, action, ToolDelegate, child, text)
			return fmt.Sprintf("%s has started on it as run %s and is working independently. Its answer will arrive in the inbox.", target, child.ID), nil
		},
	}
}

// cascade is what the ending of one run does to the runs around it.
func (p *Platform) cascade(ctx context.Context, run runs.Run) {
	// A run that failed or was stopped takes what is attached to it along.
	// What it delegated is not attached, and carries on.
	if run.Status == runs.Failed || run.Status == runs.Stopped {
		why := "The run it was attached to failed"
		if run.Status == runs.Stopped {
			why = "The run it was attached to was stopped"
		}
		for _, child := range p.Runs.AttachedTo(run.ID) {
			p.stop(ctx, child.ID, fmt.Sprintf("%s (%s, %s)", why, run.Agent, run.ID), "system")
		}
	}
	// A caller that had answered and was waiting on this run may be done now.
	if run.Parent != "" && run.Lifecycle == runs.Attached {
		if parent, done := p.Runs.Settle(run.Parent, p.now()); done {
			p.ended(ctx, parent)
		}
	}
	// A delegated run's caller is not waiting and may be gone, so its answer
	// goes where a person will find it.
	if run.Lifecycle == runs.Delegated && run.Status == runs.Completed {
		p.Inbox.Raise(run.Tenant, "result:"+run.ID, inbox.Message{
			Kind: inbox.KindResult, Subject: run.Agent + " finished: " + oneLine(run.Text, 80),
			From: inbox.From{Kind: "agent", Name: run.Agent}, Severity: inbox.Info,
			Source: inbox.Source{Run: run.ID, Thread: run.Thread, Agent: run.Agent, Project: run.Project},
			Parts:  []inbox.Part{{Type: inbox.PartText, State: inbox.PartInfo, Text: clip(run.Answer, 4000)}},
		}, run.UpdatedAt)
	}
}

// stop ends a run from outside and lets that ending do what any ending does.
func (p *Platform) stop(ctx context.Context, runID, cause, by string) (runs.Run, bool) {
	run, stopped := p.Runs.Stop(runID, cause, p.now())
	if !stopped {
		return runs.Run{}, false
	}
	p.log.Info("platform: run stopped", "tenant", run.Tenant, "run", run.ID, "agent", run.Agent, "cause", cause, "by", by,
		"root", run.Root, "parent", run.Parent, "lifecycle", run.Lifecycle)
	p.ended(ctx, run)
	return run, true
}

// StopRun stops a run at a person's request. Everything attached to it stops
// with it. What it delegated keeps running and can be stopped by itself.
func (p *Platform) StopRun(ctx context.Context, who auth.Principal, runID string) (runs.Run, error) {
	run, err := p.Runs.Get(who.Tenant, runID)
	if err != nil {
		return runs.Run{}, err
	}
	if run.Status.Terminal() {
		return run, nil
	}
	if stopped, ok := p.stop(ctx, runID, "Stopped by "+who.Subject, who.Subject); ok {
		return stopped, nil
	}
	return p.Runs.Get(who.Tenant, runID)
}

// stopFromSurface stops a run for a person who pressed stop where the run is
// answering, such as a Slack session. The tenant and the client are those of
// the client whose secret verified the request, never the request's, and a run
// outside either does not exist to this caller. Anyone who can see the
// surface's stop control may use it: the surface, not a console role, is the
// authority here.
func (p *Platform) stopFromSurface(ctx context.Context, tenant, client, surface, runID, who string) error {
	run, err := p.Runs.Get(tenant, runID)
	if errors.Is(err, runs.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Client != client || run.Status.Terminal() {
		return nil
	}
	by := surface + ":" + who
	p.stop(ctx, runID, "Stopped by "+by, by)
	return nil
}
