package slack

import (
	"context"
	"errors"
	"fmt"
	transport "github.com/urmzd/mandatum/pkg/connectors/slack"
	"strings"
	"sync"

	"github.com/urmzd/mandatum/pkg/address"
	"github.com/urmzd/mandatum/pkg/envelope"
)

// MaxCommands is Slack's limit on commands in one code channel. The limit is
// shared by every agent in the channel, so a set this size can still be refused.
const MaxCommands = 10

// maxCommandName is Slack's limit on a command name.
const maxCommandName = 31

// maxChannels bounds how many channels the connector remembers a registration
// outcome for. Forgetting one costs a single repeated API call.
const maxChannels = 4096

// statusFor maps a run's lifecycle to the session status a human should see.
//
// Only lifecycle events move the status. A run emits a handful of them and
// hundreds of deltas, and repeating a status is harmless, so reporting on every
// lifecycle event needs no bookkeeping to stay correct under redelivery.
//
// A failed run reports active, not closed: the failure is in the message the
// run owns, and the thread is still a conversation the human can continue.
func statusFor(k envelope.Kind) (Status, bool) {
	switch k {
	case envelope.KindAccepted, envelope.KindStarted, envelope.KindResumed:
		return StatusProcessing, true
	case envelope.KindParked:
		return StatusSuspended, true
	case envelope.KindCompleted, envelope.KindFailed:
		return StatusActive, true
	}
	return "", false
}

// session reports the run's status to Slack and, when a run begins, offers the
// configured commands in its channel.
//
// A refusal from Slack is logged and dropped, never returned. Status is
// decoration on a message that has already been delivered: returning the error
// would make the router retry an event whose answer is on the surface, and a
// workspace without the agent feature would retry every run forever. A
// transport failure is returned, because that one a retry can fix.
func (c *Connector) session(ctx context.Context, target address.Address, e envelope.Event) error {
	if e.Kind == envelope.KindAccepted && len(c.cfg.Commands) > 0 {
		if err := c.offerCommands(ctx, target); err != nil {
			return err
		}
	}
	status, ok := statusFor(e.Kind)
	if !c.cfg.Sessions || !ok {
		return nil
	}

	thread, _ := target.Param("thread")
	s := Session{Workspace: target.Workspace, Channel: target.Path[0], ThreadTS: thread, Status: status}
	err := c.cfg.API.(transport.AgentAPI).SetStatus(ctx, s)
	if errorCode(err) == "thread_ts_not_allowed" {
		// A code channel is its own session. The trigger cannot know a mention
		// came from one, so it threads the answer as it does everywhere else,
		// and Slack tells us here.
		s.ThreadTS = ""
		err = c.cfg.API.(transport.AgentAPI).SetStatus(ctx, s)
	}
	return c.refused(err, "session status", target, e.RunID)
}

// offerCommands registers the configured commands in the target channel, once.
//
// The connector does not know which channels are code channels and does not ask:
// Slack answers not_agent_channel for the rest, which is remembered so an
// ordinary channel costs one call rather than one per run.
func (c *Connector) offerCommands(ctx context.Context, target address.Address) error {
	key := target.Workspace + "/" + target.Path[0]
	c.offered.mu.Lock()
	done := c.offered.seen[key]
	c.offered.mu.Unlock()
	if done {
		return nil
	}

	err := c.cfg.API.(transport.AgentAPI).SetCommands(ctx, target.Workspace, target.Path[0], c.cfg.Commands)
	switch code := errorCode(err); {
	case err == nil, code == "not_agent_channel":
		c.offered.mu.Lock()
		if len(c.offered.seen) >= maxChannels {
			clear(c.offered.seen)
		}
		c.offered.seen[key] = true
		c.offered.mu.Unlock()
		return nil
	default:
		return c.refused(err, "command registration", target, "")
	}
}

// refused logs an application-level refusal and reports nil for it; any other
// error passes through.
func (c *Connector) refused(err error, what string, target address.Address, run string) error {
	code := errorCode(err)
	// Retry transient Slack failures as well as HTTP/transport errors. Session
	// status and full command-set replacement are safe to retry.
	switch code {
	case "ratelimited", "internal_error", "fatal_error", "service_unavailable", "request_timeout":
		return err
	}
	if code == "" {
		return err
	}
	if c.cfg.Log != nil {
		c.cfg.Log.Warn("slack: "+what+" refused", "error", code, "target", target.String(), "run", run)
	}
	return nil
}

// offered remembers the channels whose command registration is settled.
type offered struct {
	mu   sync.Mutex
	seen map[string]bool
}

// validCommands checks a command set against the limits Slack documents, so a
// misconfiguration fails at startup instead of on the first run in a channel.
func validCommands(commands []Command) error {
	if len(commands) > MaxCommands {
		return fmt.Errorf("slack: %d commands configured, Slack allows %d per channel", len(commands), MaxCommands)
	}
	seen := make(map[string]bool, len(commands))
	for _, cmd := range commands {
		switch {
		case cmd.Name == "" || len(cmd.Name) > maxCommandName:
			return fmt.Errorf("slack: command name %q must be 1 to %d characters", cmd.Name, maxCommandName)
		case strings.HasPrefix(cmd.Name, "/") || strings.ContainsAny(cmd.Name, " \t\n"):
			return fmt.Errorf("slack: command name %q must have no leading slash and no whitespace", cmd.Name)
		case cmd.Description == "":
			return fmt.Errorf("slack: command %q needs a description", cmd.Name)
		case seen[cmd.Name]:
			return fmt.Errorf("slack: command %q is configured twice", cmd.Name)
		}
		seen[cmd.Name] = true
	}
	return nil
}

func errorCode(err error) string {
	var e *transport.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
