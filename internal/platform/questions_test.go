package platform_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/urmzd/mandatum/pkg/agentspec"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/saige/agent/agenttest"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/runs"
)

// People of the tests. ada asks for every run.
var (
	olu      = auth.Principal{Subject: "olu", Tenant: tenant, Kind: auth.User, Roles: []auth.Role{auth.Operator}}
	pat      = auth.Principal{Subject: "pat", Tenant: tenant, Kind: auth.User, Roles: []auth.Role{auth.Operator, "oncall"}}
	grace    = auth.Principal{Subject: "grace", Tenant: tenant, Kind: auth.User, Roles: []auth.Role{auth.Operator}}
	vic      = auth.Principal{Subject: "vic", Tenant: tenant, Kind: auth.User, Roles: []auth.Role{auth.Viewer}}
	stranger = auth.Principal{Subject: "olu", Tenant: "globex", Kind: auth.User, Roles: []auth.Role{auth.Admin}}
)

func asking(question string, options ...string) []saigetypes.Delta {
	args := map[string]any{"question": question}
	if len(options) > 0 {
		list := make([]any, len(options))
		for i, o := range options {
			list[i] = o
		}
		args["options"] = list
	}
	return agenttest.ToolCallResponse("call-ask", "ask_person", args)
}

func asked(args map[string]any) []saigetypes.Delta {
	return agenttest.ToolCallResponse("call-ask", "ask_person", args)
}

func writing(path, content string) []saigetypes.Delta {
	return agenttest.ToolCallResponse("call-write", platform.ToolFileWrite, map[string]any{"path": path, "content": content})
}

// questionFor waits for the question a run is parked on to reach the inbox.
func questionFor(t *testing.T, p *platform.Platform, runID string) inbox.Message {
	t.Helper()
	var found inbox.Message
	within(t, "the question reached the inbox", func() bool {
		for _, kind := range []inbox.Kind{inbox.KindApproval, inbox.KindClarification} {
			for _, m := range p.Inbox.OpenOf(tenant, kind) {
				if m.Source.Run == runID {
					found = m
					return true
				}
			}
		}
		return false
	})
	return found
}

// sees reports whether a person's inbox lists a message.
func sees(p *platform.Platform, who auth.Principal, id string) bool {
	page, _, _ := p.InboxPage(who, inbox.Query{State: inbox.All})
	listed := slices.ContainsFunc(page, func(m inbox.Message) bool { return m.ID == id })
	_, err := p.InboxGet(who, id)
	return listed && err == nil
}

func outcomeOf(t *testing.T, p *platform.Platform, id string) *inbox.Outcome {
	t.Helper()
	msgs, _ := p.Inbox.Snapshot(tenant)
	for _, m := range msgs {
		if m.ID == id {
			return m.Parts[0].Outcome
		}
	}
	t.Fatalf("no message %s", id)
	return nil
}

func ask(t *testing.T, p *platform.Platform, id, agent string) runs.Run {
	t.Helper()
	r, err := p.Converse(t.Context(), tenant, platform.Turn{ID: id, Agent: agent, Text: "Go", Actor: envelope.Actor{ID: admin.Subject}})
	if err != nil {
		t.Fatal(err)
	}
	return await(t, p, r.ID, runs.Parked)
}

// Each kind of question becomes one inbox message, in the inboxes of those who
// may answer it and nowhere else: approvers by role, answerers by name, and
// whom the agent asked of, but never the person who asked for an approval.
func TestEachQuestionKindReachesOnlyThoseWhoMayAnswer(t *testing.T) {
	p := stage(t, &cast{continues: true,
		says: map[string][][]saigetypes.Delta{
			"deployer": {writing("plan.md", "ship"), say("Written.")},
			"picker":   {asking("Which colour?", "red", "blue"), say("Done.")},
			"asker":    {asked(map[string]any{"question": "What is the ticket?", "asked_of": "oncall"}), say("Done.")},
		},
		tools: map[string][]string{"deployer": {platform.ToolFileWrite}, "picker": {"ask_person"}, "asker": {"ask_person"}},
		shape: map[string]func(*agentspec.AgentSpec){
			"deployer": func(s *agentspec.AgentSpec) {
				s.Approvals, s.Approvers = []string{platform.ToolFileWrite}, []string{"role:operator", "ada"}
			},
			"picker": func(s *agentspec.AgentSpec) { s.Answerers = []string{"grace"} },
		},
	}, platform.Config{})

	cases := []struct {
		agent       string
		kind        inbox.Kind
		part        inbox.PartType
		see, notSee []auth.Principal
	}{
		// Operators approve; ada is listed but asked for the call herself.
		{"deployer", inbox.KindApproval, inbox.PartApproval, []auth.Principal{olu, pat, grace}, []auth.Principal{admin, vic, stranger}},
		{"picker", inbox.KindClarification, inbox.PartChoice, []auth.Principal{grace}, []auth.Principal{admin, olu, pat, vic, stranger}},
		{"asker", inbox.KindClarification, inbox.PartReply, []auth.Principal{pat}, []auth.Principal{admin, olu, grace, vic, stranger}},
	}
	for _, c := range cases {
		t.Run(c.agent, func(t *testing.T) {
			run := ask(t, p, "api-"+c.agent, c.agent)
			if run.Question == nil || run.Status != runs.Parked {
				t.Fatalf("the parked run carries no question: %+v", run)
			}
			m := questionFor(t, p, run.ID)
			q := m.Parts[0].Question
			if m.Kind != c.kind || m.Parts[0].Type != c.part || q == nil || q.Run != run.ID || q.ID != run.Question.ID || q.ExpiresAt.IsZero() {
				t.Fatalf("message = %+v", m)
			}
			for _, who := range c.see {
				if !sees(p, who, m.ID) {
					t.Errorf("%s/%s does not see the question", who.Tenant, who.Subject)
				}
			}
			for _, who := range c.notSee {
				if sees(p, who, m.ID) {
					t.Errorf("%s/%s sees a question that is not theirs", who.Tenant, who.Subject)
				}
			}
			if got, _ := p.InboxGet(c.see[0], m.ID); len(got.Parts[0].Actions) == 0 {
				t.Errorf("someone who may answer is offered no action: %+v", got.Parts[0])
			}
			if _, err := p.StopRun(t.Context(), admin, run.ID); err != nil {
				t.Fatal(err)
			}
			await(t, p, run.ID, runs.Stopped)
		})
	}
}

// Answering resumes the run with the answer and settles the message in place,
// with who answered and what, and the inbox's record says so.
func TestAnsweringResumesTheRunAndSettlesTheMessage(t *testing.T) {
	c := &cast{continues: true,
		says: map[string][][]saigetypes.Delta{
			"deployer": {writing("plan.md", "ship it"), say("Written.")},
			"picker":   {asking("Which colour?", "red", "blue"), say("Blue it is.")},
			"asker":    {asking("What is the ticket?"), say("Filed.")},
		},
		tools: map[string][]string{"deployer": {platform.ToolFileWrite}, "picker": {"ask_person"}, "asker": {"ask_person"}},
		shape: map[string]func(*agentspec.AgentSpec){
			"deployer": func(s *agentspec.AgentSpec) { s.Approvals = []string{platform.ToolFileWrite} },
		},
	}
	p := stage(t, c, platform.Config{})
	ctx := t.Context()

	cases := []struct {
		agent  string
		answer platform.QuestionAnswer
		action string
		note   string
	}{
		{"deployer", platform.QuestionAnswer{Action: platform.ActApprove}, platform.ActApprove, ""},
		{"picker", platform.QuestionAnswer{Action: platform.ActChoose, Choice: "blue"}, platform.ActChoose, "blue"},
		{"asker", platform.QuestionAnswer{Action: platform.ActReply, Text: "OPS-42"}, platform.ActReply, "OPS-42"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			run := ask(t, p, "api-"+tc.agent, tc.agent)
			m := questionFor(t, p, run.ID)
			got, err := p.AnswerQuestion(ctx, olu, run.ID, run.Question.ID, tc.answer)
			if err != nil {
				t.Fatal(err)
			}
			if o := got.Parts[0].Outcome; o == nil || o.Action != tc.action || o.By != "olu" || o.Note != tc.note || got.State != inbox.Resolved {
				t.Fatalf("settled as %+v (%s)", got.Parts[0].Outcome, got.State)
			}
			done := await(t, p, run.ID, runs.Completed)
			if done.Question != nil {
				t.Fatal("a completed run still carries its question")
			}
			resumed := false
			for _, e := range p.Journal.Events(tenant, []string{run.ID}) {
				resumed = resumed || (e.Kind == envelope.KindResumed && strings.Contains(string(e.Payload), `"by":"olu"`))
			}
			if !resumed {
				t.Fatal("the run's stream has no lifecycle.resumed with the answer")
			}
			history := p.Inbox.History(tenant, m.ID)
			if len(history) != 2 || history[1].What != inbox.DidResolve || history[1].By != "olu" || history[1].Action != tc.action {
				t.Fatalf("history = %+v", history)
			}
		})
	}
	// The model was sent what was answered.
	sent := strings.Join(c.requests("picker"), "\n")
	if !strings.Contains(sent, "blue") {
		t.Fatalf("the picker's model never saw the choice: %s", sent)
	}
	if !strings.Contains(strings.Join(c.requests("asker"), "\n"), "OPS-42") {
		t.Fatal("the asker's model never saw the reply")
	}
}

// By default the person whose request raised a call cannot approve it, through
// any path, and nothing changes when they try. Someone else can.
func TestARequesterCannotApproveTheirOwnCall(t *testing.T) {
	p := stage(t, &cast{continues: true,
		says:  map[string][][]saigetypes.Delta{"deployer": {writing("plan.md", "ship"), say("Written.")}},
		tools: map[string][]string{"deployer": {platform.ToolFileWrite}},
		shape: map[string]func(*agentspec.AgentSpec){"deployer": func(s *agentspec.AgentSpec) { s.Approvals = []string{platform.ToolFileWrite} }},
	}, platform.Config{})
	ctx := t.Context()
	run := ask(t, p, "api-1", "deployer")
	m := questionFor(t, p, run.ID)

	if _, err := p.AnswerQuestion(ctx, admin, run.ID, run.Question.ID, platform.QuestionAnswer{Action: platform.ActApprove}); !errors.Is(err, platform.ErrForbidden) {
		t.Fatalf("the requester approving = %v, want forbidden", err)
	}
	// Through the inbox the question does not exist for them.
	if _, err := p.InboxResolve(ctx, admin, m.ID, "p1", platform.Resolution{Action: platform.ActApprove}); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("the requester resolving = %v, want not found", err)
	}
	if got, _ := p.Runs.Get(tenant, run.ID); got.Status != runs.Parked {
		t.Fatalf("after a refused approval the run is %s", got.Status)
	}
	if n := len(p.Inbox.History(tenant, m.ID)); n != 1 {
		t.Fatalf("a refused approval was recorded: %d changes", n)
	}
	if _, err := p.InboxResolve(ctx, olu, m.ID, "p1", platform.Resolution{Action: platform.ActApprove}); err != nil {
		t.Fatal(err)
	}
	await(t, p, run.ID, runs.Completed)
}

// Someone a question is not for, or who may not resume runs, is refused, and
// neither the run nor the message changes.
func TestADisallowedPersonIsRefusedAndNothingChanges(t *testing.T) {
	p := stage(t, &cast{continues: true,
		says:  map[string][][]saigetypes.Delta{"picker": {asking("Which colour?", "red", "blue"), say("Done.")}},
		tools: map[string][]string{"picker": {"ask_person"}},
		shape: map[string]func(*agentspec.AgentSpec){"picker": func(s *agentspec.AgentSpec) { s.Answerers = []string{"grace", "vic"} }},
	}, platform.Config{})
	ctx := t.Context()
	run := ask(t, p, "api-1", "picker")
	m := questionFor(t, p, run.ID)
	choose := platform.QuestionAnswer{Action: platform.ActChoose, Choice: "red"}

	if _, err := p.AnswerQuestion(ctx, olu, run.ID, run.Question.ID, choose); !errors.Is(err, platform.ErrForbidden) {
		t.Fatalf("someone not among the answerers = %v, want forbidden", err)
	}
	// vic is named, but a viewer may not resume a run.
	if _, err := p.AnswerQuestion(ctx, vic, run.ID, run.Question.ID, choose); !errors.Is(err, platform.ErrForbidden) {
		t.Fatalf("a viewer = %v, want forbidden", err)
	}
	if got, _ := p.InboxGet(vic, m.ID); len(got.Parts[0].Actions) != 0 {
		t.Fatalf("a viewer is offered %v", got.Parts[0].Actions)
	}
	if _, err := p.AnswerQuestion(ctx, grace, run.ID, run.Question.ID, platform.QuestionAnswer{Action: platform.ActChoose, Choice: "green"}); !errors.Is(err, platform.ErrAction) {
		t.Fatalf("an option not offered = %v, want a bad request", err)
	}
	if got, _ := p.Runs.Get(tenant, run.ID); got.Status != runs.Parked {
		t.Fatalf("the run is %s after refusals", got.Status)
	}
	if n := len(p.Inbox.History(tenant, m.ID)); n != 1 {
		t.Fatalf("refusals were recorded: %d changes", n)
	}
	if _, err := p.AnswerQuestion(ctx, grace, run.ID, run.Question.ID, choose); err != nil {
		t.Fatal(err)
	}
	await(t, p, run.ID, runs.Completed)
}

// Two people answering at once: one answer is recorded, and both are shown it.
func TestASecondAnswerShowsTheFirstOutcome(t *testing.T) {
	p := stage(t, &cast{continues: true,
		says:  map[string][][]saigetypes.Delta{"picker": {asking("Which colour?", "red", "blue"), say("Done.")}},
		tools: map[string][]string{"picker": {"ask_person"}},
	}, platform.Config{})
	ctx := t.Context()
	run := ask(t, p, "api-1", "picker")
	m := questionFor(t, p, run.ID)

	var wg sync.WaitGroup
	got := make([]inbox.Message, 2)
	errs := make([]error, 2)
	for i, who := range []auth.Principal{olu, grace} {
		wg.Go(func() {
			got[i], errs[i] = p.AnswerQuestion(ctx, who, run.ID, run.Question.ID, platform.QuestionAnswer{Action: platform.ActChoose, Choice: []string{"red", "blue"}[i]})
		})
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
	}
	a, b := got[0].Parts[0].Outcome, got[1].Parts[0].Outcome
	if a == nil || b == nil || a.By != b.By || a.Note != b.Note {
		t.Fatalf("the two answerers were shown different outcomes: %+v and %+v", a, b)
	}
	// A third, after it settled, is shown the same.
	late, err := p.AnswerQuestion(ctx, pat, run.ID, run.Question.ID, platform.QuestionAnswer{Action: platform.ActChoose, Choice: "red"})
	if err != nil {
		t.Fatal(err)
	}
	if o := late.Parts[0].Outcome; o == nil || o.By != a.By || o.Note != a.Note {
		t.Fatalf("a late answer was shown %+v", o)
	}
	await(t, p, run.ID, runs.Completed)
	if n := len(p.Inbox.History(tenant, m.ID)); n != 2 {
		t.Fatalf("history has %d changes, want raised and one resolve", n)
	}
}

// Nobody answers: the question takes its default, the run carries on with it,
// and the message says nobody answered in time.
func TestAnExpiredQuestionSettlesAsNobodyAnsweredAndTheRunContinues(t *testing.T) {
	c := &cast{continues: true,
		says: map[string][][]saigetypes.Delta{
			"picker":   {asked(map[string]any{"question": "Which colour?", "options": []any{"red", "blue"}, "default": "blue"}), say("Went with blue.")},
			"deployer": {writing("plan.md", "ship"), say("Not written.")},
		},
		tools: map[string][]string{"picker": {"ask_person"}, "deployer": {platform.ToolFileWrite}},
		shape: map[string]func(*agentspec.AgentSpec){"deployer": func(s *agentspec.AgentSpec) { s.Approvals = []string{platform.ToolFileWrite} }},
	}
	p := stage(t, c, platform.Config{QuestionTTL: 300 * time.Millisecond})

	picked := ask(t, p, "api-1", "picker")
	pm := questionFor(t, p, picked.ID)
	await(t, p, picked.ID, runs.Completed)
	if o := outcomeOf(t, p, pm.ID); o == nil || o.Action != platform.OutcomeExpired || !strings.Contains(o.Note, "blue") {
		t.Fatalf("an expired choice settled as %+v", o)
	}
	if !strings.Contains(strings.Join(c.requests("picker"), "\n"), "blue") {
		t.Fatal("the model was not given the default")
	}

	denied := ask(t, p, "api-2", "deployer")
	dm := questionFor(t, p, denied.ID)
	await(t, p, denied.ID, runs.Completed)
	if o := outcomeOf(t, p, dm.ID); o == nil || o.Action != platform.OutcomeExpired || !strings.Contains(o.Note, "denied") {
		t.Fatalf("an expired approval settled as %+v", o)
	}
	if _, err := p.AnswerQuestion(t.Context(), olu, denied.ID, denied.Question.ID, platform.QuestionAnswer{Action: platform.ActApprove}); err != nil {
		t.Fatalf("answering a settled question = %v, want the message as it stands", err)
	}
}

// Stopping a run that waits on a person settles its question as stopped, and
// the question then takes no answer.
func TestStoppingAParkedRunSettlesItsQuestion(t *testing.T) {
	p := stage(t, &cast{continues: true,
		says:  map[string][][]saigetypes.Delta{"picker": {asking("Which colour?", "red", "blue"), say("Done.")}},
		tools: map[string][]string{"picker": {"ask_person"}},
	}, platform.Config{})
	ctx := t.Context()
	run := ask(t, p, "api-1", "picker")
	m := questionFor(t, p, run.ID)
	if _, err := p.StopRun(ctx, admin, run.ID); err != nil {
		t.Fatal(err)
	}
	await(t, p, run.ID, runs.Stopped)
	within(t, "the question settled", func() bool {
		o := outcomeOf(t, p, m.ID)
		return o != nil && o.Action == platform.OutcomeStopped && strings.Contains(o.Note, "ada")
	})
	got, err := p.AnswerQuestion(ctx, olu, run.ID, run.Question.ID, platform.QuestionAnswer{Action: platform.ActChoose, Choice: "red"})
	if err != nil || got.Parts[0].Outcome.Action != platform.OutcomeStopped {
		t.Fatalf("answering a stopped run's question = %+v, %v", got.Parts[0].Outcome, err)
	}
}

// A question asked by a run another run is waiting on lands in the inboxes of
// those who may answer the called agent, shows on the caller's stream, and
// answering it lets both finish. The called run's usage is rolled up once.
func TestADelegatedChildsQuestionReachesTheRightInbox(t *testing.T) {
	p := stage(t, &cast{continues: true,
		says: map[string][][]saigetypes.Delta{
			"lead":   {tagging("helper", "Pick a colour"), say("The helper picked.")},
			"helper": {asking("Which colour?", "red", "blue"), say("Red.")},
		},
		spawn: map[string][]string{"lead": {"helper"}},
		tools: map[string][]string{"helper": {"ask_person"}},
		shape: map[string]func(*agentspec.AgentSpec){"helper": func(s *agentspec.AgentSpec) { s.Answerers = []string{"grace"} }},
	}, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}})
	ctx := t.Context()
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Go", Actor: envelope.Actor{ID: admin.Subject}})
	if err != nil {
		t.Fatal(err)
	}
	helper := called(t, p, first.ID, "helper", runs.Parked)
	m := questionFor(t, p, helper.ID)
	if !sees(p, grace, m.ID) || sees(p, olu, m.ID) || sees(p, admin, m.ID) {
		t.Fatal("the called agent's question is not addressed by its own answerers")
	}
	within(t, "the caller's stream shows the question", func() bool {
		for _, e := range p.Journal.Events(tenant, []string{first.ID}) {
			if e.Kind == envelope.KindChildParked && strings.Contains(string(e.Payload), helper.ID) {
				return true
			}
		}
		return false
	})
	if _, err := p.AnswerQuestion(ctx, grace, helper.ID, helper.Question.ID, platform.QuestionAnswer{Action: platform.ActChoose, Choice: "red"}); err != nil {
		t.Fatal(err)
	}
	await(t, p, helper.ID, runs.Completed)
	lead := await(t, p, first.ID, runs.Completed)
	if lead.Answer != "The helper picked." {
		t.Fatalf("lead answered %q", lead.Answer)
	}
	children := 0
	for _, e := range p.Journal.Events(tenant, []string{first.ID}) {
		if e.Kind == envelope.KindUsageChild {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("the called run was rolled up %d times", children)
	}
	if lead.Usage == nil || lead.Usage.Children.ModelCalls != 2 || lead.Usage.Own.ModelCalls != 2 {
		t.Fatalf("lead usage = %+v", lead.Usage)
	}
}

// A run that parks and resumes counts each model call once.
func TestUsageIsCountedOnceAcrossAPark(t *testing.T) {
	p := stage(t, &cast{continues: true,
		says:  map[string][][]saigetypes.Delta{"picker": {asking("Which colour?", "red", "blue"), say("Done.")}},
		tools: map[string][]string{"picker": {"ask_person"}},
	}, platform.Config{})
	run := ask(t, p, "api-1", "picker")
	if _, err := p.AnswerQuestion(t.Context(), olu, run.ID, run.Question.ID, platform.QuestionAnswer{Action: platform.ActChoose, Choice: "red"}); err != nil {
		t.Fatal(err)
	}
	done := await(t, p, run.ID, runs.Completed)
	if done.Usage == nil || done.Usage.Own.ModelCalls != 2 || done.Usage.Total.ModelCalls != 2 {
		t.Fatalf("usage = %+v", done.Usage)
	}
	calls := 0
	for _, e := range p.Journal.Events(tenant, []string{run.ID}) {
		if e.Kind == envelope.KindUsageCall {
			calls++
		}
	}
	if calls != 2 {
		t.Fatalf("%d usage.call events for two model calls", calls)
	}
}

// Another workspace's question does not exist: not to read, not to answer.
func TestQuestionsStayInTheirWorkspace(t *testing.T) {
	p := stage(t, &cast{continues: true,
		says:  map[string][][]saigetypes.Delta{"picker": {asking("Which colour?", "red", "blue"), say("Done.")}},
		tools: map[string][]string{"picker": {"ask_person"}},
	}, platform.Config{})
	ctx := t.Context()
	run := ask(t, p, "api-1", "picker")
	m := questionFor(t, p, run.ID)
	if _, err := p.AnswerQuestion(ctx, stranger, run.ID, run.Question.ID, platform.QuestionAnswer{Action: platform.ActChoose, Choice: "red"}); !errors.Is(err, runs.ErrNotFound) {
		t.Fatalf("another workspace answering = %v, want not found", err)
	}
	if _, err := p.InboxResolve(ctx, stranger, m.ID, "p1", platform.Resolution{Action: platform.ActChoose, Choice: "red"}); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("another workspace resolving = %v, want not found", err)
	}
	if got, _ := p.Runs.Get(tenant, run.ID); got.Status != runs.Parked {
		t.Fatalf("the run is %s", got.Status)
	}
	if _, err := p.StopRun(ctx, admin, run.ID); err != nil {
		t.Fatal(err)
	}
	await(t, p, run.ID, runs.Stopped)
}

// A Slack app answers for a Slack person only as the credential its client
// names, and only for a person the client maps. Anyone else is refused.
func TestASurfaceAnswersOnlyForAMappedPerson(t *testing.T) {
	f := &fakes{}
	cfg := f.config()
	p := stage(t, &cast{continues: true,
		says:  map[string][][]saigetypes.Delta{"picker": {asking("Which colour?", "red", "blue"), say("Done.")}},
		tools: map[string][]string{"picker": {"ask_person"}},
		shape: map[string]func(*agentspec.AgentSpec){"picker": func(s *agentspec.AgentSpec) { s.Answerers = []string{"role:operator"} }},
	}, cfg)
	in := slackClient("app", "A0APP", team, "picker")
	in.Slack.Machine, in.Slack.People = "svc-slack", map[string]string{"U0OLU": "olu", "U0VIC": "vic"}
	create(t, p, tenant, in)
	// The workspace's policy grants olu the operator role by subject, which
	// is all that is known of a person a surface answers for.
	pol, err := auth.ParsePolicy("test", `
role viewer { allow read on * }
role operator extends viewer { allow write on runs }
grant operator to subject "olu"
`)
	if err != nil {
		t.Fatal(err)
	}
	machine := auth.Principal{Subject: "svc-slack", Tenant: tenant, Kind: auth.Machine, Scopes: []auth.Permission{auth.RunsRead, auth.RunsWrite}}.WithPolicy(pol)
	other := auth.Principal{Subject: "svc-other", Tenant: tenant, Kind: auth.Machine, Scopes: []auth.Permission{auth.RunsRead, auth.RunsWrite}}
	ctx := t.Context()
	run := ask(t, p, "api-1", "picker")
	m := questionFor(t, p, run.ID)
	choose := func(as *platform.ActAs) platform.QuestionAnswer {
		return platform.QuestionAnswer{Action: platform.ActChoose, Choice: "red", ActAs: as}
	}

	refused := map[string]struct {
		who auth.Principal
		as  *platform.ActAs
	}{
		"an unmapped person":                 {machine, &platform.ActAs{Client: "app", User: "U0NOBODY"}},
		"another credential":                 {other, &platform.ActAs{Client: "app", User: "U0OLU"}},
		"a client that does not exist":       {machine, &platform.ActAs{Client: "nope", User: "U0OLU"}},
		"a mapped person who may not answer": {machine, &platform.ActAs{Client: "app", User: "U0VIC"}},
		"the machine as itself":              {machine, nil},
	}
	for name, r := range refused {
		if _, err := p.AnswerQuestion(ctx, r.who, run.ID, run.Question.ID, choose(r.as)); !errors.Is(err, platform.ErrForbidden) {
			t.Errorf("%s = %v, want forbidden", name, err)
		}
	}
	if n := len(p.Inbox.History(tenant, m.ID)); n != 1 {
		t.Fatalf("refusals were recorded: %d changes", n)
	}
	got, err := p.InboxResolve(ctx, machine, m.ID, "p1", platform.Resolution{Action: platform.ActChoose, Choice: "red", ActAs: &platform.ActAs{Client: "app", User: "U0OLU"}})
	if err != nil {
		t.Fatal(err)
	}
	if o := got.Parts[0].Outcome; o == nil || o.By != "olu" || !strings.Contains(o.Note, "slack:U0OLU") {
		t.Fatalf("answered for olu as %+v", o)
	}
	await(t, p, run.ID, runs.Completed)
}
