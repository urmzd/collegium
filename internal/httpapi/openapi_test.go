package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	gotypesconst "go/constant"
	"go/importer"
	"go/parser"
	gotoken "go/token"
	"go/types"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/agentspec"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/runtime"
	"go.yaml.in/yaml/v4"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/alerts"
	"github.com/urmzd/collegium/pkg/artifacts"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/clients"
	"github.com/urmzd/collegium/pkg/evals"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/journal"
	"github.com/urmzd/collegium/pkg/knowledge"
	"github.com/urmzd/collegium/pkg/memory"
	"github.com/urmzd/collegium/pkg/pick"
	"github.com/urmzd/collegium/pkg/policy"
	"github.com/urmzd/collegium/pkg/prefs"
	"github.com/urmzd/collegium/pkg/runs"
	"github.com/urmzd/collegium/pkg/usage"
)

// The API contract is api/openapi.yaml. These tests are what keep it true:
// the routes it lists are the routes the server registers, each with the
// permission the server checks, its schemas have the fields the Go types
// marshal, and real responses validate against it.

const specPath = "../../api/openapi.yaml"

// Permission words the spec uses for routes that check no named permission.
const (
	permPublic        = "public"
	permAuthenticated = "authenticated"
)

type specOp struct {
	method, path string
	raw          map[string]any
	// schemas is the compiled body schema per documented status code. A
	// code with no JSON body maps to nil.
	schemas map[string]*jsonschema.Schema
}

func (o specOp) key() string { return o.method + " " + o.path }

type apiSpec struct {
	doc map[string]any
	c   *jsonschema.Compiler
	ops map[string]specOp
}

var httpMethods = []string{"get", "put", "post", "delete", "patch", "head", "options"}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }

// loadSpec reads the contract and compiles every response body schema in it.
func loadSpec(t *testing.T) *apiSpec {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var viaYAML any
	if err := yaml.Unmarshal(raw, &viaYAML); err != nil {
		t.Fatalf("the spec is not YAML: %v", err)
	}
	// Through JSON, so every value is one the schema compiler expects.
	asJSON, err := json.Marshal(viaYAML)
	if err != nil {
		t.Fatalf("the spec does not survive JSON: %v", err)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		t.Fatal(err)
	}
	s := &apiSpec{doc: obj(parsed), ops: map[string]specOp{}}
	closeObjects(s.doc)

	// Response schemas are copied under one flat key each, so they compile
	// by a pointer with no path template in it. Their $refs still resolve:
	// they point into this same document.
	hoisted := map[string]any{}
	s.doc["x-compiled"] = hoisted
	type pending struct {
		op   string
		code string
		at   string
	}
	var todo []pending
	for path, item := range obj(s.doc["paths"]) {
		for _, m := range httpMethods {
			raw := obj(obj(item)[m])
			if raw == nil {
				continue
			}
			op := specOp{method: strings.ToUpper(m), path: path, raw: raw, schemas: map[string]*jsonschema.Schema{}}
			for code, res := range obj(raw["responses"]) {
				op.schemas[code] = nil
				if ref, isRef := obj(res)["$ref"].(string); isRef {
					res = obj(obj(obj(s.doc["components"])["responses"])[strings.TrimPrefix(ref, "#/components/responses/")])
				}
				schema, has := obj(obj(obj(res)["content"])["application/json"])["schema"]
				if !has {
					continue
				}
				at := "s" + strconv.Itoa(len(hoisted))
				hoisted[at] = schema
				todo = append(todo, pending{op.key(), code, at})
			}
			s.ops[op.key()] = op
		}
	}
	s.c = jsonschema.NewCompiler()
	s.c.AssertFormat()
	s.c.AssertContent()
	if err := s.c.AddResource("openapi.json", s.doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range todo {
		compiled, err := s.c.Compile("openapi.json#/x-compiled/" + p.at)
		if err != nil {
			t.Fatalf("%s %s: the response schema does not compile: %v", p.op, p.code, err)
		}
		s.ops[p.op].schemas[p.code] = compiled
	}
	return s
}

// closeObjects makes every object schema in the test's copy of the spec
// refuse properties it does not name, so a key added to a handler's map or
// inline struct fails validation instead of reaching clients unnoticed. Only
// this copy changes: the published spec stays open, as JSON Schema is by
// default, so a client keeps accepting fields added later.
//
// A schema used as a member of an allOf is left open, because it sees only
// its own properties; the schema that composes it is closed instead, and
// unevaluatedProperties there counts what every member evaluated.
func closeObjects(doc map[string]any) {
	members := map[string]bool{}
	var collect func(v any)
	collect = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, part := range sliceOf(x["allOf"]) {
				if ref, isRef := obj(part)["$ref"].(string); isRef {
					members[ref] = true
				}
			}
			for _, child := range x {
				collect(child)
			}
		case []any:
			for _, child := range x {
				collect(child)
			}
		}
	}
	collect(doc)
	var walk func(v any, member bool)
	walk = func(v any, member bool) {
		switch x := v.(type) {
		case map[string]any:
			_, hasProps := x["properties"]
			_, hasExtra := x["additionalProperties"]
			_, composes := x["allOf"]
			if !member && !hasExtra && (hasProps || composes) {
				x["unevaluatedProperties"] = false
			}
			for key, child := range x {
				walk(child, key == "allOf")
			}
		case []any:
			for _, child := range x {
				// The elements of an allOf are its members.
				walk(child, member)
			}
		}
	}
	for name, schema := range obj(obj(doc["components"])["schemas"]) {
		walk(schema, members["#/components/schemas/"+name])
	}
	walk(doc["paths"], false)
}

func (s *apiSpec) component(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	compiled, err := s.c.Compile("openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatalf("schema %s does not compile: %v", name, err)
	}
	return compiled
}

type route struct{ method, path, permission string }

// registeredRoutes reads the route table out of this package's source, with
// types: every call of (*http.ServeMux).Handle or HandleFunc, whatever the
// receiver is called, with the permission its (*Server).authed is given. It
// fails on anything it cannot resolve to a constant (a pattern or permission
// computed at run time, a method value taken from a mux, a route on
// http.DefaultServeMux, an authed handler not passed straight to a mux), so a
// route cannot slip past the contract by being written differently.
func registeredRoutes(t *testing.T) []route {
	t.Helper()
	fset := gotoken.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0) //nolint:staticcheck // one directory of syntax is all this needs
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: exportImporter(t, fset)}
	if _, err := conf.Check("github.com/urmzd/collegium/internal/httpapi", fset, files, info); err != nil {
		t.Fatalf("type-checking the package: %v", err)
	}

	isFunc := func(obj types.Object, names ...string) bool {
		fn, ok := obj.(*types.Func)
		return ok && slices.Contains(names, fn.FullName())
	}
	register := []string{"(*net/http.ServeMux).Handle", "(*net/http.ServeMux).HandleFunc", "net/http.Handle", "net/http.HandleFunc"}
	authed := "(*github.com/urmzd/collegium/internal/httpapi.Server).authed"
	constant := func(e ast.Expr) (string, bool) {
		tv := info.Types[e]
		if tv.Value == nil || tv.Value.Kind() != gotypesconst.String {
			return "", false
		}
		return gotypesconst.StringVal(tv.Value), true
	}

	// Every place the registering functions or authed are named, and which
	// of those are the callee of a call this reader understood.
	named := map[*ast.Ident]string{}
	for id, obj := range info.Uses {
		switch {
		case isFunc(obj, register...):
			named[id] = "a route registration"
		case isFunc(obj, authed):
			named[id] = "a use of authed"
		}
	}
	understood := map[*ast.Ident]bool{}
	callee := func(call *ast.CallExpr) *ast.Ident {
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			return fn.Sel
		case *ast.Ident:
			return fn
		}
		return nil
	}

	var out []route
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			id := callee(call)
			if id == nil || !isFunc(info.Uses[id], register...) {
				return true
			}
			where := fset.Position(call.Pos()).String()
			if isFunc(info.Uses[id], "net/http.Handle", "net/http.HandleFunc") {
				t.Fatalf("%s: a route on http.DefaultServeMux is not served by Server.Handler", where)
			}
			understood[id] = true
			pattern, ok := constant(call.Args[0])
			if !ok {
				t.Fatalf("%s: a route whose pattern is not a constant cannot be checked against the spec", where)
			}
			method, path, hasMethod := strings.Cut(pattern, " ")
			if !hasMethod {
				// "/api/" and "/" are fallbacks, not operations.
				return true
			}
			r := route{method: method, path: path, permission: permPublic}
			if inner, isCall := call.Args[1].(*ast.CallExpr); isCall {
				if aid := callee(inner); aid != nil && isFunc(info.Uses[aid], authed) {
					understood[aid] = true
					perm, ok := constant(inner.Args[0])
					if !ok {
						t.Fatalf("%s: the permission this route checks is not a constant", where)
					}
					r.permission = permAuthenticated
					if perm != "" {
						r.permission = perm
					}
				}
			}
			out = append(out, r)
			return true
		})
	}
	for id, what := range named {
		if !understood[id] {
			t.Errorf("%s: %s the route reader cannot follow. Register routes as mux.HandleFunc(\"METHOD /path\", s.authed(perm, h))", fset.Position(id.Pos()), what)
		}
	}
	if len(out) < 50 {
		t.Fatalf("read only %d routes from the source, so the reader is broken", len(out))
	}
	return out
}

// exportImporter imports packages from the compiler's export data, which
// `go list -export` builds or finds in the build cache. It is the standard
// library's own importer pointed at the module's dependencies, so reading the
// route table with types needs no dependency beyond the go command.
func exportImporter(t *testing.T, fset *gotoken.FileSet) types.Importer {
	t.Helper()
	cmd := exec.Command("go", "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}", ".")
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -export: %v", err)
	}
	exports := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if path, file, ok := strings.Cut(line, "="); ok && file != "" {
			exports[path] = file
		}
	}
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
}

// isIngress reports a route that Slack and GitHub call, which the client
// contract leaves out on purpose.
func isIngress(path string) bool { return strings.HasPrefix(path, "/connectors/") }

func TestSpecListsExactlyTheRegisteredRoutes(t *testing.T) {
	s := loadSpec(t)
	registered := map[string]route{}
	for _, r := range registeredRoutes(t) {
		if isIngress(r.path) {
			if _, documented := s.ops[r.method+" "+r.path]; documented {
				t.Errorf("%s %s is webhook ingress and does not belong in the client contract", r.method, r.path)
			}
			continue
		}
		registered[r.method+" "+r.path] = r
	}
	for key := range registered {
		if _, documented := s.ops[key]; !documented {
			t.Errorf("%s is registered and missing from %s", key, specPath)
		}
	}

	declared := map[string]bool{}
	for _, tag := range s.doc["tags"].([]any) {
		declared[obj(tag)["name"].(string)] = true
	}
	ids := map[string]string{}
	for key, op := range s.ops {
		r, exists := registered[key]
		if !exists {
			t.Errorf("%s is in the spec and the server registers no such route", key)
			continue
		}
		id, _ := op.raw["operationId"].(string)
		if id == "" {
			t.Errorf("%s has no operationId", key)
		}
		if other, taken := ids[id]; taken {
			t.Errorf("%s and %s share the operationId %q", key, other, id)
		}
		ids[id] = key
		tags, _ := op.raw["tags"].([]any)
		if len(tags) == 0 {
			t.Errorf("%s has no tag", key)
		}
		for _, tag := range tags {
			if !declared[tag.(string)] {
				t.Errorf("%s uses tag %q, which the spec does not declare", key, tag)
			}
		}
		if got, _ := op.raw["x-permission"].(string); got != r.permission {
			t.Errorf("%s: x-permission is %q, the route checks %q", key, got, r.permission)
		}
		security, overrides := op.raw["security"].([]any)
		if open := overrides && len(security) == 0; open != (r.permission == permPublic) {
			t.Errorf("%s: `security: []` must be set on public routes and only on them", key)
		}
		// oag v0.20.6 reads a query parameter whose schema is a $ref as an
		// object and writes it one character at a time, so the client would
		// send nonsense. Query enums are written inline until that is fixed.
		for _, p := range sliceOf(op.raw["parameters"]) {
			if _, isRef := obj(obj(p)["schema"])["$ref"]; isRef && obj(p)["in"] == "query" {
				t.Errorf("%s: query parameter %q has a $ref schema, which the client generator mishandles", key, obj(p)["name"])
			}
		}
		for _, name := range pathParams(op.path) {
			if !declaresPathParam(s, op, name) {
				t.Errorf("%s does not declare its path parameter %q", key, name)
			}
		}
	}

	var enum []string
	for _, v := range obj(obj(obj(s.doc["components"])["schemas"])["Permission"])["enum"].([]any) {
		enum = append(enum, v.(string))
	}
	var real []string
	for _, p := range auth.Permissions() {
		real = append(real, string(p))
	}
	sort.Strings(enum)
	sort.Strings(real)
	if !slices.Equal(enum, real) {
		t.Errorf("the Permission enum is %v, the server's permissions are %v", enum, real)
	}
}

func pathParams(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") {
			out = append(out, strings.Trim(seg, "{}"))
		}
	}
	return out
}

func declaresPathParam(s *apiSpec, op specOp, name string) bool {
	params, _ := op.raw["parameters"].([]any)
	for _, p := range params {
		param := obj(p)
		if ref, isRef := param["$ref"].(string); isRef {
			param = obj(obj(obj(s.doc["components"])["parameters"])[strings.TrimPrefix(ref, "#/components/parameters/")])
		}
		if param["in"] == "path" && param["name"] == name && param["required"] == true {
			return true
		}
	}
	return false
}

// The source says which permission a route names. This asks the running
// server, operation by operation, which permission it actually enforces.
func TestSpecPermissionsAreTheOnesTheServerEnforces(t *testing.T) {
	s := loadSpec(t)
	f := newFixture(t)
	nothing := f.machine(t, "acme")
	for key, op := range s.ops {
		perm, _ := op.raw["x-permission"].(string)
		if perm == permPublic {
			continue
		}
		path := op.path
		for _, name := range pathParams(path) {
			path = strings.ReplaceAll(path, "{"+name+"}", "x")
		}
		if res, _ := f.do(t, nil, op.method, path, "", ""); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a credential = %d, want 401", key, res.StatusCode)
		}
		res, out := f.do(t, nil, op.method, path, nothing, "{}")
		if perm == permAuthenticated {
			if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusUnauthorized {
				t.Errorf("%s is documented as open to any signed-in caller and answered %d", key, res.StatusCode)
			}
			continue
		}
		if want := "you need " + perm; res.StatusCode != http.StatusForbidden || out["error"] != want {
			t.Errorf("%s with no permissions = %d %q, want 403 %q", key, res.StatusCode, out["error"], want)
		}
	}
}

// jsonField is one field as encoding/json writes it.
type jsonField struct {
	name     string
	optional bool
}

// jsonFields lists a struct's JSON fields, flattening embedded structs the way
// encoding/json does.
func jsonFields(t reflect.Type) []jsonField {
	var out []jsonField
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if f.Anonymous && tag == "" {
			inner := f.Type
			if inner.Kind() == reflect.Pointer {
				inner = inner.Elem()
			}
			out = append(out, jsonFields(inner)...)
			continue
		}
		if !f.IsExported() || tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out = append(out, jsonField{name: name, optional: strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")})
	}
	return out
}

// schemaFields flattens a component schema's properties and required list,
// following allOf and local $refs.
func schemaFields(s *apiSpec, schema map[string]any, props map[string]bool, required map[string]bool) {
	if ref, isRef := schema["$ref"].(string); isRef {
		schemaFields(s, obj(obj(obj(s.doc["components"])["schemas"])[strings.TrimPrefix(ref, "#/components/schemas/")]), props, required)
	}
	for _, part := range sliceOf(schema["allOf"]) {
		schemaFields(s, obj(part), props, required)
	}
	for name := range obj(schema["properties"]) {
		props[name] = true
	}
	for _, name := range sliceOf(schema["required"]) {
		required[name.(string)] = true
	}
}

func sliceOf(v any) []any { s, _ := v.([]any); return s }

// A schema names the fields its Go type marshals, and requires exactly the
// ones the type always writes. This is what catches a field added to a struct
// and not to the contract.
func TestSpecSchemasHaveTheFieldsTheGoTypesMarshal(t *testing.T) {
	s := loadSpec(t)
	// written says the schema is something a caller sends: the server fills
	// in what is left out, so only the field names are compared.
	type shape struct {
		typ     any
		written bool
	}
	shapes := map[string]shape{
		"Principal": {typ: auth.Principal{}}, "AccessDecision": {typ: accessDecision{}}, "AccessHeldRole": {typ: heldRole{}}, "AgentSpec": {typ: agentspec.AgentSpec{}}, "AgentSource": {typ: agentspec.Source{}},
		"AgentAccess": {typ: agentspec.Access{}}, "Revision": {typ: agentspec.Revision{}}, "Choice": {typ: pick.Choice{}},
		"Note": {typ: memory.Note{}}, "Actor": {typ: envelope.Actor{}}, "Run": {typ: runs.Run{}},
		"ThreadSummary": {typ: runs.ThreadSummary{}}, "Project": {typ: platform.Project{}}, "ProjectView": {typ: projectView{}},
		"Settings": {typ: platform.Settings{}}, "Preferences": {typ: prefs.Preferences{}},
		"Schedule": {typ: schedule{}}, "ScheduleView": {typ: scheduleView{}},
		"InboxNotice": {typ: inbox.Notice{}}, "InboxFrom": {typ: inbox.From{}}, "InboxSource": {typ: inbox.Source{}},
		"InboxOutcome": {typ: inbox.Outcome{}}, "InboxLimits": {typ: inbox.Limits{}}, "InboxAlertPart": {typ: inbox.AlertPart{}},
		"InboxFailurePart": {typ: inbox.FailurePart{}}, "InboxNoteRef": {typ: inbox.NoteRef{}}, "InboxMemoryPart": {typ: inbox.MemoryPart{}},
		"InboxBudgetPart": {typ: inbox.BudgetPart{}}, "InboxPart": {typ: inbox.Part{}}, "InboxMessage": {typ: inbox.Message{}},
		"InboxChange": {typ: inbox.Change{}},
		"Artifact":    {typ: artifacts.Artifact{}}, "StorageShare": {typ: artifacts.Share{}},
		"AnalyticsGroup": {typ: runs.Group{}}, "AnalyticsBucket": {typ: runs.Bucket{}}, "Summary": {typ: runs.Summary{}},
		"JournalEntry": {typ: journal.Entry{}}, "Span": {typ: journal.Span{}},
		"AlertRule": {typ: alerts.Rule{}}, "Alert": {typ: alerts.Alert{}},
		"EvalCheck": {typ: evals.Check{}}, "EvalCase": {typ: evals.Case{}}, "EvalSuite": {typ: evals.Suite{}, written: true},
		"EvalVerdict": {typ: evals.Verdict{}}, "EvalHit": {typ: evals.Hit{}}, "EvalResult": {typ: evals.Result{}},
		"EvalRun": {typ: evals.Run{}}, "EvalSuiteView": {typ: suiteView{}, written: true},
		"KnowledgeVariant": {typ: knowledge.Variant{}}, "KnowledgeSource": {typ: knowledge.Source{}},
		"KnowledgeDocument": {typ: knowledge.Document{}}, "KnowledgeHit": {typ: knowledge.Hit{}},
		"PolicyScope": {typ: policy.Scope{}}, "Policy": {typ: policy.Policy{}}, "PolicyChange": {typ: policy.Change{}},
		"Measure": {typ: measure{}}, "ScopeUsage": {typ: scopeUsage{}}, "UsageEntry": {typ: usage.Entry{}},
		"ClientIdentity": {typ: clients.Identity{}}, "SecretRef": {typ: clients.SecretRef{}}, "SecretInput": {typ: clients.SecretInput{}},
		"SlackCommand": {typ: clients.Command{}}, "SlackOptions": {typ: clients.SlackOptions{}}, "GitHubOptions": {typ: clients.GitHubOptions{}},
		"ClientView": {typ: clientView{}}, "ClientCaps": {typ: clients.Caps{}}, "ClientInput": {typ: clientBody{}, written: true},
		"TextPayload": {typ: payload.Text{}}, "ThinkingPayload": {typ: payload.Thinking{}}, "ToolCallPayload": {typ: payload.ToolCall{}},
		"ToolDonePayload": {typ: payload.ToolDone{}}, "Provenance": {typ: payload.Provenance{}}, "CitationPayload": {typ: payload.Citation{}},
		"LifecyclePayload": {typ: payload.Lifecycle{}}, "ActionPayload": {typ: payload.Action{}},
		"StoppedPayload": {typ: payload.Stopped{}}, "Cost": {typ: payload.Cost{}}, "Usage": {typ: payload.Usage{}},
		"ContextUsage": {typ: payload.ContextUsage{}}, "RunUsage": {typ: payload.RunUsage{}},
		"UsageCallPayload": {typ: payload.UsageCall{}}, "UsageChildPayload": {typ: payload.UsageChild{}},
		"PolicyCost": {typ: policy.Cost{}}, "UsageSpend": {typ: usage.Spend{}}, "SpendMeasure": {typ: spendMeasure{}},
		"AnalyticsUsage": {typ: runs.GroupUsage{}}, "UsageCallEvent": {typ: envelope.Event{}},
		// Every variant of the event union is the same Go struct.
		"TextDeltaEvent": {typ: envelope.Event{}}, "RunCompletedEvent": {typ: envelope.Event{}}, "ActionTakenEvent": {typ: envelope.Event{}},
	}
	schemas := obj(obj(s.doc["components"])["schemas"])
	for name, sh := range shapes {
		schema := obj(schemas[name])
		if schema == nil {
			t.Errorf("the spec has no schema %s", name)
			continue
		}
		props, required := map[string]bool{}, map[string]bool{}
		schemaFields(s, schema, props, required)
		typ := reflect.TypeOf(sh.typ)
		seen := map[string]bool{}
		for _, f := range jsonFields(typ) {
			seen[f.name] = true
			if !props[f.name] {
				t.Errorf("%s: %s marshals %q and the schema has no such property", name, typ, f.name)
				continue
			}
			if !sh.written && required[f.name] == f.optional {
				t.Errorf("%s.%s: required is %v in the schema, and the Go field's omitempty is %v", name, f.name, required[f.name], f.optional)
			}
		}
		for prop := range props {
			if !seen[prop] {
				t.Errorf("%s: the schema has %q and %s has no such field", name, prop, typ)
			}
		}
	}

	// The event union names every kind the runtime declares.
	var kinds []string
	for _, v := range obj(schemas["EventKind"])["enum"].([]any) {
		kinds = append(kinds, v.(string))
	}
	declared := []string{
		string(envelope.KindText), string(envelope.KindThinking), string(envelope.KindToolCall), string(envelope.KindToolDone),
		string(envelope.KindCitation), string(envelope.KindAccepted), string(envelope.KindStarted), string(envelope.KindParked),
		string(envelope.KindResumed), string(envelope.KindCompleted), string(envelope.KindFailed), string(envelope.KindStopped), string(envelope.KindActionTaken),
		string(envelope.KindUsageCall), string(envelope.KindUsageChild),
		string(envelope.KindAgentCreated), string(envelope.KindAgentRevised), string(envelope.KindAgentDeleted),
	}
	if !slices.Equal(kinds, declared) {
		t.Errorf("EventKind is %v, the runtime declares %v", kinds, declared)
	}
	for _, union := range []string{"RunEvent", "ManifestEvent"} {
		mapping := obj(obj(obj(schemas[union])["discriminator"])["mapping"])
		for _, kind := range declared {
			if _, mapped := mapping[kind]; !mapped {
				t.Errorf("%s has no variant for kind %q", union, kind)
			}
		}
		if len(mapping) != len(declared) || len(sliceOf(obj(schemas[union])["oneOf"])) != len(declared) {
			t.Errorf("%s does not have exactly one variant per kind", union)
		}
	}
}

// contract checks real responses against the spec.
type contract struct {
	t    *testing.T
	f    *fixture
	spec *apiSpec
	seen map[string]bool
}

type reply struct {
	status int
	header http.Header
	raw    []byte
	body   any
}

func (r reply) field(name string) any { return obj(r.body)[name] }
func (r reply) str(name string) string {
	s, _ := r.field(name).(string)
	return s
}

// call sends a request to the operation named by its path template, and fails
// unless the answer has the wanted status, that status is documented for the
// operation, and the body validates against its schema.
func (c *contract) call(tok, method, template, path, body string, want int, hdr ...string) reply {
	c.t.Helper()
	out, _ := c.attempt(tok, method, template, path, body, want, true, hdr...)
	return out
}

// attempt is call that, when strict is false, reports a status other than the
// wanted one instead of failing, for a request that can lose a race.
func (c *contract) attempt(tok, method, template, path, body string, want int, strict bool, hdr ...string) (reply, bool) {
	c.t.Helper()
	op, known := c.spec.ops[method+" "+template]
	if !known {
		c.t.Fatalf("%s %s is not an operation in the spec", method, template)
	}
	if !matches(template, path) {
		c.t.Fatalf("%s is not an instance of %s", path, template)
	}
	req, err := http.NewRequest(method, c.f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := reply{status: res.StatusCode, header: res.Header, raw: raw}
	if res.StatusCode != want {
		if !strict {
			return out, false
		}
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, res.StatusCode, raw, want)
	}
	schema, documented := op.schemas[strconv.Itoa(res.StatusCode)]
	if !documented {
		c.t.Errorf("%s %s answered %d, which the spec does not document for it", method, template, res.StatusCode)
		return out, true
	}
	c.seen[op.key()+" "+strconv.Itoa(res.StatusCode)] = true
	if schema == nil {
		if res.StatusCode == http.StatusNoContent && len(raw) != 0 {
			c.t.Errorf("%s %s answered 204 with a body", method, path)
		}
		return out, true
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		c.t.Errorf("%s %s: content type is %q, the spec says application/json", method, path, ct)
	}
	out.body, err = jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		c.t.Fatalf("%s %s: the body is not JSON: %s", method, path, raw)
	}
	if err := schema.Validate(out.body); err != nil {
		c.t.Errorf("%s %s (%d) does not match the spec:\n%v\nbody: %s", method, path, res.StatusCode, err, raw)
	}
	return out, true
}

// callFromLiveRun makes a call from a run of lead that is still going. A
// run on the offline provider ends quickly, so a call can find its caller
// already over; a fresh caller is started until one is caught alive.
func (c *contract) callFromLiveRun(tok, body string, want int) (caller, called reply) {
	c.t.Helper()
	for range 20 {
		lead := c.call(tok, "POST", "/api/runs", "/api/runs", `{"agent":"lead","text":"Plan it"}`, http.StatusAccepted)
		if out, ok := c.attempt(tok, "POST", "/api/runs/{id}/calls", "/api/runs/"+lead.str("id")+"/calls", body, want, false); ok {
			return lead, out
		}
	}
	c.t.Fatalf("no call from a live run answered %d", want)
	return reply{}, reply{}
}

func matches(template, path string) bool {
	path, _, _ = strings.Cut(path, "?")
	want, got := strings.Split(template, "/"), strings.Split(path, "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if !strings.HasPrefix(want[i], "{") && want[i] != got[i] {
			return false
		}
	}
	return true
}

func needsQuery(op specOp) bool {
	for _, p := range sliceOf(op.raw["parameters"]) {
		if obj(p)["in"] == "query" && obj(p)["required"] == true {
			return true
		}
	}
	return false
}

// sweep reads every operation that is a GET with no parameters in its path and
// a JSON answer. Run on an empty workspace and on a busy one, it is what finds
// a list that is null when the spec says array.
func (c *contract) sweep(tok string) {
	c.t.Helper()
	for _, op := range c.spec.ops {
		if op.method != http.MethodGet || len(pathParams(op.path)) > 0 || op.schemas["200"] == nil || !strings.HasPrefix(op.path, "/api/") {
			continue
		}
		if op.path == "/api/events" || needsQuery(op) {
			continue
		}
		c.call(tok, "GET", op.path, op.path, "", http.StatusOK)
	}
}

func (c *contract) settled(tok, id string) reply {
	c.t.Helper()
	for range 400 {
		run := c.call(tok, "GET", "/api/runs/{id}", "/api/runs/"+id, "", http.StatusOK)
		if s := run.str("status"); s == "completed" || s == "failed" || s == "stopped" {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.t.Fatalf("run %s never ended", id)
	return reply{}
}

func TestResponsesMatchTheSpec(t *testing.T) {
	f := newFixture(t)
	c := &contract{t: t, f: f, spec: loadSpec(t), seen: map[string]bool{}}
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)

	// A workspace with nothing in it, then every refusal that needs no setup.
	c.sweep(globex)
	c.call("", "GET", "/api/me", "/api/me", "", http.StatusUnauthorized)
	c.call(f.machine(t, "acme"), "GET", "/api/agents", "/api/agents", "", http.StatusForbidden)
	c.call(f.machine(t, "acme"), "GET", "/api/me", "/api/me", "", http.StatusOK)
	c.call(f.machine(t, "acme"), "GET", "/api/access/explain", "/api/access/explain?permission=runs:read", "", http.StatusOK)
	c.call(acme, "GET", "/api/access/explain", "/api/access/explain?permission=runs:read", "", http.StatusOK)
	c.call(acme, "GET", "/api/access/explain", "/api/access/explain?permission=everything", "", http.StatusBadRequest)

	// Agents, revisions and what they remember.
	c.call(acme, "POST", "/api/agents", "/api/agents", helperSpec, http.StatusCreated)
	c.call(acme, "POST", "/api/agents", "/api/agents", helperSpec, http.StatusConflict)
	c.call(acme, "POST", "/api/agents", "/api/agents", `{"name":"helper","metadata":{}}`, http.StatusBadRequest)
	revised := `{"expected_rev":1,"spec":{"name":"helper","description":"Helps.","model":"offline","provider":"offline","system_prompt":"Help well."}}`
	c.call(acme, "PUT", "/api/agents/{name}", "/api/agents/helper", revised, http.StatusOK)
	c.call(acme, "PUT", "/api/agents/{name}", "/api/agents/helper", revised, http.StatusConflict)
	c.call(acme, "PUT", "/api/agents/{name}", "/api/agents/ghost", strings.ReplaceAll(revised, "helper", "ghost"), http.StatusNotFound)
	c.call(acme, "GET", "/api/agents/{name}/history", "/api/agents/helper/history", "", http.StatusOK)
	c.call(globex, "GET", "/api/agents/{name}/history", "/api/agents/helper/history", "", http.StatusNotFound)
	note := c.call(acme, "POST", "/api/agents/{name}/memory", "/api/agents/helper/memory", `{"text":"Answer briefly."}`, http.StatusCreated)
	c.call(acme, "GET", "/api/agents/{name}/memory", "/api/agents/helper/memory", "", http.StatusOK)
	c.call(acme, "POST", "/api/agents/{name}/memory/approve", "/api/agents/helper/memory/approve", `{"ids":[]}`, http.StatusOK)
	c.call(acme, "DELETE", "/api/agents/{name}/memory/{id}", "/api/agents/helper/memory/"+note.str("id"), "", http.StatusNoContent)
	c.call(acme, "GET", "/api/pick", "/api/pick?q=help+me", "", http.StatusOK)

	// What a Slack surface does: start a turn under its own idempotency key,
	// continue the conversation, read the run and its tree, and stop it.
	first := c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"Say hello"}`, http.StatusAccepted, "Idempotency-Key", "slack-evt-1")
	again := c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"Say hello"}`, http.StatusAccepted, "Idempotency-Key", "slack-evt-1")
	id := first.str("id")
	if id != again.str("id") || id != runtime.RunID("acme", "api-8-svc-acme-slack-evt-1") {
		t.Errorf("the same key gave runs %q and %q", id, again.str("id"))
	}
	if actor := obj(first.field("actor")); actor["bot"] != true {
		t.Errorf("a machine's run is not marked as a bot's: %v", actor)
	}
	c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"  "}`, http.StatusBadRequest)
	c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"hi","metadata":{"channel":"C1"}}`, http.StatusBadRequest)
	c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"ghost","text":"hi"}`, http.StatusNotFound)
	if done := c.settled(acme, id); done.str("status") != "completed" || done.str("answer") == "" {
		t.Fatalf("the first turn = %s", done.raw)
	}
	next := c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"And again","after":"`+id+`"}`, http.StatusAccepted)
	if next.str("thread") != first.str("thread") || next.str("prev") != id {
		t.Errorf("the follow-up is not in the first turn's conversation: %s", next.raw)
	}
	c.settled(acme, next.str("id"))
	c.call(acme, "GET", "/api/runs/{id}/tree", "/api/runs/"+id+"/tree", "", http.StatusOK)
	c.call(acme, "GET", "/api/runs/{id}/trace", "/api/runs/"+id+"/trace", "", http.StatusOK)
	c.call(acme, "GET", "/api/runs/{id}/events", "/api/runs/"+id+"/events", "", http.StatusOK)
	c.call(acme, "GET", "/api/runs/{id}/manifest", "/api/runs/"+id+"/manifest", "", http.StatusOK)
	c.call(acme, "GET", "/api/threads/{id}", "/api/threads/"+first.str("thread"), "", http.StatusOK)
	c.call(acme, "GET", "/api/threads", "/api/threads?limit=1", "", http.StatusOK)
	c.call(acme, "GET", "/api/threads", "/api/threads?limit=0", "", http.StatusBadRequest)
	c.call(acme, "GET", "/api/threads", "/api/threads?before=nonsense", "", http.StatusBadRequest)
	stopped := c.call(acme, "POST", "/api/runs/{id}/stop", "/api/runs/"+id+"/stop", "", http.StatusOK)
	if stopped.str("status") != "completed" {
		t.Errorf("stopping a run that had ended changed it: %s", stopped.raw)
	}
	// A run still working is asked and answered 202 while it stops. The
	// offline model can finish first, so a run that has already ended (200)
	// is tried again with a new one.
	asked := false
	for range 20 {
		live := c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"Stop me"}`, http.StatusAccepted)
		out, ok := c.attempt(acme, "POST", "/api/runs/{id}/stop", "/api/runs/"+live.str("id")+"/stop", "", http.StatusAccepted, false)
		end := c.settled(acme, live.str("id"))
		if !ok {
			continue
		}
		asked = true
		if out.field("stopping") != true {
			t.Errorf("a run asked to stop is not stopping: %s", out.raw)
		}
		if s := end.str("status"); s != "stopped" && s != "completed" {
			t.Errorf("a run asked to stop ended %s", end.raw)
		}
		break
	}
	if !asked {
		t.Error("no stop found a run still working")
	}
	c.call(acme, "POST", "/api/runs/{id}/calls", "/api/runs/"+id+"/calls", `{"key":"k1","agent":"helper","text":"hi","mode":"subagent"}`, http.StatusForbidden)
	c.call(acme, "POST", "/api/runs/{id}/calls", "/api/runs/"+id+"/calls", `{"mode":7}`, http.StatusBadRequest)
	c.call(acme, "POST", "/api/agents", "/api/agents", `{"name":"lead","model":"offline","provider":"offline","system_prompt":"Lead.","access":{"spawn":["helper"]}}`, http.StatusCreated)
	lead, called := c.callFromLiveRun(acme, `{"key":"k1","agent":"helper","text":"Do it","mode":"delegate"}`, http.StatusAccepted)
	if called.str("parent") != lead.str("id") || called.str("lifecycle") != "delegated" {
		t.Errorf("the called run is not under its caller: %s", called.raw)
	}
	c.settled(acme, called.str("id"))
	c.settled(acme, lead.str("id"))

	// Another workspace's run is not there, whatever is asked of it.
	for _, op := range []struct{ method, template, suffix string }{
		{"GET", "/api/runs/{id}", ""}, {"GET", "/api/runs/{id}/tree", "/tree"}, {"GET", "/api/runs/{id}/trace", "/trace"},
		{"GET", "/api/runs/{id}/events", "/events"}, {"GET", "/api/runs/{id}/manifest", "/manifest"}, {"POST", "/api/runs/{id}/stop", "/stop"},
	} {
		c.call(globex, op.method, op.template, "/api/runs/"+id+op.suffix, "", http.StatusNotFound)
	}
	c.call(globex, "GET", "/api/threads/{id}", "/api/threads/"+first.str("thread"), "", http.StatusNotFound)
	c.call(globex, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"hi","after":"`+id+`"}`, http.StatusNotFound)

	// Limits: a refusal, the inbox message it raises, and resolving it.
	c.call(acme, "POST", "/api/projects", "/api/projects", `{"name":"Launch","description":"The launch."}`, http.StatusCreated)
	c.call(acme, "POST", "/api/projects", "/api/projects", `{"name":"Launch"}`, http.StatusConflict)
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"tenant"},"period":"day","max_runs":100,"max_concurrent":50}`, http.StatusOK)
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"project","name":"Launch"},"period":"day","max_runs":1}`, http.StatusOK)
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"project","name":"Ghost"},"period":"day","max_runs":1}`, http.StatusNotFound)
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"tenant"},"period":"day"}`, http.StatusBadRequest)
	inProject := `{"agent":"helper","text":"go","project":"Launch"}`
	c.settled(acme, c.call(acme, "POST", "/api/runs", "/api/runs", inProject, http.StatusAccepted).str("id"))
	refused := c.call(acme, "POST", "/api/runs", "/api/runs", inProject, http.StatusTooManyRequests)
	if refused.str("limit") != "runs" || refused.field("resets_at") == nil || refused.field("policy") == nil {
		t.Errorf("the refusal does not say what refused it: %s", refused.raw)
	}
	c.call(acme, "GET", "/api/usage", "/api/usage", "", http.StatusOK)
	c.call(acme, "GET", "/api/usage/notices", "/api/usage/notices", "", http.StatusOK)

	budget := c.call(acme, "GET", "/api/inbox", "/api/inbox?kind=budget&state=open&limit=10", "", http.StatusOK)
	list := sliceOf(budget.field("messages"))
	if len(list) == 0 {
		t.Fatalf("the refusal raised no message: %s", budget.raw)
	}
	msg := obj(list[0])
	mid := msg["id"].(string)
	parts := map[string]string{}
	for _, p := range sliceOf(msg["parts"]) {
		parts[obj(p)["type"].(string)] = obj(p)["id"].(string)
	}
	inboxPath := "/api/inbox/" + mid
	c.call(acme, "GET", "/api/inbox", "/api/inbox?state=nonsense", "", http.StatusBadRequest)
	c.call(acme, "GET", "/api/inbox", "/api/inbox?alert="+obj(msg["source"])["alert"].(string), "", http.StatusOK)
	c.call(acme, "GET", "/api/inbox/count", "/api/inbox/count", "", http.StatusOK)
	c.call(acme, "GET", "/api/inbox/{id}", inboxPath, "", http.StatusOK)
	c.call(globex, "GET", "/api/inbox/{id}", inboxPath, "", http.StatusNotFound)
	c.call(acme, "POST", "/api/inbox/{id}/read", inboxPath+"/read", `{}`, http.StatusOK)
	c.call(acme, "POST", "/api/inbox/{id}/read", inboxPath+"/read", ``, http.StatusBadRequest)
	c.call(acme, "POST", "/api/inbox/{id}/snooze", inboxPath+"/snooze", `{"until":"2999-01-01T00:00:00Z"}`, http.StatusOK)
	resolve := "/api/inbox/{id}/parts/{part}/resolve"
	reader := f.machine(t, "acme", "collegium:runs:read", "collegium:alerts:read")
	c.call(reader, "POST", resolve, inboxPath+"/parts/"+parts["alert"]+"/resolve", `{"action":"acknowledge"}`, http.StatusForbidden)
	c.call(acme, "POST", resolve, inboxPath+"/parts/"+parts["budget"]+"/resolve", `{"action":"approve"}`, http.StatusBadRequest)
	c.call(acme, "POST", resolve, inboxPath+"/parts/p99/resolve", `{"action":"acknowledge"}`, http.StatusNotFound)
	c.call(globex, "POST", resolve, inboxPath+"/parts/"+parts["alert"]+"/resolve", `{"action":"acknowledge"}`, http.StatusNotFound)
	c.call(acme, "POST", resolve, inboxPath+"/parts/"+parts["budget"]+"/resolve", `{"action":"raise_limit","limits":{"max_runs":5},"note":"Launch week."}`, http.StatusOK)
	c.call(acme, "POST", resolve, inboxPath+"/parts/"+parts["alert"]+"/resolve", `{"action":"acknowledge"}`, http.StatusOK)
	c.call(acme, "GET", "/api/inbox/{id}", inboxPath, "", http.StatusOK)
	c.call(acme, "GET", "/api/inbox", "/api/inbox?state=all", "", http.StatusOK)
	c.call(acme, "POST", "/api/runs", "/api/runs", inProject, http.StatusAccepted)
	// A second refusal, to set aside rather than resolve.
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"agent","name":"helper"},"period":"day","max_concurrent":1,"max_runs":1}`, http.StatusOK)
	c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"over"}`, http.StatusTooManyRequests)
	open := c.call(acme, "GET", "/api/inbox", "/api/inbox", "", http.StatusOK)
	if left := sliceOf(open.field("messages")); len(left) > 0 {
		c.call(acme, "POST", "/api/inbox/{id}/dismiss", "/api/inbox/"+obj(left[0])["id"].(string)+"/dismiss", "", http.StatusOK)
	}
	c.call(acme, "DELETE", "/api/policies", "/api/policies?kind=agent&name=helper", "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/policies", "/api/policies?kind=agent&name=helper", "", http.StatusNotFound)

	// Projects and their memory.
	shared := c.call(acme, "POST", "/api/projects/{name}/memory", "/api/projects/Launch/memory", `{"text":"We ship on Friday."}`, http.StatusCreated)
	c.call(acme, "GET", "/api/projects/{name}/memory", "/api/projects/Launch/memory", "", http.StatusOK)
	c.call(acme, "GET", "/api/projects/{name}/memory", "/api/projects/Ghost/memory", "", http.StatusNotFound)
	c.call(acme, "POST", "/api/projects/{name}/memory/approve", "/api/projects/Launch/memory/approve", `{"ids":[]}`, http.StatusOK)
	c.call(acme, "DELETE", "/api/projects/{name}/memory/{id}", "/api/projects/Launch/memory/"+shared.str("id"), "", http.StatusNoContent)

	// Schedules.
	c.call(acme, "POST", "/api/schedules", "/api/schedules", `{"name":"nightly","expression":"0 3 * * *","agent":"helper","text":"Report.","project":"Launch"}`, http.StatusCreated)
	c.call(acme, "POST", "/api/schedules", "/api/schedules", `{"name":"nightly","expression":"0 3 * * *","agent":"helper","text":"Report."}`, http.StatusConflict)
	c.call(acme, "POST", "/api/schedules", "/api/schedules", `{"name":"bad","expression":"whenever","agent":"helper","text":"Report."}`, http.StatusBadRequest)
	c.call(acme, "POST", "/api/schedules", "/api/schedules", `{"name":"lost","expression":"0 3 * * *","agent":"ghost","text":"Report."}`, http.StatusNotFound)
	c.call(acme, "POST", "/api/schedules/{name}/pause", "/api/schedules/nightly/pause", "", http.StatusOK)
	c.call(acme, "POST", "/api/schedules/{name}/resume", "/api/schedules/nightly/resume", "", http.StatusOK)
	c.call(acme, "POST", "/api/schedules/{name}/run", "/api/schedules/nightly/run", "", http.StatusAccepted)
	c.call(globex, "POST", "/api/schedules/{name}/run", "/api/schedules/nightly/run", "", http.StatusNotFound)

	// Alerts, knowledge and evals.
	c.call(acme, "POST", "/api/alert-rules", "/api/alert-rules", `{"name":"any","kind":"run_failed","severity":"critical"}`, http.StatusCreated)
	c.call(acme, "POST", "/api/alert-rules", "/api/alert-rules", `{"name":"any","kind":"run_failed","severity":"critical"}`, http.StatusConflict)
	c.call(acme, "POST", "/api/alert-rules", "/api/alert-rules", `{"name":"odd","kind":"nonsense","severity":"critical"}`, http.StatusBadRequest)
	alertList := c.call(acme, "GET", "/api/alerts", "/api/alerts", "", http.StatusOK)
	if fired := sliceOf(alertList.field("alerts")); len(fired) > 0 {
		c.call(acme, "POST", "/api/alerts/{id}/ack", "/api/alerts/"+obj(fired[0])["id"].(string)+"/ack", "", http.StatusOK)
	}
	c.call(acme, "POST", "/api/alerts/{id}/ack", "/api/alerts/nope/ack", "", http.StatusNotFound)

	c.call(acme, "POST", "/api/knowledge", "/api/knowledge", `{"name":"handbook","description":"How we work."}`, http.StatusCreated)
	c.call(acme, "POST", "/api/knowledge", "/api/knowledge", `{"name":"handbook"}`, http.StatusConflict)
	c.call(acme, "GET", "/api/knowledge/{name}/documents", "/api/knowledge/handbook/documents", "", http.StatusOK)
	c.call(acme, "GET", "/api/knowledge/search", "/api/knowledge/search?q=zebra", "", http.StatusOK)
	c.call(acme, "GET", "/api/knowledge/search", "/api/knowledge/search?q=", "", http.StatusBadRequest)
	doc := c.call(acme, "POST", "/api/knowledge/{name}/documents", "/api/knowledge/handbook/documents", `{"title":"Refunds","uri":"https://example.com/refunds","text":"Refunds are issued within five days of a request."}`, http.StatusCreated)
	c.call(acme, "GET", "/api/knowledge/{name}/documents", "/api/knowledge/handbook/documents", "", http.StatusOK)
	c.call(globex, "GET", "/api/knowledge/{name}/documents", "/api/knowledge/handbook/documents", "", http.StatusNotFound)
	c.call(acme, "GET", "/api/knowledge/search", "/api/knowledge/search?q=refunds&source=handbook", "", http.StatusOK)
	c.call(acme, "POST", "/api/knowledge/{name}/variants", "/api/knowledge/handbook/variants", `{"name":"lean"}`, http.StatusCreated)
	docPath := "/api/knowledge/handbook/documents/" + doc.str("id")
	c.call(acme, "PUT", "/api/knowledge/{name}/documents/{id}", docPath, `{"excluded":true}`, http.StatusOK)

	c.call(acme, "POST", "/api/evals", "/api/evals", `{"name":"smoke","target":"agent","subject":"helper","cases":[{"id":"says-offline","input":"hello","checks":[{"kind":"contains","value":"offline provider"}]}]}`, http.StatusCreated)
	c.call(acme, "POST", "/api/evals", "/api/evals", `{"name":"lost","target":"agent","subject":"ghost","cases":[{"id":"a","input":"hello","checks":[{"kind":"contains","value":"x"}]}]}`, http.StatusNotFound)
	c.call(acme, "PUT", "/api/evals/{name}", "/api/evals/smoke", `{"name":"smoke","description":"The basics.","target":"agent","subject":"helper","cases":[{"id":"says-offline","input":"hello","checks":[{"kind":"contains","value":"offline provider"}]}]}`, http.StatusOK)
	c.call(acme, "POST", "/api/evals/{name}/run", "/api/evals/smoke/run", "", http.StatusAccepted)
	for range 400 {
		runs := c.call(acme, "GET", "/api/evals/{name}/runs", "/api/evals/smoke/runs", "", http.StatusOK)
		if latest := sliceOf(runs.field("runs")); len(latest) > 0 && obj(latest[0])["status"] != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.call(acme, "POST", "/api/evals/{name}/run", "/api/evals/nope/run", "", http.StatusNotFound)

	// Clients. No answer carries a secret, which clients_test.go checks.
	c.call(acme, "POST", "/api/clients", "/api/clients", slackBody("support", "A01CONTRACT", "helper"), http.StatusCreated)
	c.call(acme, "POST", "/api/clients", "/api/clients", slackBody("support", "A01CONTRACT", "helper"), http.StatusConflict)
	c.call(acme, "POST", "/api/clients", "/api/clients", `{"id":"odd","surface":"fax"}`, http.StatusBadRequest)
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"client","name":"support"},"period":"day","max_runs":3}`, http.StatusOK)
	c.call(acme, "GET", "/api/clients/{id}", "/api/clients/support", "", http.StatusOK)
	c.call(globex, "GET", "/api/clients/{id}", "/api/clients/support", "", http.StatusNotFound)
	c.call(acme, "PUT", "/api/clients/{id}", "/api/clients/support", `{"name":"Support","surface":"slack","identity":{"app_id":"A01CONTRACT","team":"T01ABCDEF"},"default_agent":"helper","agents":["*"],"slack":{"sessions":true},"enabled":false}`, http.StatusOK)

	// Settings and the caller's own preferences.
	settings := c.call(acme, "GET", "/api/settings", "/api/settings", "", http.StatusOK)
	c.call(acme, "PUT", "/api/settings", "/api/settings", string(settings.raw), http.StatusOK)
	c.call(acme, "PUT", "/api/settings", "/api/settings", `{"baseline":"ghost","history":"recent","history_limit":10,"projects":[]}`, http.StatusBadRequest)
	c.call(acme, "PUT", "/api/preferences", "/api/preferences", `{"display_name":"Service","theme":"dark","accent":"teal","onboarded":true}`, http.StatusOK)
	c.call(acme, "PUT", "/api/preferences", "/api/preferences", `{"theme":"neon"}`, http.StatusBadRequest)
	c.call(acme, "GET", "/api/preferences/photo", "/api/preferences/photo", "", http.StatusNotFound)
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32)
	c.call(acme, "PUT", "/api/preferences/photo", "/api/preferences/photo", png, http.StatusOK)
	if photo := c.call(acme, "GET", "/api/preferences/photo", "/api/preferences/photo", "", http.StatusOK); photo.header.Get("Content-Type") != "image/png" {
		t.Errorf("the picture is served as %q", photo.header.Get("Content-Type"))
	}
	c.call(acme, "PUT", "/api/preferences/photo", "/api/preferences/photo", "not a picture", http.StatusBadRequest)
	c.call(acme, "DELETE", "/api/preferences/photo", "/api/preferences/photo", "", http.StatusOK)

	// Published files.
	if err := f.p.PutFile("acme", first.str("thread"), "notes.txt", "Refunds fell by a third."); err != nil {
		t.Fatal(err)
	}
	published, err := f.p.Publish(t.Context(), platform.Publication{
		Tenant: "acme", Thread: first.str("thread"), Run: id, Agent: "helper", Project: "Launch", By: "agent:helper", Path: "notes.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := "/api/artifacts/" + published.ID
	c.call(acme, "GET", "/api/artifacts", "/api/artifacts?thread="+first.str("thread")+"&limit=5", "", http.StatusOK)
	c.call(acme, "GET", "/api/artifacts", "/api/artifacts?kind=nonsense", "", http.StatusBadRequest)
	c.call(acme, "GET", "/api/artifacts/{id}", artifactPath, "", http.StatusOK)
	c.call(globex, "GET", "/api/artifacts/{id}", artifactPath, "", http.StatusNotFound)
	c.call(acme, "GET", "/api/artifacts/{id}/versions", artifactPath+"/versions", "", http.StatusOK)
	if file := c.call(acme, "GET", "/api/artifacts/{id}/content", artifactPath+"/content", "", http.StatusOK); file.header.Get("Content-Type") != "application/octet-stream" || string(file.raw) != "Refunds fell by a third." {
		t.Errorf("the download is %q as %q", file.raw, file.header.Get("Content-Type"))
	}
	c.call(globex, "GET", "/api/artifacts/{id}/content", artifactPath+"/content", "", http.StatusNotFound)

	// The busy workspace, read end to end, then taken apart.
	c.call(acme, "GET", "/api/analytics", "/api/analytics?window=24h&bucket=1h", "", http.StatusOK)
	c.call(acme, "GET", "/api/analytics", "/api/analytics?window=9999h", "", http.StatusBadRequest)
	c.call(acme, "GET", "/api/journal", "/api/journal?run="+id+"&kind=lifecycle&limit=5", "", http.StatusOK)
	c.sweep(acme)

	for _, op := range c.spec.ops {
		if _, documents := op.schemas["400"]; !documents || obj(op.raw["requestBody"]) == nil || strings.Contains(op.path, "photo") {
			continue
		}
		path := op.path
		for _, name := range pathParams(op.path) {
			path = strings.ReplaceAll(path, "{"+name+"}", "x")
		}
		c.call(acme, op.method, op.path, path, `{"unknown_field":1}`, http.StatusBadRequest)
	}

	// The rest, one by one.
	c.call(acme, "DELETE", "/api/policies", "/api/policies?kind=nonsense", "", http.StatusNotFound)
	c.call(acme, "GET", "/api/journal", "/api/journal?window=forever", "", http.StatusBadRequest)
	c.call(acme, "GET", "/api/knowledge/search", "/api/knowledge/search?q=refunds&source=ghost", "", http.StatusNotFound)
	c.call(globex, "GET", "/api/pick", "/api/pick?q=anything", "", http.StatusBadRequest)
	c.call(acme, "POST", "/api/alert-rules", "/api/alert-rules", `{"name":"lost","kind":"run_failed","agent":"ghost","severity":"critical"}`, http.StatusNotFound)
	c.call(acme, "POST", "/api/evals", "/api/evals", `{"name":"smoke","target":"agent","subject":"helper","cases":[{"id":"a","input":"hi","checks":[{"kind":"contains","value":"x"}]}]}`, http.StatusConflict)
	c.call(globex, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"tenant"},"period":"day","max_clients":1}`, http.StatusOK)
	c.call(globex, "POST", "/api/clients", "/api/clients", slackBody("one", "A01ONE", ""), http.StatusCreated)
	c.call(globex, "POST", "/api/clients", "/api/clients", slackBody("two", "A01TWO", ""), http.StatusTooManyRequests)
	// A lease over the limit is a money limit, so it is refused with 402.
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"tenant"},"period":"day","max_runs":100,"max_concurrent":50,"max_lease":10}`, http.StatusOK)
	c.callFromLiveRun(acme, `{"key":"k402","agent":"helper","text":"hi","mode":"delegate","lease":1000}`, http.StatusPaymentRequired)
	// A workspace that has spent its spend limit refuses a new run with 402.
	// The offline provider prices nothing, so the spend is written to the
	// ledger as a priced call would be.
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"tenant"},"period":"day","max_runs":100,"max_concurrent":50,"spend_limit":5000}`, http.StatusOK)
	c.f.p.Usage.Append(usage.Entry{Kind: usage.Spent, Tenant: "acme", Agent: "helper", RunID: "r-spent", Key: "r-spent/a1/1",
		At: time.Now(), Spend: &usage.Spend{Micros: 5000, ModelCalls: 1, Tokens: 1000}})
	c.call(acme, "POST", "/api/runs", "/api/runs", `{"agent":"helper","text":"hi"}`, http.StatusPaymentRequired)
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"tenant"},"period":"day","max_runs":100,"max_concurrent":50,"spend_limit":1000000}`, http.StatusOK)
	// With the agent at its day's limit, everything that would start it is
	// told so.
	c.call(acme, "PUT", "/api/policies", "/api/policies", `{"scope":{"kind":"agent","name":"helper"},"period":"day","max_runs":1}`, http.StatusOK)
	c.call(acme, "POST", "/api/schedules/{name}/run", "/api/schedules/nightly/run", "", http.StatusTooManyRequests)
	c.callFromLiveRun(acme, `{"key":"k429","agent":"helper","text":"hi","mode":"delegate"}`, http.StatusTooManyRequests)
	c.call(acme, "DELETE", "/api/policies", "/api/policies?kind=agent&name=helper", "", http.StatusNoContent)

	// Two rules hold for every route that takes something: another
	// workspace's object is not found, and a body with a field the route
	// does not know is refused. Both are asked of every operation the spec
	// says can answer them, against the objects made above.
	ids := map[string]string{
		"name": "helper", "id": "x", "part": parts["alert"], "variant": "lean",
	}
	real := map[string]map[string]string{
		"/api/agents/{name}/memory/{id}": {"id": note.str("id")}, "/api/alert-rules/{name}": {"name": "any"},
		"/api/clients/{id}": {"id": "support"}, "/api/evals/{name}": {"name": "smoke"}, "/api/evals/{name}/runs": {"name": "smoke"},
		"/api/evals/{name}/run": {"name": "smoke"}, "/api/knowledge/{name}": {"name": "handbook"},
		"/api/knowledge/{name}/documents": {"name": "handbook"}, "/api/knowledge/{name}/documents/{id}": {"name": "handbook", "id": doc.str("id")},
		"/api/knowledge/{name}/variants": {"name": "handbook"}, "/api/knowledge/{name}/variants/{variant}": {"name": "handbook"},
		"/api/projects/{name}": {"name": "Launch"}, "/api/projects/{name}/memory": {"name": "Launch"},
		"/api/projects/{name}/memory/approve": {"name": "Launch"}, "/api/projects/{name}/memory/{id}": {"name": "Launch", "id": shared.str("id")},
		"/api/schedules/{name}": {"name": "nightly"}, "/api/schedules/{name}/pause": {"name": "nightly"},
		"/api/schedules/{name}/resume": {"name": "nightly"}, "/api/schedules/{name}/run": {"name": "nightly"},
		"/api/inbox/{id}": {"id": mid}, "/api/inbox/{id}/read": {"id": mid}, "/api/inbox/{id}/dismiss": {"id": mid},
		"/api/inbox/{id}/snooze": {"id": mid}, "/api/inbox/{id}/parts/{part}/resolve": {"id": mid},
		"/api/artifacts/{id}": {"id": published.ID}, "/api/artifacts/{id}/versions": {"id": published.ID},
		"/api/artifacts/{id}/content": {"id": published.ID}, "/api/runs/{id}/calls": {"id": lead.str("id")},
	}
	valid := map[string]string{
		"/api/agents/{name}": revised, "/api/agents/{name}/memory": `{"text":"x"}`, "/api/agents/{name}/memory/approve": `{"ids":[]}`,
		"/api/clients/{id}": `{"name":"Support","surface":"slack","identity":{"team":"T01ABCDEF"}}`, "/api/evals/{name}": `{"name":"smoke","target":"agent","subject":"helper","cases":[]}`,
		"/api/inbox/{id}/read": `{}`, "/api/inbox/{id}/snooze": `{"until":"2999-01-01T00:00:00Z"}`,
		"/api/inbox/{id}/parts/{part}/resolve": `{"action":"acknowledge"}`, "/api/knowledge/{name}/documents": `{"title":"t","text":"words here"}`,
		"/api/knowledge/{name}/documents/{id}": `{"excluded":false}`, "/api/knowledge/{name}/variants": `{"name":"other"}`,
		"/api/projects/{name}/memory": `{"text":"x"}`, "/api/projects/{name}/memory/approve": `{"ids":[]}`,
		"/api/runs/{id}/calls": `{"key":"k9","agent":"helper","text":"hi","mode":"delegate"}`,
	}
	for _, op := range c.spec.ops {
		params := pathParams(op.path)
		if !strings.HasPrefix(op.path, "/api/") || len(params) == 0 {
			continue
		}
		path := op.path
		for _, name := range params {
			v := ids[name]
			if r, has := real[op.path][name]; has {
				v = r
			}
			path = strings.ReplaceAll(path, "{"+name+"}", v)
		}
		if _, documents := op.schemas["404"]; documents {
			c.call(globex, op.method, op.path, path, valid[op.path], http.StatusNotFound)
		}
	}

	c.call(acme, "DELETE", "/api/artifacts/{id}", artifactPath, "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/artifacts/{id}", artifactPath, "", http.StatusNotFound)
	c.call(acme, "DELETE", "/api/clients/{id}", "/api/clients/support", "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/clients/{id}", "/api/clients/support", "", http.StatusNotFound)
	c.call(acme, "DELETE", "/api/evals/{name}", "/api/evals/smoke", "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/knowledge/{name}/variants/{variant}", "/api/knowledge/handbook/variants/lean", "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/knowledge/{name}/documents/{id}", docPath, "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/knowledge/{name}", "/api/knowledge/handbook", "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/alert-rules/{name}", "/api/alert-rules/any", "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/schedules/{name}", "/api/schedules/nightly", "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/schedules/{name}", "/api/schedules/nightly", "", http.StatusNotFound)
	c.call(acme, "DELETE", "/api/projects/{name}", "/api/projects/Launch", "", http.StatusNoContent)
	c.call(acme, "DELETE", "/api/projects/{name}", "/api/projects/Launch", "", http.StatusNotFound)

	// Every status code the contract documents was answered at least once,
	// or is listed in unexercised with the reason it is not.
	for key, op := range c.spec.ops {
		if !strings.HasPrefix(op.path, "/api/") || op.path == "/api/events" {
			continue
		}
		perm, _ := op.raw["x-permission"].(string)
		for code := range op.schemas {
			switch {
			case c.seen[key+" "+code], unexercised[key+" "+code] != "":
			case code == "401":
				// TestSpecPermissionsAreTheOnesTheServerEnforces sends every
				// operation without a credential.
			case code == "403" && perm != permAuthenticated:
				// The same test sends every operation with no permissions.
			case code == "413", code == "500":
				// Shared by every operation: a body over the cap, and a
				// failure on the server's side. Neither is per route.
			default:
				t.Errorf("%s documents %s and no test produced it. Exercise it, remove it, or list it in unexercised", key, code)
			}
		}
	}

	// Every JSON operation in the contract answered with its success status
	// at least once above. An operation added to the spec has to be added
	// here before this passes.
	for key, op := range c.spec.ops {
		if !strings.HasPrefix(op.path, "/api/") || op.path == "/api/events" {
			continue
		}
		ok := false
		for code := range op.schemas {
			if strings.HasPrefix(code, "2") && c.seen[key+" "+code] {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s was never checked against a real success response", key)
		}
	}
}

// unexercised lists documented status codes no test produces, each with why
// it is documented anyway. Keep it short: an entry is a code the server can
// send that is hard to provoke in a test, never a guess.
var unexercised = map[string]string{
	// The runtime here always has a bus to subscribe to and a ledger to
	// record in, so it can always carry a stop. TestAStopTheRuntimeCannotCarryIsA503
	// answers 503 from a server whose runtime cannot.
	"POST /api/runs/{id}/stop 503": "stop unavailable",
	// A client the server's configuration owns cannot be changed through the
	// API. Making one needs COLLEGIUM_SLACK_* in the environment, which
	// pkg/clients and internal/platform test.
	"PUT /api/clients/{id} 409":    "managed client",
	"DELETE /api/clients/{id} 409": "managed client",
	// Busy: a second start while the first eval is still running. Its timing
	// depends on the offline provider's speed, so a test would be flaky.
	"POST /api/evals/{name}/run 409": "suite already running",
	// retry needs a failed turn a person asked, and the offline provider
	// never fails, so no test can raise one here.
	"POST /api/inbox/{id}/parts/{part}/resolve 429": "retry refused by a limit",
	"POST /api/inbox/{id}/parts/{part}/resolve 402": "retry refused by a limit",
	// A spend refusal is the same answer through every route that starts a
	// run. createRun's 402 is exercised; a schedule's run now goes through
	// the same admission.
	"POST /api/schedules/{name}/run 402": "spend refusal, exercised through createRun",
}

// The event stream is server-sent events. Each message's data is validated
// against the union the spec gives the stream, and each event's payload is
// decoded from base64 and validated against the schema for its kind.
func TestEventStreamMatchesTheSpec(t *testing.T) {
	f := newFixture(t)
	s := loadSpec(t)
	event, notice := s.component(t, "RunEvent"), s.component(t, "InboxNotice")
	tok := f.machine(t, "acme", everything...)
	f.do(t, nil, "POST", "/api/agents", tok, helperSpec)
	f.do(t, nil, "POST", "/api/projects", tok, `{"name":"Launch"}`)
	f.do(t, nil, "PUT", "/api/policies", tok, `{"scope":{"kind":"project","name":"Launch"},"period":"day","max_runs":1}`)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/events?topic=agent", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}

	// One run that completes, then one a limit refuses, which raises an
	// inbox message and so a notice on the same stream.
	body := `{"agent":"helper","text":"Say hello","project":"Launch"}`
	_, run := f.do(t, nil, "POST", "/api/runs", tok, body)
	if res, _ := f.do(t, nil, "POST", "/api/runs", tok, body); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the second run = %d, want 429", res.StatusCode)
	}

	kinds, notices, name := map[string]int{}, 0, ""
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() && (kinds["lifecycle.completed"] == 0 || notices == 0) {
		line := sc.Text()
		if v, isName := strings.CutPrefix(line, "event: "); isName {
			name = v
			continue
		}
		data, isData := strings.CutPrefix(line, "data: ")
		if !isData {
			if line == "" {
				name = ""
			} else if !strings.HasPrefix(line, ":") {
				t.Errorf("the stream sent a line the spec does not describe: %q", line)
			}
			continue
		}
		value, err := jsonschema.UnmarshalJSON(strings.NewReader(data))
		if err != nil {
			t.Fatalf("data is not JSON: %q", data)
		}
		switch name {
		case "":
			if err := event.Validate(value); err != nil {
				t.Errorf("an event does not match RunEvent:\n%v\ndata: %s", err, data)
			}
			if obj(value)["run_id"] == run["id"] {
				kinds[obj(value)["kind"].(string)]++
			}
		case "inbox":
			if err := notice.Validate(value); err != nil {
				t.Errorf("a notice does not match InboxNotice:\n%v\ndata: %s", err, data)
			}
			notices++
		default:
			t.Errorf("the stream sent an event named %q, which the spec does not describe", name)
		}
	}
	for _, want := range []string{"lifecycle.accepted", "lifecycle.started", "delta.text", "lifecycle.completed"} {
		if kinds[want] == 0 {
			t.Errorf("the run's stream had no %s: %v", want, kinds)
		}
	}
	if notices == 0 {
		t.Error("the stream carried no inbox notice")
	}
}

// The test's copy of the spec refuses properties a schema does not name, at
// every level and through allOf, so a key added to a handler's map or inline
// struct fails TestResponsesMatchTheSpec.
func TestClosedSchemasRejectUnknownProperties(t *testing.T) {
	s := loadSpec(t)
	me := s.ops["GET /api/me"].schemas["200"]
	good := map[string]any{"principal": map[string]any{"subject": "a", "tenant": "t", "kind": "user"}, "permissions": nil, "first_admin": false}
	if err := me.Validate(good); err != nil {
		t.Fatalf("a valid answer is refused: %v", err)
	}
	for name, bad := range map[string]map[string]any{
		"at the top":   {"principal": good["principal"], "permissions": nil, "first_admin": false, "secret": "x"},
		"in an object": {"principal": map[string]any{"subject": "a", "tenant": "t", "kind": "user", "token": "x"}, "permissions": nil, "first_admin": false},
	} {
		if me.Validate(bad) == nil {
			t.Errorf("an unknown property %s is accepted", name)
		}
	}
	event := s.component(t, "RunEvent")
	base := map[string]any{"seq": json.Number("1"), "topic": "agent:a:r", "run_id": "r", "tenant": "t", "agent": "a", "rev": json.Number("1"), "origin": "api", "at": "2026-01-01T00:00:00Z", "kind": "delta.text"}
	if err := event.Validate(base); err != nil {
		t.Fatalf("a valid event is refused: %v", err)
	}
	base["extra"] = true
	if event.Validate(base) == nil {
		t.Error("an unknown property on an allOf variant is accepted")
	}
}
