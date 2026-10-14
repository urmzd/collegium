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
// Stopping is the runtime's. A stop is a request published on the bus, which
// the attempt executing the run hears: the model call in flight is cancelled,
// the turn wraps up with what it had said, and the run ends with
// lifecycle.stopped. Until that event arrives the run is stopping, and from
// the moment it is asked it can start no further calls. A run whose turn
// finished before the request reached it ends as it would have, and is never
// reported stopped. Everything attached to a stopped run is asked to stop with
// it, through the runtime, all the way down.

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
	Run       string         `json:"run"`
	Agent     string         `json:"agent"`
	Lifecycle runs.Lifecycle `json:"lifecycle"`
}

func tell(report func(payload.Action) error, tool string, child runs.Run, summary string) {
	data, _ := json.Marshal(calledRun{Run: child.ID, Agent: child.Agent, Lifecycle: child.Lifecycle})
	// Saying which run was started is a courtesy to whoever is watching. The
	// call stands whether or not it is heard.
	_ = report(payload.Action{Name: tool, Summary: clip(summary, 200), Address: "run:" + child.ID, Data: data})
}

// attach is the runtime's Delegator: a turn tagged another agent and is
// waiting for its answer. The agent runs as a run attached to the caller.
func (p *Platform) attach(ctx context.Context, parent runtime.Request, target, text string, report func(payload.Action) error) (string, error) {
	// Every run that waits holds one of the places runs execute in. If all
	// of them were held by runs waiting, nothing could run to answer them.
	// So one place is always kept for work that waits on nobody.
	if int(p.waiting.Add(1)) >= p.slots() {
		p.waiting.Add(-1)
		return "", fmt.Errorf("%w, so %s cannot be asked right now", ErrBusy, target)
	}
	defer p.waiting.Add(-1)

	child, err := p.Call(ctx, parent.Tenant, parent.RunID, call.Request{
		Key: callKey("attach", target, text), Agent: target, Text: text, Mode: call.Subagent,
	})
	if err != nil {
		return "", err
	}
	tell(report, runtime.DelegateToolName(target), child, text)

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
			// The runtime cancels a delegation when the turn that made it
			// is stopped, says so in the context's cause, and leaves the run
			// it started to this host. A stopped caller takes its attached
			// run along, in the same words. One whose worker merely went
			// away does not: its next attempt makes the same call and joins
			// the same run.
			if cause := context.Cause(ctx); errors.Is(cause, runtime.ErrStopped) {
				by := "system"
				var stopped *runtime.StoppedError
				if errors.As(cause, &stopped) && stopped.By != "" {
					by = stopped.By
				}
				caller, _ := p.Runs.Get(parent.Tenant, parent.RunID)
				if caller.ID == "" {
					caller = runs.Run{ID: parent.RunID, Agent: parent.Agent()}
				}
				if err := p.requestStop(context.WithoutCancel(ctx), child.Tenant, child.ID, attachedCause("stopped", caller), by); err != nil {
					p.log.Warn("platform: an attached run could not be asked to stop", "tenant", child.Tenant, "run", child.ID, "error", err)
				}
			}
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
			tell(action, ToolDelegate, child, text)
			return fmt.Sprintf("%s has started on it as run %s and is working independently. Its answer will arrive in the inbox.", target, child.ID), nil
		},
	}
}

// cascade is what the ending of one run does to the runs around it.
func (p *Platform) cascade(ctx context.Context, run runs.Run) {
	// A run that failed or was stopped takes what is attached to it along,
	// through the runtime, so each of them keeps what it had said. What it
	// delegated is not attached, and carries on.
	if run.Status == runs.Failed || run.Status == runs.Stopped {
		why := "failed"
		if run.Status == runs.Stopped {
			why = "stopped"
		}
		for _, child := range p.Runs.AttachedTo(run.ID) {
			if err := p.requestStop(ctx, child.Tenant, child.ID, attachedCause(why, run), "system"); err != nil {
				p.log.Warn("platform: an attached run could not be asked to stop", "tenant", child.Tenant, "run", child.ID, "error", err)
			}
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

// attachedCause says why an attached run was stopped: what became of the run
// it was attached to.
func attachedCause(what string, caller runs.Run) string {
	return fmt.Sprintf("The run it was attached to was %s (%s, %s)", what, caller.Agent, caller.ID)
}

// ErrStopUnavailable reports a runtime that cannot interrupt a run. The run is
// still recorded as asked to stop, so it can start nothing more, but the work
// it is doing carries on and it ends as its own events say.
var ErrStopUnavailable = errors.New("platform: this runtime cannot interrupt a run")

// stopper is the part of the runtime a stop goes through.
type stopper interface {
	Stop(ctx context.Context, req runtime.StopRequest) error
}

// requestStop asks a run, and everything attached to it, to stop.
//
// Each run is first marked stopping in the index, which is what refuses its
// further calls, and then asked through the runtime, which is what interrupts
// it. A run whose turn is already over and that only this platform holds open
// (it has answered and waits for what it attached) has nothing to interrupt,
// so it ends stopped here, keeping its answer.
//
// Asking is idempotent: a run already asked is asked again, which the runtime
// ignores, so a request whose publication failed can be retried. The error is
// the first request that could not be published. Every other run is still
// asked.
func (p *Platform) requestStop(ctx context.Context, tenant, runID, cause, by string) error {
	type ask struct{ id, cause string }
	var ended []runs.Run
	var failed error
	// Serialized with Call, so a call either lands before the request and is
	// stopped with the rest, or after it and is refused.
	p.calls.Lock()
	for queue := []ask{{runID, cause}}; len(queue) > 0; queue = queue[1:] {
		next := queue[0]
		run, ok := p.Runs.AskStop(next.id, p.now())
		if !ok || run.Tenant != tenant {
			continue
		}
		p.log.Info("platform: run asked to stop", "tenant", run.Tenant, "run", run.ID, "agent", run.Agent, "cause", next.cause, "by", by,
			"root", run.Root, "parent", run.Parent, "lifecycle", run.Lifecycle)
		if run.Finishing {
			if stopped, did := p.Runs.Stop(run.ID, "", next.cause, p.now()); did {
				// Its ending stops what is attached to it.
				ended = append(ended, stopped)
			}
			continue
		}
		err := p.stops.Stop(ctx, runtime.StopRequest{Tenant: run.Tenant, RunID: run.ID, Reason: next.cause, By: by})
		if errors.Is(err, runtime.ErrStopUnavailable) {
			err = fmt.Errorf("%w: %w", ErrStopUnavailable, err)
		}
		if err != nil && failed == nil {
			failed = err
		}
		// Stop says nothing about the run, by design. The ledger does: a
		// run it holds no request for has already ended there, and its own
		// terminal event is on its way. It is not reported as stopping.
		if err == nil && p.asked != nil {
			if _, held, aerr := p.asked(ctx, run.Tenant, run.ID); aerr == nil && !held {
				p.Runs.Unask(run.ID)
			}
		}
		for _, child := range p.Runs.AttachedTo(run.ID) {
			queue = append(queue, ask{child.ID, attachedCause("stopped", run)})
		}
	}
	p.calls.Unlock()
	for _, run := range ended {
		p.ended(ctx, run)
	}
	return failed
}

// StopRun asks a run to stop at a person's request. Everything attached to it
// is asked too. What it delegated keeps running and can be stopped by itself.
//
// It does not wait for the run. The run it returns is stopping, or already
// over: stopped, or completed or failed when its turn finished first. A run
// that has ended is returned as it is, so asking twice is safe.
func (p *Platform) StopRun(ctx context.Context, who auth.Principal, runID string) (runs.Run, error) {
	run, err := p.Runs.Get(who.Tenant, runID)
	if err != nil {
		return runs.Run{}, err
	}
	if run.Status.Terminal() {
		return run, nil
	}
	err = p.requestStop(ctx, who.Tenant, runID, "Stopped by "+who.Subject, who.Subject)
	now, gerr := p.Runs.Get(who.Tenant, runID)
	if gerr != nil {
		return runs.Run{}, gerr
	}
	return now, err
}

// stopFromSurface asks a run to stop for a person who pressed stop where the
// run is answering, such as a Slack session. The tenant and the client are
// those of the client whose secret verified the request, never the request's,
// and a run outside either does not exist to this caller. Anyone who can see
// the surface's stop control may use it: the surface, not a console role, is
// the authority here.
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
	return p.requestStop(ctx, tenant, runID, "Stopped by "+by, by)
}
