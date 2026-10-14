package authz

import (
	"fmt"
	"strings"
)

// Effect is what a rule does to the permissions it names.
type Effect string

const (
	Allow Effect = "allow"
	Deny  Effect = "deny"
)

// File is a parsed policy, before it is checked.
type File struct {
	// Name is the source's name, for error messages.
	Name   string
	Roles  []*RoleDecl
	Grants []*Grant
}

// Name is a word in the source and where it was written.
type Name struct {
	Text string
	Pos  Pos
}

// RoleDecl is one role statement.
type RoleDecl struct {
	Pos     Pos
	Name    Name
	Extends []Name
	Rules   []*Rule
}

// Rule allows or denies actions on resources. An empty Actions or Resources
// list means "*".
type Rule struct {
	Pos       Pos
	Effect    Effect
	Actions   []Name
	Resources []Name
}

// String writes the rule as the language reads it.
func (r *Rule) String() string {
	return fmt.Sprintf("%s %s on %s", r.Effect, names(r.Actions), names(r.Resources))
}

func names(ns []Name) string {
	if len(ns) == 0 {
		return "*"
	}
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Text
	}
	return strings.Join(out, ", ")
}

// SubjectKind is which clause of a grant names its subject.
type SubjectKind string

const (
	// SubjectGroup matches a member of a group.
	SubjectGroup SubjectKind = "group"
	// SubjectID matches one subject identifier.
	SubjectID SubjectKind = "subject"
	// SubjectEmailDomain matches a verified email address in a domain.
	SubjectEmailDomain SubjectKind = "email domain"
	// SubjectClaim matches a string claim with one value.
	SubjectClaim SubjectKind = "claim"
	// SubjectAnyone matches every authenticated identity.
	SubjectAnyone SubjectKind = "anyone"
)

// Subject is who a grant is for.
type Subject struct {
	Kind SubjectKind
	// Claim is the claim's name. It is set only for SubjectClaim.
	Claim string
	// Value is the group, subject, domain or claim value. It is empty for
	// SubjectAnyone.
	Value string
}

// String writes the subject as the language reads it.
func (s Subject) String() string {
	switch s.Kind {
	case SubjectAnyone:
		return "anyone"
	case SubjectClaim:
		name := s.Claim
		if !isPlainWord(name) {
			name = quote(name)
		}
		return fmt.Sprintf("claim %s = %s", name, quote(s.Value))
	}
	return fmt.Sprintf("%s %s", s.Kind, quote(s.Value))
}

// Grant gives roles to a subject.
type Grant struct {
	Pos     Pos
	Roles   []Name
	Subject Subject
}

// String writes the grant as the language reads it.
func (g *Grant) String() string {
	return fmt.Sprintf("grant %s to %s", names(g.Roles), g.Subject)
}

// reserved are the words of the language. A role, an action or a resource
// cannot be named one. A claim can, because a claim's name is not ours to
// choose.
var reserved = map[string]bool{
	"role": true, "extends": true, "allow": true, "deny": true, "on": true,
	"grant": true, "to": true, "group": true, "subject": true, "email": true,
	"domain": true, "claim": true, "anyone": true,
}

func isPlainWord(s string) bool {
	if s == "" || !isWordStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isWordPart(s[i]) {
			return false
		}
	}
	return true
}

// maxSource bounds a policy. A policy is written by a person, and anything
// larger than this is a mistake or an attack.
const maxSource = 1 << 20

// Parse reads a policy. name is used in error messages and may be empty. The
// result is syntactically valid and nothing more: Compile checks what it means.
// Every error is an *Error.
func Parse(name, src string) (*File, error) {
	if len(src) > maxSource {
		return nil, &Error{Name: name, Pos: Pos{1, 1}, Msg: fmt.Sprintf("policy is %d bytes, larger than the %d allowed", len(src), maxSource)}
	}
	p := &parser{lex: lexer{name: name, src: src, line: 1, col: 1}}
	if err := p.advance(); err != nil {
		return nil, err
	}
	f := &File{Name: name}
	for p.tok.kind != tokEOF {
		switch {
		case p.isWord("role"):
			r, err := p.role()
			if err != nil {
				return nil, err
			}
			f.Roles = append(f.Roles, r)
		case p.isWord("grant"):
			g, err := p.grant()
			if err != nil {
				return nil, err
			}
			f.Grants = append(f.Grants, g)
		default:
			return nil, p.expected(`"role" or "grant"`)
		}
	}
	return f, nil
}

type parser struct {
	lex lexer
	tok token
}

// advance moves to the next token. The lexer's *Error is converted here, so a
// nil pointer never becomes a non-nil error.
func (p *parser) advance() error {
	t, err := p.lex.next()
	if err != nil {
		return err
	}
	p.tok = t
	return nil
}

func (p *parser) expected(what string) error {
	return p.lex.errorf(p.tok.pos, "expected %s, found %s", what, p.tok.describe())
}

func (p *parser) isWord(w string) bool { return p.tok.kind == tokWord && p.tok.text == w }

// word consumes the keyword w.
func (p *parser) word(w string) error {
	if !p.isWord(w) {
		return p.expected(fmt.Sprintf("%q", w))
	}
	return p.advance()
}

func (p *parser) punct(k kind, what string) error {
	if p.tok.kind != k {
		return p.expected(what)
	}
	return p.advance()
}

// ident consumes a name that is not a word of the language.
func (p *parser) ident(what string) (Name, error) {
	if p.tok.kind != tokWord {
		return Name{}, p.expected(what)
	}
	if reserved[p.tok.text] {
		return Name{}, p.lex.errorf(p.tok.pos, "expected %s, found the reserved word %q", what, p.tok.text)
	}
	n := Name{Text: p.tok.text, Pos: p.tok.pos}
	return n, p.advance()
}

// idents consumes IDENT { "," IDENT }.
func (p *parser) idents(what string) ([]Name, error) {
	var out []Name
	for {
		n, err := p.ident(what)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
		if p.tok.kind != tokComma {
			return out, nil
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
	}
}

func (p *parser) str(what string) (string, error) {
	if p.tok.kind != tokString {
		return "", p.expected(what)
	}
	s := p.tok.text
	return s, p.advance()
}

func (p *parser) role() (*RoleDecl, error) {
	r := &RoleDecl{Pos: p.tok.pos}
	if err := p.word("role"); err != nil {
		return nil, err
	}
	var err error
	if r.Name, err = p.ident("a role name"); err != nil {
		return nil, err
	}
	if p.isWord("extends") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		if r.Extends, err = p.idents("the name of a role to extend"); err != nil {
			return nil, err
		}
	}
	if p.tok.kind != tokLBrace {
		if len(r.Extends) > 0 {
			return nil, p.expected(`"," or "{"`)
		}
		return nil, p.expected(`"extends" or "{"`)
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	for p.tok.kind != tokRBrace {
		if !p.isWord("allow") && !p.isWord("deny") {
			return nil, p.expected(`"allow", "deny" or "}"`)
		}
		rule, err := p.rule()
		if err != nil {
			return nil, err
		}
		r.Rules = append(r.Rules, rule)
	}
	return r, p.advance()
}

func (p *parser) rule() (*Rule, error) {
	r := &Rule{Pos: p.tok.pos, Effect: Effect(p.tok.text)}
	if err := p.advance(); err != nil {
		return nil, err
	}
	var err error
	if r.Actions, err = p.starOr(`an action or "*"`, "an action"); err != nil {
		return nil, err
	}
	if !p.isWord("on") {
		if len(r.Actions) > 0 {
			return nil, p.expected(`"," or "on"`)
		}
		return nil, p.expected(`"on"`)
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if r.Resources, err = p.starOr(`a resource or "*"`, "a resource"); err != nil {
		return nil, err
	}
	return r, nil
}

// starOr consumes "*" and returns nil, or a list of names.
func (p *parser) starOr(first, rest string) ([]Name, error) {
	if p.tok.kind == tokStar {
		return nil, p.advance()
	}
	if p.tok.kind != tokWord {
		return nil, p.expected(first)
	}
	return p.idents(rest)
}

func (p *parser) grant() (*Grant, error) {
	g := &Grant{Pos: p.tok.pos}
	if err := p.word("grant"); err != nil {
		return nil, err
	}
	var err error
	if g.Roles, err = p.idents("a role name"); err != nil {
		return nil, err
	}
	if !p.isWord("to") {
		return nil, p.expected(`"," or "to"`)
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if g.Subject, err = p.subject(); err != nil {
		return nil, err
	}
	return g, nil
}

func (p *parser) subject() (Subject, error) {
	const want = `"group", "subject", "email", "claim" or "anyone"`
	if p.tok.kind != tokWord {
		return Subject{}, p.expected(want)
	}
	var s Subject
	switch p.tok.text {
	case "anyone":
		return Subject{Kind: SubjectAnyone}, p.advance()
	case "group", "subject":
		s.Kind = SubjectKind(p.tok.text)
		if err := p.advance(); err != nil {
			return Subject{}, err
		}
	case "email":
		s.Kind = SubjectEmailDomain
		if err := p.advance(); err != nil {
			return Subject{}, err
		}
		if err := p.word("domain"); err != nil {
			return Subject{}, err
		}
	case "claim":
		s.Kind = SubjectClaim
		if err := p.advance(); err != nil {
			return Subject{}, err
		}
		// A claim is named by the provider, so any word is accepted here,
		// and a string for names that are not words, such as a URL.
		if p.tok.kind != tokWord && p.tok.kind != tokString {
			return Subject{}, p.expected("a claim name")
		}
		s.Claim = p.tok.text
		if s.Claim == "" {
			return Subject{}, p.lex.errorf(p.tok.pos, "expected a claim name, found an empty string")
		}
		if err := p.advance(); err != nil {
			return Subject{}, err
		}
		if err := p.punct(tokEquals, `"="`); err != nil {
			return Subject{}, err
		}
	default:
		return Subject{}, p.expected(want)
	}
	valuePos := p.tok.pos
	v, err := p.str("a quoted string")
	if err != nil {
		return Subject{}, err
	}
	// An empty value would match an identity that has an empty claim, which
	// is never what a policy means.
	if v == "" {
		return Subject{}, p.lex.errorf(valuePos, "expected a value, found an empty string")
	}
	if s.Kind == SubjectEmailDomain && strings.ContainsAny(v, "@ ") {
		return Subject{}, p.lex.errorf(valuePos, `expected a domain such as "example.com", found %s`, quote(v))
	}
	s.Value = v
	return s, nil
}
