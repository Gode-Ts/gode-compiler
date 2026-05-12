package semantics

import (
	"fmt"

	"gode.dev/gode-compiler/internal/config"
	"gode.dev/gode-compiler/internal/diagnostics"
	fast "gode.dev/gode-compiler/internal/frontend/ast"
	"gode.dev/gode-compiler/internal/ir"
	"gode.dev/gode-compiler/internal/names"
)

func Lower(mod *fast.Module, cfg config.Config) (ir.Module, diagnostics.List) {
	if mod == nil {
		return ir.Module{PackageName: cfg.Package}, nil
	}
	l := lowerer{
		file:    mod.File,
		cfg:     cfg,
		symbols: map[string]fast.Declaration{},
		funcs:   map[string]fast.Declaration{},
	}
	out := ir.Module{PackageName: cfg.Package}
	for _, decl := range mod.Declarations {
		if _, exists := l.symbols[decl.Name]; exists {
			l.diags = append(l.diags, diagnostics.Errorf(mod.File, diagnostics.Position{Line: 1, Column: 1}, "GODE_BIND_001", "duplicate symbol %q", decl.Name))
			continue
		}
		l.symbols[decl.Name] = decl
		if decl.Kind == "function" {
			l.funcs[decl.Name] = decl
		}
	}
	for _, decl := range mod.Declarations {
		switch decl.Kind {
		case "type_alias", "interface":
			out.Declarations = append(out.Declarations, l.lowerTypeDecl(decl))
		case "function":
			lowered := l.lowerFunction(decl)
			if lowered.Async {
				out.UsesAsync = true
			}
			out.Declarations = append(out.Declarations, lowered)
		case "const":
			out.Declarations = append(out.Declarations, l.lowerConst(decl))
		}
	}
	return out, l.diags
}

type lowerer struct {
	file    string
	cfg     config.Config
	symbols map[string]fast.Declaration
	funcs   map[string]fast.Declaration
	diags   diagnostics.List
}

func (l *lowerer) lowerTypeDecl(decl fast.Declaration) ir.Declaration {
	if decl.Type == nil || decl.Type.Kind != "object" {
		l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_TYPE_001", "type %q must be an object type in V1", decl.Name))
		return ir.Declaration{Kind: "struct", Name: decl.Name, GoName: names.Exported(decl.Name), Exported: decl.Exported}
	}
	fields := make([]ir.Field, 0, len(decl.Type.Fields))
	for _, field := range decl.Type.Fields {
		l.validateType(field.Type)
		fields = append(fields, ir.Field{
			Name:     field.Name,
			GoName:   names.Exported(field.Name),
			Optional: field.Optional || isNullable(field.Type),
			Type:     field.Type,
			JSONName: field.Name,
		})
	}
	return ir.Declaration{Kind: "struct", Name: decl.Name, Exported: decl.Exported, Fields: fields}
}

func (l *lowerer) lowerConst(decl fast.Declaration) ir.Declaration {
	if decl.Type == nil && len(decl.Body) > 0 {
		decl.Type = inferExprType(decl.Body[0].Expr)
	}
	l.validateType(decl.Type)
	return ir.Declaration{
		Kind:       "const",
		Name:       decl.Name,
		GoName:     exportedIf(decl.Exported, decl.Name),
		Exported:   decl.Exported,
		ReturnType: decl.Type,
		Body:       decl.Body,
	}
}

func (l *lowerer) lowerFunction(decl fast.Declaration) ir.Declaration {
	params := make([]ir.Param, 0, len(decl.Params))
	for _, param := range decl.Params {
		l.validateType(param.Type)
		params = append(params, ir.Param{Name: param.Name, GoName: names.Local(param.Name), Type: param.Type})
	}
	l.validateType(decl.ReturnType)
	if decl.Async && (decl.ReturnType == nil || decl.ReturnType.Kind != "promise") {
		l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_TYPE_002", "async function %q must return Promise<T>", decl.Name))
	}
	body := l.lowerStatements(decl.Body, decl.Async)
	kind := "function"
	if decl.Declare {
		kind = "external_function"
	}
	return ir.Declaration{
		Kind:       kind,
		Name:       decl.Name,
		GoName:     exportedIf(true, decl.Name),
		Exported:   decl.Exported,
		Async:      decl.Async,
		Params:     params,
		ReturnType: decl.ReturnType,
		Body:       body,
	}
}

func (l *lowerer) lowerStatements(stmts []fast.Statement, inAsync bool) []fast.Statement {
	out := make([]fast.Statement, 0, len(stmts))
	scope := map[string]*fast.Type{}
	for _, stmt := range stmts {
		if stmt.Kind == "var" {
			if stmt.Type == nil {
				stmt.Type = l.inferStatementType(stmt)
			}
			scope[stmt.Name] = stmt.Type
		}
		if containsAwait(stmt.Expr) && !inAsync {
			l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_SUBSET_001", "await is only valid inside async functions"))
		}
		l.validateExpr(stmt.Expr, scope, inAsync)
		out = append(out, stmt)
	}
	return out
}

func (l *lowerer) inferStatementType(stmt fast.Statement) *fast.Type {
	if stmt.Expr == nil {
		return nil
	}
	if stmt.Expr.Kind == "await" {
		call := stmt.Expr.Expr
		if call != nil && call.Kind == "call" && call.Callee != nil && call.Callee.Kind == "identifier" {
			if fn, ok := l.funcs[call.Callee.Name]; ok && fn.ReturnType != nil && fn.ReturnType.Kind == "promise" {
				return fn.ReturnType.Inner
			}
		}
	}
	return inferExprType(stmt.Expr)
}

func (l *lowerer) validateExpr(expr *fast.Expr, scope map[string]*fast.Type, inAsync bool) {
	if expr == nil {
		return
	}
	switch expr.Kind {
	case "identifier":
		if _, ok := scope[expr.Name]; ok {
			return
		}
		if _, ok := l.symbols[expr.Name]; ok {
			return
		}
	case "await":
		if !inAsync {
			l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_SUBSET_001", "await is only valid inside async functions"))
		}
		if expr.Expr != nil && expr.Expr.Kind == "call" && expr.Expr.Callee != nil && expr.Expr.Callee.Kind == "identifier" {
			fn, ok := l.funcs[expr.Expr.Callee.Name]
			if !ok {
				l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_BIND_002", "unresolved identifier %q", expr.Expr.Callee.Name))
			} else if fn.ReturnType == nil || fn.ReturnType.Kind != "promise" {
				l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_TYPE_002", "await target %q must return Promise<T>", expr.Expr.Callee.Name))
			}
		}
		l.validateExpr(expr.Expr, scope, inAsync)
	case "call":
		l.validateExpr(expr.Callee, scope, inAsync)
		for i := range expr.Args {
			l.validateExpr(&expr.Args[i], scope, inAsync)
		}
	case "binary":
		l.validateExpr(expr.Left, scope, inAsync)
		l.validateExpr(expr.Right, scope, inAsync)
	case "member":
		l.validateExpr(expr.Object, scope, inAsync)
	case "object":
		for _, entry := range expr.Entries {
			l.validateExpr(entry.Value, scope, inAsync)
		}
	}
}

func (l *lowerer) validateType(t *fast.Type) {
	if t == nil {
		return
	}
	switch t.Kind {
	case "primitive":
		switch t.Name {
		case "string", "number", "boolean", "void", "null", "undefined":
		default:
			l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_TYPE_001", "unsupported primitive type %q", t.Name))
		}
	case "reference":
		if _, ok := l.symbols[t.Name]; !ok {
			l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_BIND_002", "unresolved identifier %q", t.Name))
		}
	case "array", "nullable", "promise":
		l.validateType(t.Inner)
	case "record":
		if t.Key == nil || t.Key.Kind != "primitive" || t.Key.Name != "string" {
			l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_TYPE_001", "Record<K,V> only supports string keys in V1"))
		}
		l.validateType(t.Value)
	case "object":
		for _, field := range t.Fields {
			l.validateType(field.Type)
		}
	case "union":
		l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_TYPE_001", "only nullable unions are supported in V1"))
	default:
		l.diags = append(l.diags, diagnostics.Errorf(l.file, diagnostics.Position{Line: 1, Column: 1}, "GODE_TYPE_001", "unsupported type kind %q", t.Kind))
	}
}

func inferExprType(expr *fast.Expr) *fast.Type {
	if expr == nil {
		return nil
	}
	switch expr.Kind {
	case "string":
		return fast.Primitive("string")
	case "number":
		return fast.Primitive("number")
	case "boolean":
		return fast.Primitive("boolean")
	}
	return nil
}

func containsAwait(expr *fast.Expr) bool {
	if expr == nil {
		return false
	}
	if expr.Kind == "await" {
		return true
	}
	if containsAwait(expr.Expr) || containsAwait(expr.Left) || containsAwait(expr.Right) || containsAwait(expr.Object) || containsAwait(expr.Callee) {
		return true
	}
	for i := range expr.Args {
		if containsAwait(&expr.Args[i]) {
			return true
		}
	}
	for _, entry := range expr.Entries {
		if containsAwait(entry.Value) {
			return true
		}
	}
	return false
}

func isNullable(t *fast.Type) bool {
	return t != nil && t.Kind == "nullable"
}

func exportedIf(exported bool, name string) string {
	if exported {
		return names.Exported(name)
	}
	return names.Local(name)
}

func Debug(mod *fast.Module, cfg config.Config) string {
	irMod, diags := Lower(mod, cfg)
	return fmt.Sprintf("%+v\n%s", irMod, diags.String())
}
