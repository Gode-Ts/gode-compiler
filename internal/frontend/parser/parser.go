package parser

import (
	"fmt"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"gode.dev/gode-compiler/internal/diagnostics"
	fast "gode.dev/gode-compiler/internal/frontend/ast"
)

func ParseFile(filename string, src []byte) (*fast.Module, diagnostics.List) {
	if diag, ok := firstUnsupported(filename, string(src)); ok {
		return nil, diagnostics.List{diag}
	}
	if diag, ok := validateTreeSitter(filename, src); !ok {
		return nil, diagnostics.List{diag}
	}
	toks, err := lex(src)
	if err != nil {
		return nil, diagnostics.List{diagnostics.Errorf(filename, diagnostics.Position{Line: 1, Column: 1}, "GODE_PARSE_001", "%s", err)}
	}
	p := subsetParser{filename: filename, tokens: toks}
	mod := p.parseModule()
	if p.diags.HasErrors() {
		return nil, p.diags
	}
	return mod, p.diags
}

func validateTreeSitter(filename string, src []byte) (diagnostics.Diagnostic, bool) {
	p := tree_sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())); err != nil {
		return diagnostics.Errorf(filename, diagnostics.Position{Line: 1, Column: 1}, "GODE_PARSE_001", "failed to load TypeScript grammar: %v", err), false
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		return diagnostics.Errorf(filename, diagnostics.Position{Line: 1, Column: 1}, "GODE_PARSE_001", "invalid TypeScript syntax"), false
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return diagnostics.Errorf(filename, diagnostics.Position{Line: 1, Column: 1}, "GODE_PARSE_001", "invalid TypeScript syntax"), false
	}
	return diagnostics.Diagnostic{}, true
}

func firstUnsupported(filename, src string) (diagnostics.Diagnostic, bool) {
	for _, keyword := range []string{"class", "decorator", "any", "unknown", "this", "throw", "try", "catch"} {
		if line, col, ok := findWord(src, keyword); ok {
			code := "GODE_SUBSET_001"
			if keyword == "any" || keyword == "unknown" {
				code = "GODE_TYPE_001"
			}
			return diagnostics.Errorf(filename, diagnostics.Position{Line: line, Column: col}, code, "unsupported language construct %q", keyword), true
		}
	}
	return diagnostics.Diagnostic{}, false
}

func findWord(src, word string) (int, int, bool) {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		idx := strings.Index(line, word)
		for idx >= 0 {
			beforeOK := idx == 0 || !isIdentPart(rune(line[idx-1]))
			after := idx + len(word)
			afterOK := after >= len(line) || !isIdentPart(rune(line[after]))
			if beforeOK && afterOK {
				return i + 1, idx + 1, true
			}
			next := strings.Index(line[after:], word)
			if next < 0 {
				break
			}
			idx = after + next
		}
	}
	return 0, 0, false
}

type subsetParser struct {
	filename string
	tokens   []token
	pos      int
	diags    diagnostics.List
}

func (p *subsetParser) parseModule() *fast.Module {
	mod := &fast.Module{File: p.filename}
	for !p.at("eof", "") {
		decl := p.parseDeclaration()
		if decl.Name != "" || decl.Kind != "" {
			mod.Declarations = append(mod.Declarations, decl)
		}
	}
	return mod
}

func (p *subsetParser) parseDeclaration() fast.Declaration {
	exported := p.matchIdent("export")
	declare := p.matchIdent("declare")
	if p.matchIdent("type") {
		return p.parseTypeAlias(exported)
	}
	if p.matchIdent("interface") {
		return p.parseInterface(exported)
	}
	async := p.matchIdent("async")
	if p.matchIdent("function") {
		return p.parseFunction(exported, async, declare)
	}
	if p.matchIdent("const") || p.matchIdent("let") {
		return p.parseConst(exported)
	}
	tok := p.peek()
	p.add(tok, "GODE_SUBSET_001", "unsupported language construct %q", tok.value)
	p.advance()
	return fast.Declaration{}
}

func (p *subsetParser) parseTypeAlias(exported bool) fast.Declaration {
	name := p.expectIdent()
	p.expect("symbol", "=")
	typ := p.parseType()
	p.optionalTerminator()
	return fast.Declaration{Kind: "type_alias", Name: name.value, Exported: exported, Type: typ}
}

func (p *subsetParser) parseInterface(exported bool) fast.Declaration {
	name := p.expectIdent()
	typ := p.parseObjectType()
	return fast.Declaration{Kind: "interface", Name: name.value, Exported: exported, Type: typ}
}

func (p *subsetParser) parseFunction(exported, async, declare bool) fast.Declaration {
	name := p.expectIdent()
	p.expect("symbol", "(")
	params := p.parseParams()
	p.expect("symbol", ")")
	p.expect("symbol", ":")
	returnType := p.parseType()
	decl := fast.Declaration{Kind: "function", Name: name.value, Exported: exported, Async: async, Declare: declare, Params: params, ReturnType: returnType}
	if declare {
		p.optionalTerminator()
		return decl
	}
	decl.Body = p.parseBlock()
	return decl
}

func (p *subsetParser) parseConst(exported bool) fast.Declaration {
	name := p.expectIdent()
	var typ *fast.Type
	if p.match("symbol", ":") {
		typ = p.parseType()
	}
	p.expect("symbol", "=")
	expr := p.parseExpression(0)
	p.optionalTerminator()
	return fast.Declaration{Kind: "const", Name: name.value, Exported: exported, Type: typ, Body: []fast.Statement{{Kind: "return", Expr: expr}}}
}

func (p *subsetParser) parseParams() []fast.Param {
	var params []fast.Param
	if p.at("symbol", ")") {
		return params
	}
	for {
		name := p.expectIdent()
		p.expect("symbol", ":")
		params = append(params, fast.Param{Name: name.value, Type: p.parseType()})
		if !p.match("symbol", ",") {
			break
		}
	}
	return params
}

func (p *subsetParser) parseBlock() []fast.Statement {
	p.expect("symbol", "{")
	var stmts []fast.Statement
	for !p.at("eof", "") && !p.match("symbol", "}") {
		stmts = append(stmts, p.parseStatement())
	}
	return stmts
}

func (p *subsetParser) parseStatement() fast.Statement {
	if p.matchIdent("return") {
		if p.at("symbol", ";") || p.at("symbol", "}") {
			p.optionalTerminator()
			return fast.Statement{Kind: "return"}
		}
		expr := p.parseExpression(0)
		p.optionalTerminator()
		return fast.Statement{Kind: "return", Expr: expr}
	}
	if p.matchIdent("const") || p.matchIdent("let") {
		name := p.expectIdent()
		var typ *fast.Type
		if p.match("symbol", ":") {
			typ = p.parseType()
		}
		p.expect("symbol", "=")
		expr := p.parseExpression(0)
		p.optionalTerminator()
		return fast.Statement{Kind: "var", Name: name.value, Type: typ, Expr: expr}
	}
	if p.matchIdent("if") {
		p.expect("symbol", "(")
		cond := p.parseExpression(0)
		p.expect("symbol", ")")
		then := p.parseBlock()
		var otherwise []fast.Statement
		if p.matchIdent("else") {
			otherwise = p.parseBlock()
		}
		return fast.Statement{Kind: "if", Expr: cond, Then: then, Else: otherwise}
	}
	tok := p.peek()
	p.add(tok, "GODE_SUBSET_001", "unsupported language construct %q", tok.value)
	p.advance()
	return fast.Statement{Kind: "invalid"}
}

func (p *subsetParser) parseType() *fast.Type {
	left := p.parsePrimaryType()
	for p.match("symbol", "|") {
		right := p.parsePrimaryType()
		if isNilish(right) {
			left = &fast.Type{Kind: "nullable", Inner: left}
			continue
		}
		if isNilish(left) {
			left = &fast.Type{Kind: "nullable", Inner: right}
			continue
		}
		left = &fast.Type{Kind: "union", Key: left, Value: right}
	}
	return left
}

func (p *subsetParser) parsePrimaryType() *fast.Type {
	if p.match("symbol", "{") {
		return p.parseObjectTypeAfterOpen()
	}
	name := p.expectIdent()
	var typ *fast.Type
	switch name.value {
	case "string", "number", "boolean", "void", "null", "undefined":
		typ = fast.Primitive(name.value)
	case "Array":
		p.expect("symbol", "<")
		inner := p.parseType()
		p.expect("symbol", ">")
		typ = &fast.Type{Kind: "array", Inner: inner}
	case "Promise":
		p.expect("symbol", "<")
		inner := p.parseType()
		p.expect("symbol", ">")
		typ = &fast.Type{Kind: "promise", Inner: inner}
	case "Record":
		p.expect("symbol", "<")
		key := p.parseType()
		p.expect("symbol", ",")
		value := p.parseType()
		p.expect("symbol", ">")
		typ = &fast.Type{Kind: "record", Key: key, Value: value}
	default:
		typ = fast.Reference(name.value)
	}
	for p.match("symbol", "[") {
		p.expect("symbol", "]")
		typ = &fast.Type{Kind: "array", Inner: typ}
	}
	return typ
}

func (p *subsetParser) parseObjectType() *fast.Type {
	p.expect("symbol", "{")
	return p.parseObjectTypeAfterOpen()
}

func (p *subsetParser) parseObjectTypeAfterOpen() *fast.Type {
	typ := &fast.Type{Kind: "object"}
	for !p.at("eof", "") && !p.match("symbol", "}") {
		name := p.expectIdent()
		optional := p.match("symbol", "?")
		p.expect("symbol", ":")
		typ.Fields = append(typ.Fields, fast.Field{Name: name.value, Optional: optional, Type: p.parseType()})
		p.match("symbol", ",")
		p.match("symbol", ";")
	}
	return typ
}

func (p *subsetParser) parseExpression(minPrec int) *fast.Expr {
	left := p.parsePrefix()
	for {
		op := p.peek()
		prec := precedence(op.value)
		if op.kind != "symbol" || prec < minPrec {
			break
		}
		p.advance()
		right := p.parseExpression(prec + 1)
		left = &fast.Expr{Kind: "binary", Op: op.value, Left: left, Right: right}
	}
	return left
}

func (p *subsetParser) parsePrefix() *fast.Expr {
	if p.matchIdent("await") {
		return &fast.Expr{Kind: "await", Expr: p.parseExpression(8)}
	}
	if p.match("symbol", "!") || p.match("symbol", "-") {
		op := p.previous()
		return &fast.Expr{Kind: "unary", Op: op.value, Expr: p.parseExpression(8)}
	}
	return p.parsePostfix()
}

func (p *subsetParser) parsePostfix() *fast.Expr {
	expr := p.parsePrimaryExpr()
	for {
		if p.match("symbol", ".") {
			prop := p.expectIdent()
			expr = &fast.Expr{Kind: "member", Object: expr, Property: prop.value}
			continue
		}
		if p.match("symbol", "(") {
			var args []fast.Expr
			if !p.at("symbol", ")") {
				for {
					args = append(args, *p.parseExpression(0))
					if !p.match("symbol", ",") {
						break
					}
				}
			}
			p.expect("symbol", ")")
			expr = &fast.Expr{Kind: "call", Callee: expr, Args: args}
			continue
		}
		return expr
	}
}

func (p *subsetParser) parsePrimaryExpr() *fast.Expr {
	if p.match("number", "") {
		tok := p.previous()
		return &fast.Expr{Kind: "number", Value: tok.value}
	}
	if p.match("string", "") {
		tok := p.previous()
		return &fast.Expr{Kind: "string", Value: tok.value}
	}
	if p.matchIdent("true") || p.matchIdent("false") {
		tok := p.previous()
		return &fast.Expr{Kind: "boolean", Value: tok.value}
	}
	if p.matchIdent("null") || p.matchIdent("undefined") {
		tok := p.previous()
		return &fast.Expr{Kind: tok.value}
	}
	if p.match("symbol", "{") {
		return p.parseObjectLiteral()
	}
	if p.match("symbol", "(") {
		expr := p.parseExpression(0)
		p.expect("symbol", ")")
		return expr
	}
	name := p.expectIdent()
	return &fast.Expr{Kind: "identifier", Name: name.value}
}

func (p *subsetParser) parseObjectLiteral() *fast.Expr {
	obj := &fast.Expr{Kind: "object"}
	for !p.at("eof", "") && !p.match("symbol", "}") {
		key := p.expectIdent()
		var value *fast.Expr
		if p.match("symbol", ":") {
			value = p.parseExpression(0)
		} else {
			value = &fast.Expr{Kind: "identifier", Name: key.value}
		}
		obj.Entries = append(obj.Entries, fast.ObjectEntry{Key: key.value, Value: value})
		if !p.match("symbol", ",") {
			p.match("symbol", ";")
		}
	}
	return obj
}

func precedence(op string) int {
	switch op {
	case "||":
		return 1
	case "&&":
		return 2
	case "==", "!=", "===", "!==":
		return 3
	case "<", "<=", ">", ">=":
		return 4
	case "+", "-":
		return 5
	case "*", "/":
		return 6
	default:
		return -1
	}
}

func isNilish(t *fast.Type) bool {
	return t != nil && t.Kind == "primitive" && (t.Name == "null" || t.Name == "undefined")
}

func (p *subsetParser) optionalTerminator() {
	p.match("symbol", ";")
}

func (p *subsetParser) expectIdent() token {
	if p.match("ident", "") {
		return p.previous()
	}
	tok := p.peek()
	p.add(tok, "GODE_PARSE_001", "expected identifier")
	return token{kind: "ident", value: "invalid", line: tok.line, column: tok.column}
}

func (p *subsetParser) expect(kind, value string) token {
	if p.match(kind, value) {
		return p.previous()
	}
	tok := p.peek()
	expected := kind
	if value != "" {
		expected = value
	}
	p.add(tok, "GODE_PARSE_001", "expected %s", expected)
	return tok
}

func (p *subsetParser) matchIdent(value string) bool {
	if p.at("ident", value) {
		p.advance()
		return true
	}
	return false
}

func (p *subsetParser) match(kind, value string) bool {
	if p.at(kind, value) {
		p.advance()
		return true
	}
	return false
}

func (p *subsetParser) at(kind, value string) bool {
	tok := p.peek()
	if tok.kind != kind {
		return false
	}
	return value == "" || tok.value == value
}

func (p *subsetParser) peek() token {
	if p.pos >= len(p.tokens) {
		return token{kind: "eof", line: 1, column: 1}
	}
	return p.tokens[p.pos]
}

func (p *subsetParser) previous() token {
	if p.pos == 0 {
		return token{}
	}
	return p.tokens[p.pos-1]
}

func (p *subsetParser) advance() {
	if p.pos < len(p.tokens) {
		p.pos++
	}
}

func (p *subsetParser) add(tok token, code, format string, args ...any) {
	p.diags = append(p.diags, diagnostics.Errorf(p.filename, diagnostics.Position{Line: tok.line, Column: tok.column}, code, format, args...))
}

func debugTokens(src []byte) string {
	toks, err := lex(src)
	if err != nil {
		return err.Error()
	}
	parts := make([]string, 0, len(toks))
	for _, tok := range toks {
		parts = append(parts, fmt.Sprintf("%s:%s", tok.kind, tok.value))
	}
	return strings.Join(parts, " ")
}
