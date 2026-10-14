package authz

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Pos is a place in a policy source. Line and Col start at 1, and Col counts
// bytes.
type Pos struct {
	Line int
	Col  int
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// Error is a problem with a policy, found while reading or compiling it.
type Error struct {
	// Name is the source's name, usually a file path. It may be empty.
	Name string
	Pos  Pos
	Msg  string
}

func (e *Error) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
	}
	return fmt.Sprintf("%s:%s: %s", e.Name, e.Pos, e.Msg)
}

type kind int

const (
	tokEOF kind = iota
	tokWord
	tokString
	tokLBrace
	tokRBrace
	tokComma
	tokStar
	tokEquals
)

type token struct {
	kind kind
	// text is the word, or the unquoted value of a string.
	text string
	pos  Pos
}

// describe names a token the way an error message should show it.
func (t token) describe() string {
	switch t.kind {
	case tokEOF:
		return "end of policy"
	case tokWord:
		return fmt.Sprintf("%q", t.text)
	case tokString:
		return "string " + quote(t.text)
	case tokLBrace:
		return `"{"`
	case tokRBrace:
		return `"}"`
	case tokComma:
		return `","`
	case tokStar:
		return `"*"`
	case tokEquals:
		return `"="`
	}
	return "token"
}

type lexer struct {
	name string
	src  string
	off  int
	line int
	col  int
}

func (l *lexer) errorf(pos Pos, format string, args ...any) *Error {
	return &Error{Name: l.name, Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

func (l *lexer) advance(n int) {
	for i := 0; i < n; i++ {
		if l.src[l.off] == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
		l.off++
	}
}

func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isWordPart(c byte) bool {
	return isWordStart(c) || c == '-' || (c >= '0' && c <= '9')
}

// next returns the next token, skipping blanks and comments.
func (l *lexer) next() (token, *Error) {
	for l.off < len(l.src) {
		c := l.src[l.off]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			l.advance(1)
		case c == '#':
			for l.off < len(l.src) && l.src[l.off] != '\n' {
				l.advance(1)
			}
		default:
			return l.scan()
		}
	}
	return token{kind: tokEOF, pos: Pos{l.line, l.col}}, nil
}

func (l *lexer) scan() (token, *Error) {
	pos := Pos{l.line, l.col}
	c := l.src[l.off]
	single := map[byte]kind{'{': tokLBrace, '}': tokRBrace, ',': tokComma, '*': tokStar, '=': tokEquals}
	if k, ok := single[c]; ok {
		l.advance(1)
		return token{kind: k, pos: pos}, nil
	}
	if isWordStart(c) {
		start := l.off
		for l.off < len(l.src) && isWordPart(l.src[l.off]) {
			l.advance(1)
		}
		return token{kind: tokWord, text: l.src[start:l.off], pos: pos}, nil
	}
	if c == '"' {
		return l.scanString(pos)
	}
	r, size := utf8.DecodeRuneInString(l.src[l.off:])
	if r == utf8.RuneError && size <= 1 {
		return token{}, l.errorf(pos, "invalid UTF-8")
	}
	return token{}, l.errorf(pos, "unexpected character %q, expected a word, a string, or one of { } , * =", r)
}

// scanString reads a double-quoted string on one line. The only escapes are
// \" and \\.
func (l *lexer) scanString(pos Pos) (token, *Error) {
	l.advance(1)
	var b strings.Builder
	for l.off < len(l.src) {
		c := l.src[l.off]
		switch {
		case c == '"':
			l.advance(1)
			if !utf8.ValidString(b.String()) {
				return token{}, l.errorf(pos, "string is not valid UTF-8")
			}
			return token{kind: tokString, text: b.String(), pos: pos}, nil
		case c == '\n':
			return token{}, l.errorf(pos, `string is not closed, expected '"' before the end of the line`)
		case c == '\\':
			if l.off+1 >= len(l.src) || (l.src[l.off+1] != '"' && l.src[l.off+1] != '\\') {
				return token{}, l.errorf(Pos{l.line, l.col}, `unknown escape, expected \" or \\`)
			}
			b.WriteByte(l.src[l.off+1])
			l.advance(2)
		default:
			b.WriteByte(c)
			l.advance(1)
		}
	}
	return token{}, l.errorf(pos, `string is not closed, expected '"' before the end of the policy`)
}

// quote writes s the way the language reads it back.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
