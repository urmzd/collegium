package platform_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/artifacts"
	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/runs"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/saige/agent/agenttest"
	saigetypes "github.com/urmzd/saige/agent/types"
)

func helperCall(messages []any, format map[string]any) []saigetypes.Delta {
	return agenttest.ToolCallResponse("helper-call", platform.ToolCallAgent, map[string]any{"agent": "helper", "messages": messages, "response_format": format})
}
func inputMessage(role, text string) any {
	return map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": text}}}
}
func modelFor(t *testing.T, c *cast, name string) *agenttest.ScriptedProvider {
	t.Helper()
	v, ok := c.models.Load(name)
	if !ok {
		t.Fatalf("no model for %s", name)
	}
	return v.(*agenttest.ScriptedProvider)
}
func TestHelperReceivesConstructedMessagesAndReturnsOneResultToItsCaller(t *testing.T) {
	c := &cast{says: map[string][][]saigetypes.Delta{
		"lead":   {helperCall([]any{inputMessage("user", "Example question"), inputMessage("assistant", "Example summary"), inputMessage("user", "Inspect the tests")}, map[string]any{"type": "summary", "max_words": 8}), say("Three tests need attention.")},
		"helper": {say("Three tests fail. Investigate fixtures.")},
	}, spawn: map[string][]string{"lead": {"helper"}}}
	p := stage(t, c, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}})
	first, err := p.Converse(t.Context(), tenant, platform.Turn{ID: "explicit-call", Agent: "lead", Text: "PRIVATE PARENT CONTEXT"})
	if err != nil {
		t.Fatal(err)
	}
	lead := await(t, p, first.ID, runs.Completed)
	helper := called(t, p, lead.ID, "helper", runs.Completed)
	conversation, err := p.Runs.Conversation(tenant, lead.Thread)
	if err != nil || len(conversation) != 1 || conversation[0].ID != lead.ID {
		t.Fatalf("helper became a user-facing turn: %+v, %v", conversation, err)
	}
	if helper.Input == nil || len(helper.Input.Messages) != 3 {
		t.Fatal("helper input is missing from run details")
	}
	requests := modelFor(t, c, "helper").Requests()
	if len(requests) != 1 {
		t.Fatalf("helper made %d calls", len(requests))
	}
	for _, tool := range requests[0].Tools {
		if tool.Name == platform.ToolCallAgent {
			t.Fatal("helper without a spawn grant received call_agent")
		}
	}
	matchedCall := false
	for _, event := range p.Journal.Events(tenant, []string{lead.ID}) {
		matchedCall = matchedCall || (event.Kind == envelope.KindActionTaken && strings.Contains(string(event.Payload), `"tool_call_id":"helper-call"`))
	}
	if !matchedCall {
		t.Fatal("helper run was not correlated with its tool call ID")
	}
	msgs := requests[0].Messages
	if len(msgs) != 4 {
		t.Fatalf("helper input has %d messages: %+v", len(msgs), msgs)
	}
	if msgs[0].Role() != saigetypes.RoleSystem || !strings.Contains(saigetypes.TextOf(msgs[0]), "within 8 words") {
		t.Fatalf("return instruction missing: %+v", msgs[0])
	}
	if msgs[2].Role() != saigetypes.RoleAssistant || saigetypes.TextOf(msgs[2]) != "Example summary" {
		t.Fatal("example role or text changed")
	}
	for _, m := range msgs {
		if strings.Contains(saigetypes.TextOf(m), "PRIVATE PARENT CONTEXT") {
			t.Fatal("parent context was inherited")
		}
	}
	parentRequests := modelFor(t, c, "lead").Requests()
	if len(parentRequests) != 2 {
		t.Fatalf("parent made %d model calls", len(parentRequests))
	}
	sawResult := false
	for _, m := range parentRequests[1].Messages {
		for _, r := range saigetypes.Each[saigetypes.ToolResultPart](m) {
			if strings.Contains(fmt.Sprint(r.Parts), helper.Answer) {
				sawResult = true
			}
		}
	}
	if !sawResult {
		t.Fatal("caller never received helper result")
	}
	if lead.Answer != "Three tests need attention." {
		t.Fatalf("parent answer = %q", lead.Answer)
	}
	for _, tool := range parentRequests[0].Tools {
		if strings.HasPrefix(tool.Name, "tag_") {
			t.Fatal("per-target tool leaked beside unified call_agent")
		}
	}
}

func TestInvalidHelperResultFailsWithoutPublishingAnAnswer(t *testing.T) {
	c := &cast{says: map[string][][]saigetypes.Delta{
		"lead":   {helperCall([]any{inputMessage("user", "Return a brief summary")}, map[string]any{"type": "summary", "max_words": 2}), say("The helper result failed validation.")},
		"helper": {say("This is longer than two words.")},
	}, spawn: map[string][]string{"lead": {"helper"}}}
	p := stage(t, c, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}})
	first, err := p.Converse(t.Context(), tenant, platform.Turn{ID: "bad-format", Agent: "lead", Text: "Start"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, first.ID, runs.Completed)
	helper := called(t, p, first.ID, "helper", runs.Failed)
	if !strings.Contains(helper.Error, "summary exceeds 2 words") {
		t.Fatalf("helper error = %q", helper.Error)
	}
	for _, e := range p.Journal.Events(tenant, []string{helper.ID}) {
		if e.Kind == envelope.KindText || e.Kind == envelope.KindCompleted {
			t.Fatalf("invalid answer published as %s", e.Kind)
		}
	}
}

func TestCallReferencesAreTenantScopedAndFrozenBeforeExecution(t *testing.T) {
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"source": {say("Reference answer")}, "lead": {say("Parent answer")}, "helper": {say("Understood")}},
		spawn: map[string][]string{"lead": {"helper"}},
		gates: map[string]chan struct{}{"lead": make(chan struct{}), "helper": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 1}})
	source, err := p.Converse(t.Context(), tenant, platform.Turn{ID: "source", Agent: "source", Text: "Reference question"})
	if err != nil {
		t.Fatal(err)
	}
	source = await(t, p, source.ID, runs.Completed)
	info, err := p.Blobs.Put(t.Context(), tenant, "reference.txt", strings.NewReader("ORIGINAL ARTIFACT"), 100)
	if err != nil {
		t.Fatal(err)
	}
	art, err := p.Artifacts.Publish(tenant, artifacts.Artifact{ID: "reference", Key: info.Key, Title: "ref.txt", Thread: source.Thread, Kind: artifacts.Document, Size: info.Size})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := p.Converse(t.Context(), tenant, platform.Turn{ID: "reference-parent", Agent: "lead", Text: "PARENT SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, parent.ID, runs.Running)
	if err := p.Runs.Record("foreign", envelope.Tag{ID: "foreign", Tenant: "other", Agent: "source", Origin: runs.OriginAPI, Text: "OTHER TENANT SECRET"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Artifacts.Publish("other", artifacts.Artifact{ID: "foreign-artifact", Key: "secret.txt", Title: "secret.txt", Thread: "foreign", Kind: artifacts.Document}); err != nil {
		t.Fatal(err)
	}
	inputFor := func(part call.Part) *call.Input {
		return &call.Input{Messages: []call.Message{{Role: "user", Content: []call.Part{part}}}, ResponseFormat: call.TextFormat{}}
	}
	for _, tc := range []struct {
		name string
		part call.Part
		want error
	}{
		{"missing run", call.RunResultPart{Run: "missing"}, runs.ErrNotFound},
		{"foreign run", call.RunResultPart{Run: "foreign"}, runs.ErrNotFound},
		{"foreign artifact", call.ArtifactPart{ID: "foreign-artifact", Version: 1}, artifacts.ErrNotFound},
		{"wrong version", call.ArtifactPart{ID: art.ID, Version: 2}, call.ErrInvalid},
		{"wrong thread", call.ConversationRange{Thread: "wrong", From: source.ID, Through: source.ID}, call.ErrInvalid},
		{"unrelated endpoints", call.ConversationRange{Thread: source.Thread, From: parent.ID, Through: source.ID}, call.ErrInvalid},
		{"running result", call.RunResultPart{Run: parent.ID}, call.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Call(t.Context(), tenant, parent.ID, call.Request{Key: tc.name, Agent: "helper", Mode: call.Subagent, Input: inputFor(tc.part)})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	input := &call.Input{Messages: []call.Message{{Role: "user", Content: []call.Part{
		call.TextPart{Text: "Inspect these references"},
		call.ConversationRange{Thread: source.Thread, From: source.ID, Through: source.ID},
		call.RunResultPart{Run: source.ID}, call.ArtifactPart{ID: art.ID, Version: 1},
	}}}, ResponseFormat: call.TextFormat{}}
	request := call.Request{Key: "frozen", Agent: "helper", Mode: call.Subagent, Input: input}
	child, err := p.Call(t.Context(), tenant, parent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	// Change and then remove the original reference after acceptance. Neither
	// execution nor an idempotent replay may resolve it again.
	if _, err := p.Blobs.Put(t.Context(), tenant, info.Key, strings.NewReader("CHANGED ARTIFACT"), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Artifacts.Delete(tenant, art.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := p.Call(t.Context(), tenant, parent.ID, request)
	if err != nil || replay.ID != child.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	conflict := request
	conflict.Input = inputFor(call.TextPart{Text: "Different request"})
	if _, err := p.Call(t.Context(), tenant, parent.ID, conflict); !errors.Is(err, call.ErrInvalid) {
		t.Fatalf("changed input reused call ID: %v", err)
	}
	c.release("helper")
	await(t, p, child.ID, runs.Completed)
	messages := modelFor(t, c, "helper").Requests()[0].Messages
	var seen strings.Builder
	for _, m := range messages {
		seen.WriteString(saigetypes.TextOf(m))
	}
	for _, text := range []string{"Inspect these references", "Reference question", "Reference answer", "ORIGINAL ARTIFACT"} {
		if !strings.Contains(seen.String(), text) {
			t.Errorf("resolved input missing %q", text)
		}
	}
	for _, text := range []string{"CHANGED ARTIFACT", "PARENT SECRET", "OTHER TENANT SECRET"} {
		if strings.Contains(seen.String(), text) {
			t.Errorf("input contains %q", text)
		}
	}
	var frozen call.Prepared
	if err := json.Unmarshal([]byte(child.Tag().Meta[call.MetaInput]), &frozen); err != nil {
		t.Fatal(err)
	}
	if _, ok := frozen.Input.Messages[0].Content[3].(call.ArtifactPart); !ok {
		t.Fatal("reference provenance was lost")
	}
	c.release("lead")
	await(t, p, parent.ID, runs.Completed)
}
