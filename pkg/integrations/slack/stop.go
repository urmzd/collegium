package slack

import (
	"context"
	"errors"
	"sync"

	"github.com/urmzd/mandatum/pkg/address"
	transport "github.com/urmzd/mandatum/pkg/connectors/slack"
	"github.com/urmzd/mandatum/pkg/envelope"
)

// StopFunc stops one run at the request of the Slack user who pressed stop.
// Stopping a run that has already ended is not an error. It is an alias so a
// composition root can ask for the capability without importing this package.
type StopFunc = func(ctx context.Context, runID, slackUser string) error

// maxRuns bounds how many open and stopped runs the connector remembers.
// Forgetting an open run costs its stop button; forgetting a stopped one lets
// the rest of its answer through.
const maxRuns = 4096

// live maps Slack sessions to the runs answering in them, which is what turns
// "stop this session" into "stop these runs". It is in memory, like the sink's
// message handles: a restart forgets which runs a session holds.
type live struct {
	mu      sync.Mutex
	open    map[string]session
	stopped map[string]bool
	stop    StopFunc
}

// session is where one run is answering.
type session struct{ workspace, channel, thread string }

func sessionOf(a address.Address) session {
	thread, _ := a.Param("thread")
	return session{workspace: a.Workspace, channel: a.Path[0], thread: thread}
}

// OnStop sets what stops a run. Until it is set a stop still settles the
// session's status, but no run is told.
func (c *Connector) OnStop(fn StopFunc) {
	c.live.mu.Lock()
	defer c.live.mu.Unlock()
	c.live.stop = fn
}

// track records a run's lifecycle and reports whether the run was stopped from
// Slack, in which case nothing more of it belongs on the surface.
func (l *live) track(to address.Address, e envelope.Event) (discard bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped[e.RunID] {
		return true
	}
	switch e.Kind {
	case envelope.KindAccepted, envelope.KindStarted, envelope.KindResumed, envelope.KindParked:
		if len(l.open) >= maxRuns {
			clear(l.open)
		}
		l.open[e.RunID] = sessionOf(to)
	case envelope.KindCompleted, envelope.KindFailed:
		delete(l.open, e.RunID)
	}
	return false
}

// in lists the runs open in a session. A code channel is one session, so a
// stop without a thread covers every run in the channel.
func (l *live) in(s session) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var ids []string
	for id, at := range l.open {
		if at.workspace == s.workspace && at.channel == s.channel && (s.thread == "" || at.thread == s.thread) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (l *live) settle(runID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.open, runID)
	if len(l.stopped) >= maxRuns {
		clear(l.stopped)
	}
	l.stopped[runID] = true
}

// stopped handles a person pressing stop on a session, in the order Slack asks
// for: stop the work, say so, move the status off processing. Slack does none
// of that itself.
//
// A run is marked stopped only once the platform accepted the stop, so a
// failure leaves it stoppable and Slack's redelivery tries again.
func (c *Connector) stopped(ctx context.Context, s transport.Stop) error {
	at := sessionOf(s.Session)
	c.live.mu.Lock()
	stop := c.live.stop
	c.live.mu.Unlock()

	var failed error
	halted := 0
	for _, id := range c.live.in(at) {
		if stop != nil {
			if err := stop(ctx, id, s.User); err != nil {
				failed = errors.Join(failed, err)
				continue
			}
		}
		c.live.settle(id)
		halted++
	}
	if failed != nil {
		return failed
	}

	if halted > 0 {
		_, err := c.cfg.API.PostMessage(ctx, transport.Message{
			Workspace: at.workspace, Channel: at.channel, ThreadTS: at.thread,
			Text: "Stopped by <@" + s.User + ">.",
		})
		if err := c.refused(err, "stop confirmation", s.Session, ""); err != nil {
			return err
		}
	}
	if !c.cfg.Sessions {
		return nil
	}
	err := c.cfg.API.(transport.AgentAPI).SetStatus(ctx, Session{
		Workspace: at.workspace, Channel: at.channel, ThreadTS: at.thread, Status: StatusActive,
	})
	return c.refused(err, "session status", s.Session, "")
}
