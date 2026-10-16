package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/runtime"
	saigetypes "github.com/urmzd/saige/agent/types"
)

// ToolCallAgent is available whenever the pinned agent has a spawn grant.
// The same tool can wait for a helper or start independent background work.
const ToolCallAgent = "call_agent"

func callParameters(targets []string) saigetypes.ParameterSchema {
	str := func(description string) saigetypes.PropertyDef {
		return saigetypes.PropertyDef{Type: "string", Description: description}
	}
	part := saigetypes.PropertyDef{Type: "object", Required: []string{"type"}, Properties: map[string]saigetypes.PropertyDef{
		"type": {Type: "string", Enum: []string{"text", "conversation_range", "artifact", "run_result"}, Description: "Discriminated content: text requires text; conversation_range requires thread/from/through run IDs; artifact requires id/version; run_result requires run. Other variant fields are rejected."},
		"text": str("Inline instructions, context, or an example"), "thread": str("Conversation ID"), "from": str("First turn's run ID, inclusive"), "through": str("Last turn's run ID, inclusive"),
		"id": str("Artifact ID"), "version": {Type: "integer", Description: "Exact artifact version, at least 1"}, "run": str("Completed run ID"),
	}}
	message := saigetypes.PropertyDef{Type: "object", Required: []string{"role", "content"}, Properties: map[string]saigetypes.PropertyDef{
		"role": {Type: "string", Enum: []string{"user", "assistant"}}, "content": {Type: "array", Items: &part},
	}}
	return saigetypes.ParameterSchema{Type: "object", Required: []string{"agent", "messages", "response_format"}, Properties: map[string]saigetypes.PropertyDef{
		"agent":    {Type: "string", Enum: targets},
		"messages": {Type: "array", Items: &message, Description: "Ordered input, including examples and selected context. Nothing is inherited from your conversation. End with a user message. References are resolved and frozen before the helper starts."},
		"response_format": {Type: "object", Required: []string{"type"}, Description: "Closed union: text accepts only max_chars (default 16000); summary accepts only max_words (default 200); json_schema requires schema. The harness generates the return instruction and validates the result.", Properties: map[string]saigetypes.PropertyDef{
			"type": {Type: "string", Enum: []string{"text", "summary", "json_schema"}}, "max_chars": {Type: "integer"}, "max_words": {Type: "integer"}, "schema": {Type: "object", Description: "JSON Schema checked by saige; unsupported keywords are rejected"},
		}},
		"mode":  {Type: "string", Enum: []string{string(call.Subagent), string(call.Delegate)}, Description: "subagent (default) waits and returns the helper result to you. delegate starts independent work and returns a run reference; inspect its result later in execution history or the inbox."},
		"lease": {Type: "integer", Description: "Budget in micro-USD. Required to be positive when this run has a metered lease."},
	}}
}

func (p *Platform) callAgentTool(parent runtime.Request, report func(payload.Action) error) saigetypes.Tool {
	return &saigetypes.ToolFunc{
		Def: saigetypes.ToolDef{Name: ToolCallAgent, Description: "Ask an allowed agent to perform helper work. Construct its complete input from ordered messages, examples and references. Choose the result format you need. You retain ownership of the user-facing answer. Use subagent to receive its result, or delegate for background work. Targets: " + strings.Join(parent.Revision.Spec.Access.Spawn, ", "), Parameters: callParameters(parent.Revision.Spec.Access.Spawn)},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			b, err := json.Marshal(args)
			if err != nil {
				return "", err
			}
			if len(b) > call.MaxInputBytes {
				return "", fmt.Errorf("%w: call input is too large", call.ErrInvalid)
			}
			var wire struct {
				Agent    string          `json:"agent"`
				Messages []call.Message  `json:"messages"`
				Format   json.RawMessage `json:"response_format"`
				Mode     call.Mode       `json:"mode,omitempty"`
				Lease    int64           `json:"lease,omitempty"`
			}
			if err := call.StrictDecode(b, &wire); err != nil {
				return "", err
			}
			// Decode through Input so the return contract's discriminator is checked
			// by the same codec used by HTTP and the durable snapshot.
			raw, err := json.Marshal(struct {
				Messages []call.Message  `json:"messages"`
				Format   json.RawMessage `json:"response_format"`
			}{wire.Messages, wire.Format})
			if err != nil {
				return "", err
			}
			var input call.Input
			if err := call.StrictDecode(raw, &input); err != nil {
				return "", err
			}
			if wire.Mode == "" {
				wire.Mode = call.Subagent
			}
			if wire.Mode != call.Subagent && wire.Mode != call.Delegate {
				return "", fmt.Errorf("%w: helper mode must be subagent or delegate", call.ErrInvalid)
			}
			key := callKey(string(wire.Mode), wire.Agent, string(raw)+fmt.Sprintf("\nlease:%d", wire.Lease))
			if info, ok := saigetypes.ToolCallInfoFromContext(ctx); ok && info.ID != "" {
				key = info.ID
			}
			req := call.Request{Key: key, Agent: wire.Agent, Input: &input, Mode: wire.Mode, Lease: wire.Lease}
			if wire.Mode == call.Subagent {
				return p.waitForCall(ctx, parent, req, ToolCallAgent, report)
			}
			child, err := p.Call(ctx, parent.Tenant, parent.RunID, req)
			if err != nil {
				return "", err
			}
			tell(ctx, report, ToolCallAgent, child, "Background helper started")
			data, err := json.Marshal(calledRun{Run: child.ID, Agent: child.Agent, Lifecycle: child.Lifecycle})
			return string(data), err
		},
	}
}
