package parser

import (
	"fmt"
	"unicode"
)

type token struct {
	kind   string
	value  string
	line   int
	column int
}

func lex(src []byte) ([]token, error) {
	l := lexer{src: []rune(string(src)), line: 1, column: 1}
	var out []token
	for {
		tok, ok, err := l.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		out = append(out, tok)
	}
	out = append(out, token{kind: "eof", line: l.line, column: l.column})
	return out, nil
}

type lexer struct {
	src    []rune
	pos    int
	line   int
	column int
}

func (l *lexer) next() (token, bool, error) {
	l.skipSpaceAndComments()
	if l.pos >= len(l.src) {
		return token{}, false, nil
	}
	startLine, startCol := l.line, l.column
	ch := l.peek()
	if isIdentStart(ch) {
		value := l.takeWhile(isIdentPart)
		return token{kind: "ident", value: value, line: startLine, column: startCol}, true, nil
	}
	if unicode.IsDigit(ch) {
		value := l.takeWhile(func(r rune) bool { return unicode.IsDigit(r) || r == '.' })
		return token{kind: "number", value: value, line: startLine, column: startCol}, true, nil
	}
	if ch == '"' || ch == '\'' {
		value, err := l.readString(ch)
		if err != nil {
			return token{}, false, err
		}
		return token{kind: "string", value: value, line: startLine, column: startCol}, true, nil
	}
	for _, op := range []string{">=", "<=", "===", "!==", "==", "!=", "&&", "||", "=>"} {
		if l.hasPrefix(op) {
			l.advanceN(len([]rune(op)))
			return token{kind: "symbol", value: op, line: startLine, column: startCol}, true, nil
		}
	}
	if isSymbol(ch) {
		l.advance()
		return token{kind: "symbol", value: string(ch), line: startLine, column: startCol}, true, nil
	}
	return token{}, false, fmt.Errorf("unexpected character %q at %d:%d", ch, startLine, startCol)
}

func (l *lexer) skipSpaceAndComments() {
	for l.pos < len(l.src) {
		ch := l.peek()
		if unicode.IsSpace(ch) {
			l.advance()
			continue
		}
		if ch == '/' && l.peekN(1) == '/' {
			for l.pos < len(l.src) && l.peek() != '\n' {
				l.advance()
			}
			continue
		}
		if ch == '/' && l.peekN(1) == '*' {
			l.advanceN(2)
			for l.pos < len(l.src) {
				if l.peek() == '*' && l.peekN(1) == '/' {
					l.advanceN(2)
					break
				}
				l.advance()
			}
			continue
		}
		return
	}
}

func (l *lexer) readString(quote rune) (string, error) {
	var value []rune
	value = append(value, quote)
	l.advance()
	for l.pos < len(l.src) {
		ch := l.peek()
		value = append(value, ch)
		l.advance()
		if ch == '\\' && l.pos < len(l.src) {
			value = append(value, l.peek())
			l.advance()
			continue
		}
		if ch == quote {
			if quote == '\'' {
				value[0] = '"'
				value[len(value)-1] = '"'
			}
			return string(value), nil
		}
	}
	return "", fmt.Errorf("unterminated string")
}

func (l *lexer) takeWhile(fn func(rune) bool) string {
	start := l.pos
	for l.pos < len(l.src) && fn(l.peek()) {
		l.advance()
	}
	return string(l.src[start:l.pos])
}

func (l *lexer) hasPrefix(s string) bool {
	rs := []rune(s)
	if l.pos+len(rs) > len(l.src) {
		return false
	}
	for i, r := range rs {
		if l.src[l.pos+i] != r {
			return false
		}
	}
	return true
}

func (l *lexer) advanceN(n int) {
	for i := 0; i < n; i++ {
		l.advance()
	}
}

func (l *lexer) advance() {
	if l.pos >= len(l.src) {
		return
	}
	if l.src[l.pos] == '\n' {
		l.line++
		l.column = 1
	} else {
		l.column++
	}
	l.pos++
}

func (l *lexer) peek() rune {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) peekN(n int) rune {
	if l.pos+n >= len(l.src) {
		return 0
	}
	return l.src[l.pos+n]
}

func isIdentStart(ch rune) bool {
	return ch == '_' || ch == '$' || unicode.IsLetter(ch)
}

func isIdentPart(ch rune) bool {
	return isIdentStart(ch) || unicode.IsDigit(ch)
}

func isSymbol(ch rune) bool {
	switch ch {
	case '{', '}', '(', ')', '[', ']', ':', ';', ',', '.', '?', '=', '+', '-', '*', '/', '<', '>', '|', '!', '&':
		return true
	default:
		return false
	}
}
