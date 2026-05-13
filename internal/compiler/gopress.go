package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Gode-Ts/gode-compiler/internal/config"
	"github.com/Gode-Ts/gode-compiler/internal/diagnostics"
	"github.com/Gode-Ts/gode-compiler/internal/ir"
	"github.com/Gode-Ts/gode-compiler/internal/names"
)

type gopressApp struct {
	packageName  string
	path         string
	appName      string
	vars         map[string]string
	helpers      []gopressFunction
	helperShapes map[string]*gopressReturnObject
	handlers     []gopressHandler
	calls        []gopressCall
	diags        diagnostics.List
}

type gopressHandler struct {
	Name    string
	GoName  string
	Body    string
	IsError bool
}

type gopressFunction struct {
	Name         string
	Params       []gopressParam
	Body         string
	ReturnType   string
	ReturnObject *gopressReturnObject
}

type gopressParam struct {
	Name   string
	GoType string
}

type gopressCall struct {
	Receiver string
	Method   string
	Args     []string
}

type gopressEmitContext struct {
	imports        map[string]bool
	needsMergeJSON bool
}

type gopressRenderedFunc struct {
	Header     string
	Lines      []string
	ReturnType string
}

type gopressBodyContext struct {
	emit         *gopressEmitContext
	path         string
	builders     map[string]bool
	byteBuffers  map[string]int
	locals       map[string]gopressLocal
	helperShapes map[string]*gopressReturnObject
	returnObject *gopressReturnObject
	directParams bool
	rawResponse  bool
	jsonVarCount int
}

type gopressLocal struct {
	kind   string
	object *gopressReturnObject
}

type gopressBodyOptions struct {
	directParams bool
	rawResponse  bool
	helperShapes map[string]*gopressReturnObject
	returnObject *gopressReturnObject
}

type gopressReturnObject struct {
	StructName string
	Fields     []gopressObjectField
}

type gopressObjectField struct {
	Key     string
	GoName  string
	GoType  string
	Expr    string
	Dynamic bool
}

type gopressFastRequestUse struct {
	query   bool
	headers bool
	cookies bool
	body    bool
	locals  bool
}

type gopressRouteMetadata struct {
	Framework string                    `json:"framework"`
	Routes    []gopressRouteMetadataRow `json:"routes"`
	Mounts    []gopressMountMetadataRow `json:"mounts"`
}

type gopressRouteMetadataRow struct {
	Receiver string `json:"receiver"`
	Method   string `json:"method"`
	Path     string `json:"path"`
}

type gopressMountMetadataRow struct {
	Receiver string `json:"receiver"`
	Path     string `json:"path"`
	Target   string `json:"target"`
}

var (
	gopressAppVarRE        = regexp.MustCompile(`const\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(gopress|Router)\s*\(\s*\)`)
	gopressHandlerRE       = regexp.MustCompile(`export\s+(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)\s*:\s*Promise<[^>]+>\s*\{`)
	gopressHelperRE        = regexp.MustCompile(`(?:^|[;\n])\s*(?:export\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)\s*(?::\s*([A-Za-z_][A-Za-z0-9_<>\[\]\s|]*))?\s*\{`)
	gopressFunctionSpanRE  = regexp.MustCompile(`(?:^|[;\n])\s*(?:export\s+)?(?:async\s+)?function\s+[A-Za-z_][A-Za-z0-9_]*\s*\([^)]*\)\s*(?::\s*[A-Za-z_][A-Za-z0-9_<>\[\]\s|]*)?\s*\{`)
	gopressReturnObjectRE  = regexp.MustCompile(`\breturn\s*\{`)
	gopressBuilderVarRE    = regexp.MustCompile(`(?:^|[\s;{}])(?:let|var)\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s*:\s*string)?\s*=\s*(['"])`)
	gopressRequestMemberRE = regexp.MustCompile(`req\.(params|query|body|headers|cookies)\.([A-Za-z_][A-Za-z0-9_]*)`)
)

func isGopressSource(src []byte, cfg config.Config) bool {
	return cfg.Framework == "gopress" || strings.Contains(string(src), `from "gopress"`) || strings.Contains(string(src), `from 'gopress'`)
}

func CompileGopress(path string, src []byte, cfg config.Config) Result {
	app := parseGopress(path, string(src), cfg)
	if app.diags.HasErrors() && !cfg.EmitOnError {
		return Result{Diagnostics: app.diags}
	}
	goSrc, diags := app.emit()
	app.diags = append(app.diags, diags...)
	return Result{IR: ir.Module{PackageName: cfg.Package}, Go: goSrc, Metadata: app.routeMetadata(), Diagnostics: app.diags}
}

func parseGopress(path string, src string, cfg config.Config) *gopressApp {
	app := &gopressApp{packageName: cfg.Package, path: path, vars: map[string]string{}}
	if app.packageName == "" {
		app.packageName = "main"
	}
	if !strings.Contains(src, "gopress") {
		app.diags = append(app.diags, diagnostics.Errorf(path, diagnostics.Position{Line: 1, Column: 1}, "GODE_BIND_002", "gopress app must import from \"gopress\""))
	}
	for _, match := range gopressAppVarRE.FindAllStringSubmatch(src, -1) {
		kind := "app"
		if match[2] == "Router" {
			kind = "router"
		}
		app.vars[match[1]] = kind
		if kind == "app" && app.appName == "" {
			app.appName = match[1]
		}
	}
	app.handlers = parseGopressHandlers(src)
	app.helpers = parseGopressHelperFunctions(src, app.handlers)
	app.helperShapes = map[string]*gopressReturnObject{}
	for _, helper := range app.helpers {
		if helper.ReturnObject != nil {
			app.helperShapes[helper.Name] = helper.ReturnObject
		}
	}
	handlerSpans := findFunctionSpans(src)
	for _, call := range scanGopressCalls(src, handlerSpans) {
		if _, ok := app.vars[call.Receiver]; !ok {
			continue
		}
		if !isSupportedGopressMethod(call.Method) {
			app.diags = append(app.diags, diagnostics.Errorf(path, diagnostics.Position{Line: 1, Column: 1}, "GODE_SUBSET_001", "unsupported gopress API %q", call.Method))
			continue
		}
		app.calls = append(app.calls, call)
	}
	if app.appName == "" {
		app.diags = append(app.diags, diagnostics.Errorf(path, diagnostics.Position{Line: 1, Column: 1}, "GODE_BIND_002", "gopress app must declare const app = gopress()"))
	}
	return app
}

func parseGopressHandlers(src string) []gopressHandler {
	var handlers []gopressHandler
	for _, loc := range gopressHandlerRE.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		params := src[loc[4]:loc[5]]
		open := loc[1] - 1
		close := findMatching(src, open, '{', '}')
		if close < 0 {
			continue
		}
		handlers = append(handlers, gopressHandler{Name: name, GoName: names.Exported(name), Body: src[open+1 : close], IsError: isErrorParamList(params)})
	}
	return handlers
}

func parseGopressHelperFunctions(src string, handlers []gopressHandler) []gopressFunction {
	handlerNames := map[string]bool{}
	for _, handler := range handlers {
		handlerNames[handler.Name] = true
	}
	var functions []gopressFunction
	for _, loc := range gopressHelperRE.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		if handlerNames[name] {
			continue
		}
		params := src[loc[4]:loc[5]]
		declaredReturn := ""
		if loc[6] >= 0 && loc[7] >= 0 {
			declaredReturn = src[loc[6]:loc[7]]
		}
		open := loc[1] - 1
		close := findMatching(src, open, '{', '}')
		if close < 0 {
			continue
		}
		body := src[open+1 : close]
		parsedParams := optimizeGopressParams(parseGopressParams(params), body)
		returnObject := inferGopressReturnObject(name, body, parsedParams)
		returnType := inferGopressReturnType(declaredReturn, body)
		if returnObject != nil {
			returnType = returnObject.StructName
		}
		functions = append(functions, gopressFunction{
			Name:         name,
			Params:       parsedParams,
			Body:         body,
			ReturnType:   returnType,
			ReturnObject: returnObject,
		})
	}
	return functions
}

func parseGopressParams(params string) []gopressParam {
	var out []gopressParam
	for _, part := range splitTopLevelArgs(params) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		namePart, typePart, _ := strings.Cut(part, ":")
		name := strings.TrimSpace(strings.TrimSuffix(namePart, "?"))
		if name == "" {
			continue
		}
		out = append(out, gopressParam{Name: name, GoType: compileGopressType(typePart)})
	}
	return out
}

func compileGopressType(tsType string) string {
	switch strings.TrimSpace(tsType) {
	case "string":
		return "string"
	case "boolean", "bool":
		return "bool"
	case "number", "":
		return "float64"
	default:
		return "any"
	}
}

func optimizeGopressParams(params []gopressParam, body string) []gopressParam {
	for i := range params {
		if params[i].GoType == "float64" && isIntegerOnlyParamUse(body, params[i].Name) {
			params[i].GoType = "int"
		}
	}
	return params
}

func isIntegerOnlyParamUse(body string, name string) bool {
	if !paramAppearsInForCondition(body, name) {
		return false
	}
	if paramHasFloatyUse(body, name) {
		return false
	}
	return true
}

func inferGopressReturnType(declared string, body string) string {
	declared = strings.TrimSpace(declared)
	switch declared {
	case "void":
		return ""
	case "string":
		return "string"
	case "boolean", "bool":
		return "bool"
	case "number":
		return "float64"
	}
	if gopressReturnObjectRE.MatchString(body) {
		return "map[string]any"
	}
	if strings.Contains(body, "return ") {
		return "any"
	}
	return ""
}

func inferGopressReturnObject(name string, body string, params []gopressParam) *gopressReturnObject {
	expr, ok := findGopressReturnObject(body)
	if !ok {
		return nil
	}
	fields, ok := parseGopressObjectFields(expr)
	if !ok || len(fields) == 0 {
		return nil
	}
	locals := inferGopressLocalKinds(body, params, nil)
	object := &gopressReturnObject{StructName: name + "Result", Fields: make([]gopressObjectField, 0, len(fields))}
	for _, field := range fields {
		if field.Dynamic {
			return nil
		}
		goName := names.Local(field.Key)
		if !isGoIdentifier(goName) {
			return nil
		}
		goType := inferGopressGoType(field.Expr, locals)
		if goType == "" || goType == "any" {
			return nil
		}
		object.Fields = append(object.Fields, gopressObjectField{
			Key:    field.Key,
			GoName: goName,
			GoType: goType,
			Expr:   field.Expr,
		})
	}
	return object
}

func findGopressReturnObject(body string) (string, bool) {
	for pos := 0; pos < len(body); {
		pos = skipGopressSeparators(body, pos)
		if pos >= len(body) {
			break
		}
		if hasKeywordAt(body, pos, "if") || hasKeywordAt(body, pos, "for") {
			open := strings.IndexByte(body[pos:], '{')
			if open < 0 {
				return "", false
			}
			open += pos
			close := findMatching(body, open, '{', '}')
			if close < 0 {
				return "", false
			}
			pos = close + 1
			continue
		}
		stmt, next := readGopressStatement(body, pos)
		trimmed := strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
		if strings.HasPrefix(trimmed, "return ") {
			expr := strings.TrimSpace(strings.TrimPrefix(trimmed, "return "))
			if strings.HasPrefix(expr, "{") {
				return expr, true
			}
			return "", false
		}
		pos = next
	}
	return "", false
}

type gopressObjectFieldExpr struct {
	Key     string
	Expr    string
	Dynamic bool
}

func parseGopressObjectFields(expr string) ([]gopressObjectFieldExpr, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "{") {
		return nil, false
	}
	close := findMatching(expr, 0, '{', '}')
	if close != len(expr)-1 {
		return nil, false
	}
	body := strings.TrimSpace(expr[1:close])
	if body == "" {
		return nil, true
	}
	parts := splitTopLevelArgs(body)
	fields := make([]gopressObjectFieldExpr, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "...") {
			fields = append(fields, gopressObjectFieldExpr{Expr: strings.TrimSpace(strings.TrimPrefix(part, "...")), Dynamic: true})
			continue
		}
		key := ""
		value := ""
		if idx := findTopLevelColon(part); idx >= 0 {
			key = strings.Trim(strings.TrimSpace(part[:idx]), `"'`)
			value = strings.TrimSpace(part[idx+1:])
		} else {
			key = strings.Trim(strings.TrimSpace(part), `"'`)
			value = part
		}
		if key == "" || value == "" {
			return nil, false
		}
		fields = append(fields, gopressObjectFieldExpr{Key: key, Expr: value})
	}
	return fields, true
}

func inferGopressLocalKinds(body string, params []gopressParam, helperShapes map[string]*gopressReturnObject) map[string]gopressLocal {
	locals := map[string]gopressLocal{}
	for _, param := range params {
		locals[param.Name] = gopressLocal{kind: param.GoType}
	}
	for pos := 0; pos < len(body); {
		pos = skipGopressSeparators(body, pos)
		if pos >= len(body) {
			break
		}
		if hasKeywordAt(body, pos, "for") {
			openHeader := skipSpace(body, pos+len("for"))
			closeHeader := -1
			if openHeader < len(body) && body[openHeader] == '(' {
				closeHeader = findMatching(body, openHeader, '(', ')')
			}
			openBody := -1
			closeBody := -1
			if closeHeader >= 0 {
				openBody = skipSpace(body, closeHeader+1)
				if openBody < len(body) && body[openBody] == '{' {
					closeBody = findMatching(body, openBody, '{', '}')
				}
			}
			if closeHeader > openHeader {
				parts := splitTopLevelSemicolons(body[openHeader+1 : closeHeader])
				if len(parts) == 3 {
					recordGopressVarKind(parts[0], locals, helperShapes)
				}
			}
			if closeBody >= 0 {
				pos = closeBody + 1
				continue
			}
		}
		stmt, next := readGopressStatement(body, pos)
		recordGopressVarKind(stmt, locals, helperShapes)
		pos = next
	}
	return locals
}

func recordGopressVarKind(stmt string, locals map[string]gopressLocal, helperShapes map[string]*gopressReturnObject) {
	name, expr, ok := parseGopressVarDecl(stmt)
	if !ok {
		return
	}
	local := gopressLocal{kind: inferGopressGoType(expr, locals)}
	if shape := helperShapeForCall(expr, helperShapes); shape != nil {
		local.kind = "object"
		local.object = shape
	}
	locals[name] = local
}

func parseGopressVarDecl(stmt string) (string, string, bool) {
	stmt = strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
	for _, keyword := range []string{"const", "let", "var"} {
		if !hasKeywordAt(stmt, 0, keyword) {
			continue
		}
		rest := strings.TrimSpace(stmt[len(keyword):])
		namePart, expr, hasValue := strings.Cut(rest, "=")
		if !hasValue {
			return "", "", false
		}
		name, _, _ := strings.Cut(strings.TrimSpace(namePart), ":")
		name = strings.TrimSpace(strings.TrimSuffix(name, "?"))
		expr = strings.TrimSpace(expr)
		return name, expr, name != "" && expr != ""
	}
	return "", "", false
}

func inferGopressGoType(expr string, locals map[string]gopressLocal) string {
	expr = strings.TrimSpace(expr)
	switch {
	case isStringLiteral(expr):
		return "string"
	case isIntegerLiteral(expr):
		return "int"
	case isNumberLiteral(expr):
		return "float64"
	case expr == "true" || expr == "false":
		return "bool"
	case expr == "performance.now()":
		return "time"
	case strings.HasPrefix(expr, "performance.now() -"):
		return "float64"
	}
	if local, ok := locals[expr]; ok {
		return local.kind
	}
	return ""
}

func helperShapeForCall(expr string, helperShapes map[string]*gopressReturnObject) *gopressReturnObject {
	if len(helperShapes) == 0 {
		return nil
	}
	expr = strings.TrimSpace(expr)
	open := strings.IndexByte(expr, '(')
	if open <= 0 || !strings.HasSuffix(expr, ")") {
		return nil
	}
	name := strings.TrimSpace(expr[:open])
	if !isGoIdentifier(name) {
		return nil
	}
	close := findMatching(expr, open, '(', ')')
	if close != len(expr)-1 {
		return nil
	}
	return helperShapes[name]
}

func isGoIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !isIdentStartRune(r) {
				return false
			}
			continue
		}
		if !isIdentPartRune(r) {
			return false
		}
	}
	return true
}

func findFunctionSpans(src string) [][2]int {
	var spans [][2]int
	for _, loc := range gopressFunctionSpanRE.FindAllStringIndex(src, -1) {
		open := loc[1] - 1
		close := findMatching(src, open, '{', '}')
		if close >= 0 {
			spans = append(spans, [2]int{loc[0], close + 1})
		}
	}
	return spans
}

func scanGopressCalls(src string, skip [][2]int) []gopressCall {
	var calls []gopressCall
	for i := 0; i < len(src); i++ {
		if inSpans(i, skip) || !isIdentStartRune(rune(src[i])) {
			continue
		}
		start := i
		for i < len(src) && isIdentPartRune(rune(src[i])) {
			i++
		}
		receiver := src[start:i]
		j := skipSpace(src, i)
		if j >= len(src) || src[j] != '.' {
			continue
		}
		j++
		methodStart := skipSpace(src, j)
		methodEnd := methodStart
		for methodEnd < len(src) && isIdentPartRune(rune(src[methodEnd])) {
			methodEnd++
		}
		method := src[methodStart:methodEnd]
		open := skipSpace(src, methodEnd)
		if open >= len(src) || src[open] != '(' {
			continue
		}
		close := findMatching(src, open, '(', ')')
		if close < 0 {
			continue
		}
		args := splitTopLevelArgs(src[open+1 : close])
		if strings.EqualFold(method, "route") {
			chainDot := skipSpace(src, close+1)
			if chainDot < len(src) && src[chainDot] == '.' {
				routeMethodStart := skipSpace(src, chainDot+1)
				routeMethodEnd := routeMethodStart
				for routeMethodEnd < len(src) && isIdentPartRune(rune(src[routeMethodEnd])) {
					routeMethodEnd++
				}
				routeMethod := src[routeMethodStart:routeMethodEnd]
				routeOpen := skipSpace(src, routeMethodEnd)
				if routeOpen < len(src) && src[routeOpen] == '(' {
					routeClose := findMatching(src, routeOpen, '(', ')')
					if routeClose >= 0 {
						routeArgs := append([]string{}, args...)
						routeArgs = append(routeArgs, splitTopLevelArgs(src[routeOpen+1:routeClose])...)
						calls = append(calls, gopressCall{Receiver: receiver, Method: "route." + routeMethod, Args: routeArgs})
						i = routeClose
						continue
					}
				}
			}
		}
		calls = append(calls, gopressCall{Receiver: receiver, Method: method, Args: args})
		i = close
	}
	return calls
}

func (a *gopressApp) emit() (string, diagnostics.List) {
	ctx := newGopressEmitContext()
	ctx.addImport("github.com/Gode-Ts/gopress")
	var helperBlocks []gopressRenderedFunc
	for _, helper := range a.helpers {
		params := make([]string, 0, len(helper.Params))
		for _, param := range helper.Params {
			params = append(params, fmt.Sprintf("%s %s", param.Name, param.GoType))
		}
		returnType := ""
		if helper.ReturnType != "" {
			returnType = " " + helper.ReturnType
		}
		lines, bodyDiags := compileGopressBodyWithOptions(ctx, a.path, helper.Body, helper.Params, gopressBodyOptions{
			helperShapes: a.helperShapes,
			returnObject: helper.ReturnObject,
		})
		a.diags = append(a.diags, bodyDiags...)
		helperBlocks = append(helperBlocks, gopressRenderedFunc{
			Header:     fmt.Sprintf("func %s(%s)%s {", helper.Name, strings.Join(params, ", "), returnType),
			Lines:      lines,
			ReturnType: helper.ReturnType,
		})
	}
	var handlerBlocks []gopressRenderedFunc
	for _, handler := range a.handlers {
		header := ""
		if handler.IsError {
			header = fmt.Sprintf("func %s(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {", handler.GoName)
		} else {
			header = fmt.Sprintf("func %s(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {", handler.GoName)
		}
		lines, bodyDiags := compileGopressBodyWithOptions(ctx, a.path, handler.Body, nil, gopressBodyOptions{helperShapes: a.helperShapes})
		a.diags = append(a.diags, bodyDiags...)
		handlerBlocks = append(handlerBlocks, gopressRenderedFunc{Header: header, Lines: lines})
	}
	var callLines []string
	for _, call := range a.calls {
		callLines = append(callLines, a.emitGopressCall(ctx, call)...)
	}

	var b bytes.Buffer
	w := func(format string, args ...any) {
		if len(args) == 0 {
			b.WriteString(format)
		} else {
			fmt.Fprintf(&b, format, args...)
		}
		b.WriteByte('\n')
	}
	w("// Code generated by godec; DO NOT EDIT.")
	w("")
	w("package %s", a.packageName)
	w("")
	w("import (")
	for _, imp := range ctx.sortedImports() {
		w("%q", imp)
	}
	w(")")
	w("")
	if ctx.needsMergeJSON {
		w("func godeMergeJSON(parts ...map[string]any) map[string]any {")
		w("out := map[string]any{}")
		w("for _, part := range parts {")
		w("for key, value := range part {")
		w("out[key] = value")
		w("}")
		w("}")
		w("return out")
		w("}")
		w("")
	}
	for _, helper := range a.helpers {
		if helper.ReturnObject == nil {
			continue
		}
		w("type %s struct {", helper.ReturnObject.StructName)
		for _, field := range helper.ReturnObject.Fields {
			w("%s %s", field.GoName, field.GoType)
		}
		w("}")
		w("")
	}
	for _, helper := range helperBlocks {
		w("%s", helper.Header)
		for _, line := range helper.Lines {
			w("%s", line)
		}
		if helper.ReturnType != "" && !gopressLinesReturn(helper.Lines) {
			w("%s", zeroReturnStatement(helper.ReturnType))
		}
		w("}")
		w("")
	}
	for _, handler := range handlerBlocks {
		w("%s", handler.Header)
		for _, line := range handler.Lines {
			w("%s", line)
		}
		w("return nil")
		w("}")
		w("")
	}
	w("func BuildGopressApp() *gopress.App {")
	for _, pair := range sortedVars(a.vars) {
		name, kind := pair[0], pair[1]
		if kind == "app" {
			w("%s := gopress.New()", name)
		} else {
			w("%s := gopress.Router()", name)
		}
	}
	for _, line := range callLines {
		w("%s", line)
	}
	w("return %s", a.appName)
	w("}")
	formatted, err := format.Source(b.Bytes())
	if err != nil {
		return b.String(), diagnostics.List{diagnostics.Errorf("", diagnostics.Position{Line: 1, Column: 1}, "GODE_CODEGEN_001", "generated Go is invalid: %v", err)}
	}
	return string(formatted), nil
}

func (a *gopressApp) routeMetadata() string {
	metadata := gopressRouteMetadata{Framework: "gopress", Routes: []gopressRouteMetadataRow{}, Mounts: []gopressMountMetadataRow{}}
	for _, call := range a.calls {
		method := strings.ToLower(call.Method)
		switch {
		case method == "use" && len(call.Args) >= 2 && isStringLiteral(strings.TrimSpace(call.Args[0])):
			for _, arg := range call.Args[1:] {
				target := strings.TrimSpace(arg)
				if _, ok := a.vars[target]; ok {
					metadata.Mounts = append(metadata.Mounts, gopressMountMetadataRow{Receiver: call.Receiver, Path: metadataPath(call.Args[0]), Target: target})
				}
			}
		case isRouteMethod(method) && len(call.Args) >= 1:
			metadata.Routes = append(metadata.Routes, gopressRouteMetadataRow{Receiver: call.Receiver, Method: strings.ToUpper(method), Path: metadataPath(call.Args[0])})
		case strings.HasPrefix(method, "route.") && len(call.Args) >= 1:
			metadata.Routes = append(metadata.Routes, gopressRouteMetadataRow{Receiver: call.Receiver, Method: strings.ToUpper(strings.TrimPrefix(method, "route.")), Path: metadataPath(call.Args[0])})
		}
	}
	sort.SliceStable(metadata.Routes, func(i, j int) bool {
		if metadata.Routes[i].Receiver != metadata.Routes[j].Receiver {
			return metadata.Routes[i].Receiver < metadata.Routes[j].Receiver
		}
		if metadata.Routes[i].Path != metadata.Routes[j].Path {
			return metadata.Routes[i].Path < metadata.Routes[j].Path
		}
		return metadata.Routes[i].Method < metadata.Routes[j].Method
	})
	sort.SliceStable(metadata.Mounts, func(i, j int) bool {
		if metadata.Mounts[i].Receiver != metadata.Mounts[j].Receiver {
			return metadata.Mounts[i].Receiver < metadata.Mounts[j].Receiver
		}
		if metadata.Mounts[i].Path != metadata.Mounts[j].Path {
			return metadata.Mounts[i].Path < metadata.Mounts[j].Path
		}
		return metadata.Mounts[i].Target < metadata.Mounts[j].Target
	})
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return ""
	}
	return string(data) + "\n"
}

func metadataPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(trimmed); err == nil {
		return unquoted
	}
	return trimmed
}

func sortedVars(vars map[string]string) [][2]string {
	var out [][2]string
	for name, kind := range vars {
		out = append(out, [2]string{name, kind})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func newGopressEmitContext() *gopressEmitContext {
	return &gopressEmitContext{imports: map[string]bool{}}
}

func (c *gopressEmitContext) addImport(path string) {
	c.imports[path] = true
}

func (c *gopressEmitContext) merge(other *gopressEmitContext) {
	for path := range other.imports {
		c.imports[path] = true
	}
	if other.needsMergeJSON {
		c.needsMergeJSON = true
	}
}

func (c *gopressEmitContext) sortedImports() []string {
	imports := make([]string, 0, len(c.imports))
	for path := range c.imports {
		imports = append(imports, path)
	}
	sort.Strings(imports)
	return imports
}

func (a *gopressApp) emitGopressCall(ctx *gopressEmitContext, call gopressCall) []string {
	method := strings.ToLower(call.Method)
	switch method {
	case "use":
		return a.emitUseCall(ctx, call)
	case "all", "get", "post", "put", "patch", "delete", "options", "head":
		if len(call.Args) < 2 {
			return nil
		}
		if len(call.Args) == 2 {
			if raw, ok := a.compileInlineRawHandler(ctx, call.Args[1]); ok {
				return []string{fmt.Sprintf("%s.HandleRaw(%q, %s, %s)", call.Receiver, strings.ToUpper(method), call.Args[0], raw)}
			}
			if usage, ok := a.fastHandlerUsage(call.Args[1]); ok {
				return []string{fmt.Sprintf("%s.HandleFastOptions(%q, %s, %s, %s)", call.Receiver, strings.ToUpper(method), call.Args[0], usage.compileOptions(), a.compileInlineFastHandler(ctx, call.Args[1]))}
			}
		}
		args := []string{call.Args[0]}
		for _, arg := range call.Args[1:] {
			args = append(args, a.compileHandlerArg(ctx, arg))
		}
		return []string{fmt.Sprintf("%s.%s(%s)", call.Receiver, names.Exported(method), strings.Join(args, ", "))}
	case "route.all", "route.get", "route.post", "route.put", "route.patch", "route.delete", "route.options", "route.head":
		if len(call.Args) < 2 {
			return nil
		}
		routeMethod := strings.TrimPrefix(method, "route.")
		args := make([]string, 0, len(call.Args)-1)
		for _, arg := range call.Args[1:] {
			args = append(args, a.compileHandlerArg(ctx, arg))
		}
		return []string{fmt.Sprintf("%s.Route(%s).%s(%s)", call.Receiver, call.Args[0], names.Exported(routeMethod), strings.Join(args, ", "))}
	default:
		return nil
	}
}

func (a *gopressApp) emitUseCall(ctx *gopressEmitContext, call gopressCall) []string {
	var lines []string
	prefix := ""
	args := call.Args
	if len(args) > 0 && isStringLiteral(strings.TrimSpace(args[0])) {
		prefix = strings.TrimSpace(args[0])
		args = args[1:]
	}
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if a.isErrorHandlerArg(trimmed) {
			lines = append(lines, fmt.Sprintf("%s.UseError(%s)", call.Receiver, a.compileErrorHandlerArg(ctx, trimmed)))
			continue
		}
		useArgs := []string{}
		if prefix != "" {
			useArgs = append(useArgs, prefix)
		}
		useArgs = append(useArgs, a.compileUseArg(ctx, trimmed))
		lines = append(lines, fmt.Sprintf("%s.Use(%s)", call.Receiver, strings.Join(useArgs, ", ")))
	}
	return lines
}

func (a *gopressApp) compileUseArg(ctx *gopressEmitContext, arg string) string {
	trimmed := strings.TrimSpace(arg)
	switch {
	case trimmed == "gopress.json()":
		return "gopress.JSON()"
	case strings.HasPrefix(trimmed, "gopress.static("):
		return "gopress.Static(" + insideCall(trimmed) + ")"
	case isStringLiteral(trimmed):
		return trimmed
	default:
		return a.compileHandlerArg(ctx, trimmed)
	}
}

func (a *gopressApp) compileHandlerArg(ctx *gopressEmitContext, arg string) string {
	trimmed := strings.TrimSpace(arg)
	if strings.Contains(trimmed, "=>") {
		return a.compileInlineHandler(ctx, trimmed)
	}
	if _, ok := a.vars[trimmed]; ok {
		return trimmed
	}
	return names.Exported(trimmed)
}

func (a *gopressApp) compileErrorHandlerArg(ctx *gopressEmitContext, arg string) string {
	trimmed := strings.TrimSpace(arg)
	if strings.Contains(trimmed, "=>") {
		return a.compileInlineErrorHandler(ctx, trimmed)
	}
	return names.Exported(trimmed)
}

func (a *gopressApp) isErrorHandlerArg(arg string) bool {
	if strings.Contains(arg, "=>") {
		return isErrorArrow(arg)
	}
	for _, handler := range a.handlers {
		if handler.Name == arg || handler.GoName == arg {
			return handler.IsError
		}
	}
	return false
}

func (a *gopressApp) compileInlineHandler(ctx *gopressEmitContext, src string) string {
	bodyStart := strings.Index(src, "{")
	bodyEnd := strings.LastIndex(src, "}")
	if bodyStart < 0 || bodyEnd < bodyStart {
		return "func(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error { return nil }"
	}
	lines, bodyDiags := compileGopressBodyWithOptions(ctx, a.path, src[bodyStart+1:bodyEnd], nil, gopressBodyOptions{helperShapes: a.helperShapes})
	a.diags = append(a.diags, bodyDiags...)
	return formatGopressFunc("func(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error", lines)
}

func (a *gopressApp) canCompileFastHandler(src string) bool {
	_, ok := a.fastHandlerUsage(src)
	return ok
}

func (a *gopressApp) fastHandlerUsage(src string) (gopressFastRequestUse, bool) {
	trimmed := strings.TrimSpace(src)
	if !strings.Contains(trimmed, "=>") || strings.Contains(trimmed, "next") || isErrorArrow(trimmed) {
		return gopressFastRequestUse{}, false
	}
	usage := gopressFastRequestUse{
		query:   strings.Contains(trimmed, "req.query"),
		headers: strings.Contains(trimmed, "req.headers"),
		cookies: strings.Contains(trimmed, "req.cookies"),
		body:    strings.Contains(trimmed, "req.body"),
		locals:  strings.Contains(trimmed, "req.locals"),
	}
	return usage, true
}

func (u gopressFastRequestUse) compileOptions() string {
	var fields []string
	if u.query {
		fields = append(fields, "Query: true")
	}
	if u.headers {
		fields = append(fields, "Headers: true")
	}
	if u.cookies {
		fields = append(fields, "Cookies: true")
	}
	if u.body {
		fields = append(fields, "Body: true")
	}
	if u.locals {
		fields = append(fields, "Locals: true")
	}
	if len(fields) == 0 {
		return "gopress.FastRequestOptions{}"
	}
	return "gopress.FastRequestOptions{" + strings.Join(fields, ", ") + "}"
}

func (a *gopressApp) compileInlineFastHandler(ctx *gopressEmitContext, src string) string {
	bodyStart := strings.Index(src, "{")
	bodyEnd := strings.LastIndex(src, "}")
	if bodyStart < 0 || bodyEnd < bodyStart {
		return "func(req *gopress.Request, res *gopress.Response) error { return nil }"
	}
	lines, bodyDiags := compileGopressBodyWithOptions(ctx, a.path, src[bodyStart+1:bodyEnd], nil, gopressBodyOptions{directParams: true, helperShapes: a.helperShapes})
	a.diags = append(a.diags, bodyDiags...)
	return formatGopressFunc("func(req *gopress.Request, res *gopress.Response) error", lines)
}

func (a *gopressApp) compileInlineRawHandler(ctx *gopressEmitContext, src string) (string, bool) {
	if !canCompileRawHandler(src) {
		return "", false
	}
	bodyStart := strings.Index(src, "{")
	bodyEnd := strings.LastIndex(src, "}")
	if bodyStart < 0 || bodyEnd < bodyStart {
		return "", false
	}
	body := src[bodyStart+1 : bodyEnd]
	rawCtx := newGopressEmitContext()
	lines, bodyDiags := compileGopressBodyWithOptions(rawCtx, a.path, body, nil, gopressBodyOptions{rawResponse: true, helperShapes: a.helperShapes})
	if bodyDiags.HasErrors() {
		return "", false
	}
	rawLines, ok := rewriteRawHandlerLines(lines)
	if !ok {
		return "", false
	}
	ctx.merge(rawCtx)
	a.diags = append(a.diags, bodyDiags...)
	ctx.addImport("net/http")
	return formatGopressFunc("func(w http.ResponseWriter, request *http.Request) error", rawLines), true
}

func canCompileRawHandler(src string) bool {
	trimmed := strings.TrimSpace(src)
	if !strings.Contains(trimmed, "=>") || strings.Contains(trimmed, "req.") || strings.Contains(trimmed, "next") || isErrorArrow(trimmed) {
		return false
	}
	for _, unsupported := range []string{"res.set(", "res.cookie(", "res.redirect(", "res.sendFile(", "res.sendStatus("} {
		if strings.Contains(trimmed, unsupported) {
			return false
		}
	}
	return strings.Contains(trimmed, "res.")
}

func rewriteRawHandlerLines(lines []string) ([]string, bool) {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		rewritten, ok := rewriteRawHandlerLine(line)
		if !ok {
			return nil, false
		}
		out = append(out, rewritten)
	}
	return out, true
}

func rewriteRawHandlerLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "res.") {
		return line, true
	}
	if !strings.HasPrefix(trimmed, "return ") {
		return "", false
	}
	expr := strings.TrimSpace(strings.TrimPrefix(trimmed, "return "))
	switch {
	case strings.HasPrefix(expr, "res.JSONBytes("):
		arg, ok := singleCallArg(expr, "res.JSONBytes")
		if !ok {
			return "", false
		}
		return "return gopress.WriteJSONBytes(w, 200, " + arg + ")", true
	case strings.HasPrefix(expr, "res.JSONString("):
		arg, ok := singleCallArg(expr, "res.JSONString")
		if !ok {
			return "", false
		}
		return "return gopress.WriteJSONString(w, 200, " + arg + ")", true
	case strings.HasPrefix(expr, "res.StatusJSON("):
		args, ok := callArgs(expr, "res.StatusJSON")
		if !ok || len(args) != 2 {
			return "", false
		}
		return "return gopress.WriteJSONString(w, " + args[0] + ", " + args[1] + ")", true
	case strings.HasPrefix(expr, "res.StatusSend("):
		args, ok := callArgs(expr, "res.StatusSend")
		if !ok || len(args) != 3 {
			return "", false
		}
		return "return gopress.WriteRawString(w, " + args[0] + ", " + args[1] + ", " + args[2] + ")", true
	case strings.HasPrefix(expr, "res.Status("):
		return rewriteRawStatusChain(expr)
	case strings.HasPrefix(expr, "res.Send("):
		arg, ok := singleCallArg(expr, "res.Send")
		if !ok {
			return "", false
		}
		return `return gopress.WriteRawString(w, 200, "text/plain", ` + arg + ")", true
	}
	return "", false
}

func rewriteRawStatusChain(expr string) (string, bool) {
	open := strings.IndexByte(expr, '(')
	close := findMatching(expr, open, '(', ')')
	if close < 0 {
		return "", false
	}
	status := strings.TrimSpace(expr[open+1 : close])
	rest := strings.TrimSpace(expr[close+1:])
	if strings.HasPrefix(rest, ".Send(") {
		arg, ok := singleCallArg("res"+rest, "res.Send")
		if !ok {
			return "", false
		}
		return `return gopress.WriteRawString(w, ` + status + `, "text/plain", ` + arg + ")", true
	}
	if strings.HasPrefix(rest, ".JSON(") {
		arg, ok := singleCallArg("res"+rest, "res.JSON")
		if !ok {
			return "", false
		}
		return "return gopress.WriteJSON(w, " + status + ", " + arg + ")", true
	}
	return "", false
}

func singleCallArg(expr string, name string) (string, bool) {
	args, ok := callArgs(expr, name)
	if !ok || len(args) != 1 {
		return "", false
	}
	return args[0], true
}

func callArgs(expr string, name string) ([]string, bool) {
	if !strings.HasPrefix(expr, name) {
		return nil, false
	}
	open := skipSpace(expr, len(name))
	if open >= len(expr) || expr[open] != '(' {
		return nil, false
	}
	close := findMatching(expr, open, '(', ')')
	if close != len(expr)-1 {
		return nil, false
	}
	return splitTopLevelArgs(expr[open+1 : close]), true
}

func (a *gopressApp) compileInlineErrorHandler(ctx *gopressEmitContext, src string) string {
	bodyStart := strings.Index(src, "{")
	bodyEnd := strings.LastIndex(src, "}")
	if bodyStart < 0 || bodyEnd < bodyStart {
		return "func(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error { return nil }"
	}
	lines, bodyDiags := compileGopressBodyWithOptions(ctx, a.path, src[bodyStart+1:bodyEnd], nil, gopressBodyOptions{helperShapes: a.helperShapes})
	a.diags = append(a.diags, bodyDiags...)
	return formatGopressFunc("func(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error", lines)
}

func formatGopressFunc(header string, lines []string) string {
	body := strings.Join(lines, "\n")
	if !gopressLinesReturn(lines) {
		if body != "" {
			body += "\n"
		}
		body += "return nil"
	}
	return fmt.Sprintf("%s {\n%s\n}", header, body)
}

func gopressLinesReturn(lines []string) bool {
	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := strings.TrimSpace(lines[idx])
		if line == "" || line == "}" {
			continue
		}
		return strings.HasPrefix(line, "return ")
	}
	return false
}

func compileGopressBody(ctx *gopressEmitContext, path string, body string, params []gopressParam) ([]string, diagnostics.List) {
	return compileGopressBodyWithOptions(ctx, path, body, params, gopressBodyOptions{})
}

func compileGopressBodyWithOptions(ctx *gopressEmitContext, path string, body string, params []gopressParam, options gopressBodyOptions) ([]string, diagnostics.List) {
	builders := discoverStringBuilderVars(body)
	byteBuffers := discoverJSONByteBufferVars(body, builders)
	for name := range byteBuffers {
		delete(builders, name)
	}
	if len(builders) > 0 {
		ctx.addImport("strings")
	}
	bodyCtx := &gopressBodyContext{
		emit:         ctx,
		path:         path,
		builders:     builders,
		byteBuffers:  byteBuffers,
		locals:       map[string]gopressLocal{},
		helperShapes: options.helperShapes,
		returnObject: options.returnObject,
		directParams: options.directParams,
		rawResponse:  options.rawResponse,
	}
	for _, param := range params {
		bodyCtx.locals[param.Name] = gopressLocal{kind: param.GoType}
	}
	return bodyCtx.compile(body)
}

func (c *gopressBodyContext) compile(body string) ([]string, diagnostics.List) {
	var out []string
	var diags diagnostics.List
	for pos := 0; pos < len(body); {
		pos = skipGopressSeparators(body, pos)
		if pos >= len(body) {
			break
		}
		switch {
		case hasKeywordAt(body, pos, "if"):
			lines, next, blockDiags := c.compileIf(body, pos)
			out = append(out, lines...)
			diags = append(diags, blockDiags...)
			pos = next
		case hasKeywordAt(body, pos, "for"):
			lines, next, blockDiags := c.compileFor(body, pos)
			out = append(out, lines...)
			diags = append(diags, blockDiags...)
			pos = next
		default:
			stmt, next := readGopressStatement(body, pos)
			lines, stmtDiags := c.compileStatement(stmt)
			out = append(out, lines...)
			diags = append(diags, stmtDiags...)
			pos = next
		}
	}
	return out, diags
}

func (c *gopressBodyContext) compileIf(src string, pos int) ([]string, int, diagnostics.List) {
	openCond := skipSpace(src, pos+len("if"))
	if openCond >= len(src) || src[openCond] != '(' {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	closeCond := findMatching(src, openCond, '(', ')')
	if closeCond < 0 {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	openBody := skipSpace(src, closeCond+1)
	if openBody >= len(src) || src[openBody] != '{' {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	closeBody := findMatching(src, openBody, '{', '}')
	if closeBody < 0 {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	bodyLines, diags := c.compile(src[openBody+1 : closeBody])
	out := []string{fmt.Sprintf("if %s {", c.compileExpr(src[openCond+1:closeCond]))}
	out = append(out, bodyLines...)
	next := closeBody + 1
	elsePos := skipGopressSeparators(src, next)
	if hasKeywordAt(src, elsePos, "else") {
		afterElse := skipSpace(src, elsePos+len("else"))
		switch {
		case hasKeywordAt(src, afterElse, "if"):
			elseLines, elseNext, elseDiags := c.compileIf(src, afterElse)
			out = append(out, "} else {")
			out = append(out, elseLines...)
			out = append(out, "}")
			diags = append(diags, elseDiags...)
			return out, elseNext, diags
		case afterElse < len(src) && src[afterElse] == '{':
			closeElse := findMatching(src, afterElse, '{', '}')
			if closeElse < 0 {
				diags = append(diags, unsupportedGopressStatement(c.path, readRestLine(src, elsePos)))
				out = append(out, "}")
				return out, len(src), diags
			}
			elseBody, elseDiags := c.compile(src[afterElse+1 : closeElse])
			out = append(out, "} else {")
			out = append(out, elseBody...)
			out = append(out, "}")
			diags = append(diags, elseDiags...)
			return out, closeElse + 1, diags
		}
		diags = append(diags, unsupportedGopressStatement(c.path, readRestLine(src, elsePos)))
	}
	out = append(out, "}")
	return out, next, diags
}

func (c *gopressBodyContext) compileFor(src string, pos int) ([]string, int, diagnostics.List) {
	openHeader := skipSpace(src, pos+len("for"))
	if openHeader >= len(src) || src[openHeader] != '(' {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	closeHeader := findMatching(src, openHeader, '(', ')')
	if closeHeader < 0 {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	openBody := skipSpace(src, closeHeader+1)
	if openBody >= len(src) || src[openBody] != '{' {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	closeBody := findMatching(src, openBody, '{', '}')
	if closeBody < 0 {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	parts := splitTopLevelSemicolons(src[openHeader+1 : closeHeader])
	if len(parts) != 3 {
		return nil, closeBody + 1, diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	init, ok := c.compileForInit(parts[0])
	if !ok {
		return nil, closeBody + 1, diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	post, ok := c.compileForPost(parts[2])
	if !ok {
		return nil, closeBody + 1, diagnostics.List{unsupportedGopressStatement(c.path, readRestLine(src, pos))}
	}
	bodyLines, diags := c.compile(src[openBody+1 : closeBody])
	out := []string{fmt.Sprintf("for %s; %s; %s {", init, c.compileExpr(parts[1]), post)}
	out = append(out, bodyLines...)
	out = append(out, "}")
	return out, closeBody + 1, diags
}

func (c *gopressBodyContext) compileStatement(stmt string) ([]string, diagnostics.List) {
	stmt = strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
	if stmt == "" {
		return nil, nil
	}
	if lines, ok := c.compileVarDecl(stmt, false); ok {
		return lines, nil
	}
	if strings.HasPrefix(stmt, "return ") {
		expr := strings.TrimSpace(strings.TrimPrefix(stmt, "return "))
		if c.rawResponse && strings.HasPrefix(expr, "res.") {
			if lines, ok := c.compileRawResponseStatement(expr); ok {
				return lines, nil
			}
		}
		if c.returnObject != nil && strings.HasPrefix(expr, "{") {
			if compiled, ok := c.compileReturnObjectLiteral(expr); ok {
				return []string{"return " + compiled}, nil
			}
		}
		return []string{"return " + c.compileExpr(expr)}, nil
	}
	if strings.HasPrefix(stmt, "res.") || strings.HasPrefix(stmt, "next(") {
		if c.rawResponse && strings.HasPrefix(stmt, "res.") {
			if lines, ok := c.compileRawResponseStatement(stmt); ok {
				return lines, nil
			}
		}
		return []string{"return " + c.compileExpr(stmt)}, nil
	}
	if lines, ok := c.compileAssignment(stmt); ok {
		return lines, nil
	}
	if isSimpleCallStatement(stmt) {
		return []string{c.compileExpr(stmt)}, nil
	}
	return nil, diagnostics.List{unsupportedGopressStatement(c.path, stmt)}
}

func (c *gopressBodyContext) compileForInit(stmt string) (string, bool) {
	if lines, ok := c.compileVarDecl(stmt, true); ok && len(lines) == 1 {
		return lines[0], true
	}
	lines, ok := c.compileAssignment(stmt)
	if !ok || len(lines) != 1 {
		return "", false
	}
	return lines[0], true
}

func (c *gopressBodyContext) compileForPost(stmt string) (string, bool) {
	lines, ok := c.compileAssignment(stmt)
	if !ok || len(lines) != 1 {
		return "", false
	}
	return lines[0], true
}

func (c *gopressBodyContext) compileVarDecl(stmt string, forClause bool) ([]string, bool) {
	stmt = strings.TrimSpace(stmt)
	keyword := ""
	for _, candidate := range []string{"const", "let", "var"} {
		if hasKeywordAt(stmt, 0, candidate) {
			keyword = candidate
			stmt = strings.TrimSpace(stmt[len(candidate):])
			break
		}
	}
	if keyword == "" {
		return nil, false
	}
	namePart, expr, hasValue := strings.Cut(stmt, "=")
	if !hasValue {
		return nil, false
	}
	name, _, _ := strings.Cut(strings.TrimSpace(namePart), ":")
	name = strings.TrimSpace(strings.TrimSuffix(name, "?"))
	if name == "" {
		return nil, false
	}
	expr = strings.TrimSpace(expr)
	if c.builders[name] && !forClause {
		c.locals[name] = gopressLocal{kind: "builder"}
		lines := []string{fmt.Sprintf("var %s strings.Builder", name)}
		lines = append(lines, c.compileBuilderAppend(name, expr)...)
		return lines, true
	}
	if capacity, ok := c.byteBuffers[name]; ok && !forClause {
		c.locals[name] = gopressLocal{kind: "bytes"}
		lines := []string{fmt.Sprintf("%s := make([]byte, 0, %d)", name, capacity)}
		lines = append(lines, c.compileByteAppend(name, expr)...)
		return lines, true
	}
	compiled := c.compileExpr(expr)
	local := gopressLocal{kind: c.inferLocalKind(expr)}
	if shape := helperShapeForCall(expr, c.helperShapes); shape != nil {
		local.kind = "object"
		local.object = shape
	}
	c.locals[name] = local
	if keyword == "const" && !forClause && isGopressConstLiteral(expr) {
		return []string{fmt.Sprintf("const %s = %s", name, compiled)}, true
	}
	return []string{fmt.Sprintf("%s := %s", name, compiled)}, true
}

func (c *gopressBodyContext) inferLocalKind(expr string) string {
	return inferGopressGoType(expr, c.locals)
}

func (c *gopressBodyContext) compileAssignment(stmt string) ([]string, bool) {
	stmt = strings.TrimSpace(stmt)
	if isPostIncDec(stmt) {
		return []string{stmt}, true
	}
	for _, op := range []string{"+=", "-=", "*=", "/=", "="} {
		idx := strings.Index(stmt, op)
		if idx <= 0 {
			continue
		}
		if op == "=" && (strings.Contains(stmt, "==") || strings.Contains(stmt, "!=") || strings.Contains(stmt, ">=") || strings.Contains(stmt, "<=")) {
			continue
		}
		left := strings.TrimSpace(stmt[:idx])
		right := strings.TrimSpace(stmt[idx+len(op):])
		if !isAssignableTarget(left) || right == "" {
			return nil, false
		}
		if c.builders[left] && op == "+=" {
			return c.compileBuilderAppend(left, right), true
		}
		if _, ok := c.byteBuffers[left]; ok && op == "+=" {
			return c.compileByteAppend(left, right), true
		}
		return []string{fmt.Sprintf("%s %s %s", left, op, c.compileExpr(right))}, true
	}
	return nil, false
}

func unsupportedGopressStatement(path string, stmt string) diagnostics.Diagnostic {
	stmt = strings.TrimSpace(stmt)
	if len(stmt) > 80 {
		stmt = stmt[:77] + "..."
	}
	return diagnostics.Errorf(path, diagnostics.Position{Line: 1, Column: 1}, "GODE_SUBSET_001", "unsupported gopress statement %q", stmt)
}

func (c *gopressBodyContext) compileBuilderAppend(name string, expr string) []string {
	parts := splitTopLevelPlus(expr)
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s.WriteString(%s)", name, c.compileStringPart(part)))
	}
	return lines
}

func (c *gopressBodyContext) compileStringPart(expr string) string {
	expr = strings.TrimSpace(expr)
	if isStringLiteral(expr) {
		return expr
	}
	if c.builders[expr] {
		return expr + ".String()"
	}
	if local, ok := c.locals[expr]; ok {
		switch local.kind {
		case "string":
			return expr
		case "int":
			c.emit.addImport("strconv")
			return "strconv.Itoa(" + c.compileExpr(expr) + ")"
		case "bool":
			c.emit.addImport("strconv")
			return "strconv.FormatBool(" + c.compileExpr(expr) + ")"
		}
	}
	if strings.HasPrefix(expr, "req.") {
		return c.compileExpr(expr)
	}
	if isIntegerLiteral(expr) {
		c.emit.addImport("strconv")
		return "strconv.Itoa(" + expr + ")"
	}
	if expr == "true" || expr == "false" {
		c.emit.addImport("strconv")
		return "strconv.FormatBool(" + expr + ")"
	}
	c.emit.addImport("strconv")
	return "strconv.FormatFloat(" + c.compileExpr(expr) + ", 'f', -1, 64)"
}

func (c *gopressBodyContext) compileByteAppend(name string, expr string) []string {
	parts := splitTopLevelPlus(expr)
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lines = append(lines, c.compileByteAppendPart(name, part))
	}
	return lines
}

func (c *gopressBodyContext) compileByteAppendPart(name string, expr string) string {
	expr = strings.TrimSpace(expr)
	if isStringLiteral(expr) {
		return fmt.Sprintf("%s = append(%s, %s...)", name, name, expr)
	}
	if c.builders[expr] {
		return fmt.Sprintf("%s = append(%s, %s.String()...)", name, name, expr)
	}
	if _, ok := c.byteBuffers[expr]; ok {
		return fmt.Sprintf("%s = append(%s, %s...)", name, name, expr)
	}
	if local, ok := c.locals[expr]; ok {
		switch local.kind {
		case "string":
			return fmt.Sprintf("%s = append(%s, %s...)", name, name, expr)
		case "int":
			c.emit.addImport("strconv")
			return fmt.Sprintf("%s = strconv.AppendInt(%s, int64(%s), 10)", name, name, c.compileExpr(expr))
		case "float64":
			c.emit.addImport("strconv")
			return fmt.Sprintf("%s = strconv.AppendFloat(%s, %s, 'f', -1, 64)", name, name, c.compileExpr(expr))
		case "bool":
			c.emit.addImport("strconv")
			return fmt.Sprintf("%s = strconv.AppendBool(%s, %s)", name, name, c.compileExpr(expr))
		}
	}
	if strings.HasPrefix(expr, "req.") {
		return fmt.Sprintf("%s = append(%s, %s...)", name, name, c.compileExpr(expr))
	}
	if isIntegerLiteral(expr) {
		c.emit.addImport("strconv")
		return fmt.Sprintf("%s = strconv.AppendInt(%s, %s, 10)", name, name, expr)
	}
	if isNumberLiteral(expr) {
		c.emit.addImport("strconv")
		return fmt.Sprintf("%s = strconv.AppendFloat(%s, %s, 'f', -1, 64)", name, name, expr)
	}
	if expr == "true" || expr == "false" {
		c.emit.addImport("strconv")
		return fmt.Sprintf("%s = strconv.AppendBool(%s, %s)", name, name, expr)
	}
	c.emit.addImport("strconv")
	return fmt.Sprintf("%s = strconv.AppendFloat(%s, %s, 'f', -1, 64)", name, name, c.compileExpr(expr))
}

func (c *gopressBodyContext) compileExpr(expr string) string {
	expr = strings.TrimSpace(strings.TrimSuffix(expr, ";"))
	expr = strings.ReplaceAll(expr, "===", "==")
	expr = strings.ReplaceAll(expr, "!==", "!=")
	if expr == "performance.now()" {
		c.emit.addImport("time")
		return "time.Now()"
	}
	if start, ok := parsePerformanceNowDelta(expr); ok {
		c.emit.addImport("time")
		return fmt.Sprintf("float64(time.Since(%s).Microseconds()) / 1000.0", start)
	}
	if strings.HasPrefix(expr, "next(") {
		return expr
	}
	if strings.HasPrefix(expr, "res.") {
		return c.compileResponseChain(expr)
	}
	if strings.HasPrefix(expr, "{") {
		return c.compileObjectLiteral(expr)
	}
	if strings.HasPrefix(expr, "req.") {
		return c.replaceRequestMembers(expr)
	}
	return c.replaceBuilderRefs(c.replaceRequestMembers(expr))
}

func (c *gopressBodyContext) compileResponseChain(expr string) string {
	if fast, ok := c.compileFastResponseChain(expr); ok {
		return fast
	}
	replacements := map[string]string{
		".status(":     ".Status(",
		".send(":       ".Send(",
		".json(":       ".JSON(",
		".type(":       ".Type(",
		".set(":        ".Set(",
		".cookie(":     ".Cookie(",
		".redirect(":   ".Redirect(",
		".sendStatus(": ".SendStatus(",
		".sendFile(":   ".SendFile(",
	}
	out := expr
	for from, to := range replacements {
		out = strings.ReplaceAll(out, from, to)
	}
	out = c.replaceRequestMembers(out)
	out = c.replaceObjectArgs(out)
	out = c.replaceBuilderRefs(out)
	return out
}

func (c *gopressBodyContext) compileRawResponseStatement(expr string) ([]string, bool) {
	expr = strings.TrimSpace(expr)
	if status, arg, ok := parseResponseJSONCall(expr); ok {
		if lines, ok := c.compileJSONByteResponse(status, arg); ok {
			return lines, true
		}
		return []string{"return gopress.WriteJSON(w, " + status + ", " + c.compileExpr(arg) + ")"}, true
	}
	return nil, false
}

func parseResponseJSONCall(expr string) (string, string, bool) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "res.status(") {
		openStatus := strings.IndexByte(expr, '(')
		closeStatus := findMatching(expr, openStatus, '(', ')')
		if closeStatus < 0 {
			return "", "", false
		}
		rest := strings.TrimSpace(expr[closeStatus+1:])
		if !strings.HasPrefix(rest, ".json(") {
			return "", "", false
		}
		openJSON := closeStatus + strings.Index(expr[closeStatus+1:], "(") + 1
		closeJSON := findMatching(expr, openJSON, '(', ')')
		if closeJSON != len(expr)-1 {
			return "", "", false
		}
		return strings.TrimSpace(expr[openStatus+1 : closeStatus]), strings.TrimSpace(expr[openJSON+1 : closeJSON]), true
	}
	if strings.HasPrefix(expr, "res.json(") {
		open := strings.IndexByte(expr, '(')
		close := findMatching(expr, open, '(', ')')
		if close != len(expr)-1 {
			return "", "", false
		}
		return "200", strings.TrimSpace(expr[open+1 : close]), true
	}
	return "", "", false
}

func (c *gopressBodyContext) compileJSONByteResponse(status string, expr string) ([]string, bool) {
	fields, ok := c.flattenJSONFields(expr)
	if !ok {
		return nil, false
	}
	name := c.nextJSONVar()
	lines := c.compileJSONByteFields(name, fields)
	lines = append(lines, "return gopress.WriteJSONBytes(w, "+status+", "+name+")")
	return lines, true
}

func (c *gopressBodyContext) nextJSONVar() string {
	if c.jsonVarCount == 0 {
		c.jsonVarCount++
		return "godeJSON"
	}
	name := fmt.Sprintf("godeJSON%d", c.jsonVarCount)
	c.jsonVarCount++
	return name
}

func (c *gopressBodyContext) flattenJSONFields(expr string) ([]gopressObjectField, bool) {
	parts, ok := parseGopressObjectFields(expr)
	if !ok || len(parts) == 0 {
		return nil, false
	}
	fields := make([]gopressObjectField, 0, len(parts))
	for _, part := range parts {
		if part.Dynamic {
			local, ok := c.locals[part.Expr]
			if !ok || local.object == nil {
				return nil, false
			}
			for _, field := range local.object.Fields {
				fields = append(fields, gopressObjectField{
					Key:    field.Key,
					GoName: field.GoName,
					GoType: field.GoType,
					Expr:   part.Expr + "." + field.GoName,
				})
			}
			continue
		}
		goType := inferGopressGoType(part.Expr, c.locals)
		if goType == "" {
			goType = c.selectorGoType(part.Expr)
		}
		if goType == "" && isStringLiteral(part.Expr) {
			goType = "string"
		}
		if goType == "" {
			return nil, false
		}
		fields = append(fields, gopressObjectField{Key: part.Key, GoType: goType, Expr: part.Expr})
	}
	return fields, true
}

func (c *gopressBodyContext) compileJSONByteFields(name string, fields []gopressObjectField) []string {
	lines := []string{fmt.Sprintf("%s := make([]byte, 0, %d)", name, estimateJSONFieldsCapacity(fields))}
	literal := "{"
	flushLiteral := func() {
		if literal == "" {
			return
		}
		lines = append(lines, fmt.Sprintf("%s = append(%s, %s...)", name, name, strconv.Quote(literal)))
		literal = ""
	}
	for idx, field := range fields {
		if idx > 0 {
			literal += ","
		}
		keyJSON, err := json.Marshal(field.Key)
		if err != nil {
			keyJSON = []byte(strconv.Quote(field.Key))
		}
		literal += string(keyJSON) + ":"
		if isStringLiteral(field.Expr) {
			if value, err := strconv.Unquote(field.Expr); err == nil {
				if data, err := json.Marshal(value); err == nil {
					literal += string(data)
					continue
				}
			}
		}
		flushLiteral()
		lines = append(lines, c.compileJSONByteValueAppend(name, field.Expr, field.GoType))
	}
	literal += "}"
	flushLiteral()
	return lines
}

func estimateJSONFieldsCapacity(fields []gopressObjectField) int {
	total := 2
	for _, field := range fields {
		total += len(field.Key) + 4 + 24
		if isStringLiteral(field.Expr) {
			if value, err := strconv.Unquote(field.Expr); err == nil {
				total += len(value)
			}
		}
	}
	if total < 32 {
		return 32
	}
	return total
}

func (c *gopressBodyContext) compileJSONByteValueAppend(name string, expr string, goType string) string {
	expr = strings.TrimSpace(expr)
	compiled := c.compileExpr(expr)
	if goType == "" {
		goType = inferGopressGoType(expr, c.locals)
	}
	if goType == "" {
		goType = c.selectorGoType(expr)
	}
	switch goType {
	case "string":
		c.emit.addImport("strconv")
		return fmt.Sprintf("%s = strconv.AppendQuote(%s, %s)", name, name, compiled)
	case "int":
		c.emit.addImport("strconv")
		return fmt.Sprintf("%s = strconv.AppendInt(%s, int64(%s), 10)", name, name, compiled)
	case "float64":
		c.emit.addImport("strconv")
		return fmt.Sprintf("%s = strconv.AppendFloat(%s, %s, 'f', -1, 64)", name, name, compiled)
	case "bool":
		c.emit.addImport("strconv")
		return fmt.Sprintf("%s = strconv.AppendBool(%s, %s)", name, name, compiled)
	default:
		return fmt.Sprintf("%s = append(%s, \"null\"...)", name, name)
	}
}

func (c *gopressBodyContext) selectorGoType(expr string) string {
	left, right, ok := strings.Cut(strings.TrimSpace(expr), ".")
	if !ok || left == "" || right == "" || strings.Contains(right, ".") {
		return ""
	}
	local, ok := c.locals[left]
	if !ok || local.object == nil {
		return ""
	}
	for _, field := range local.object.Fields {
		if field.GoName == right {
			return field.GoType
		}
	}
	return ""
}

func (c *gopressBodyContext) compileReturnObjectLiteral(expr string) (string, bool) {
	fields, ok := parseGopressObjectFields(expr)
	if !ok || len(fields) != len(c.returnObject.Fields) {
		return "", false
	}
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		if field.Dynamic {
			return "", false
		}
		values[field.Key] = field.Expr
	}
	parts := make([]string, 0, len(c.returnObject.Fields))
	for _, field := range c.returnObject.Fields {
		expr, ok := values[field.Key]
		if !ok {
			return "", false
		}
		parts = append(parts, fmt.Sprintf("%s: %s", field.GoName, c.compileExpr(expr)))
	}
	return c.returnObject.StructName + "{" + strings.Join(parts, ", ") + "}", true
}

func (c *gopressBodyContext) compileFastResponseChain(expr string) (string, bool) {
	expr = strings.TrimSpace(expr)
	if name, ok := parseJSONTypeSend(expr); ok {
		if _, ok := c.byteBuffers[name]; ok {
			return "res.JSONBytes(" + name + ")", true
		}
		if c.builders[name] {
			return "res.JSONString(" + name + ".String())", true
		}
		if local, ok := c.locals[name]; ok && local.kind == "string" {
			return "res.JSONString(" + name + ")", true
		}
	}
	if strings.HasPrefix(expr, "res.status(") {
		openStatus := strings.IndexByte(expr, '(')
		closeStatus := findMatching(expr, openStatus, '(', ')')
		if closeStatus > openStatus && strings.HasPrefix(strings.TrimSpace(expr[closeStatus+1:]), ".json(") {
			status := strings.TrimSpace(expr[openStatus+1 : closeStatus])
			jsonOpen := closeStatus + strings.Index(expr[closeStatus+1:], "(") + 1
			jsonClose := findMatching(expr, jsonOpen, '(', ')')
			if jsonClose > jsonOpen {
				if raw, ok := c.compileJSONLiteral(expr[jsonOpen+1 : jsonClose]); ok {
					return fmt.Sprintf("res.StatusJSON(%s, %s)", status, raw), true
				}
			}
		}
	}
	if strings.HasPrefix(expr, "res.json(") {
		open := strings.IndexByte(expr, '(')
		close := findMatching(expr, open, '(', ')')
		if close > open {
			if raw, ok := c.compileJSONLiteral(expr[open+1 : close]); ok {
				return "res.JSONString(" + raw + ")", true
			}
		}
	}
	return "", false
}

func (c *gopressBodyContext) compileJSONLiteral(expr string) (string, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "{") {
		return "", false
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(expr, "{"), "}"))
	if strings.Contains(body, "...") {
		return "", false
	}
	if body == "" {
		return strconv.Quote("{}"), true
	}
	parts := splitTopLevelArgs(body)
	var fragments []string
	literal := "{"
	flushLiteral := func() {
		if literal == "" {
			return
		}
		fragments = append(fragments, strconv.Quote(literal))
		literal = ""
	}
	for idx, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, "...") {
			return "", false
		}
		key := ""
		value := ""
		if colon := findTopLevelColon(part); colon >= 0 {
			key = strings.Trim(strings.TrimSpace(part[:colon]), `"'`)
			value = strings.TrimSpace(part[colon+1:])
		} else {
			key = strings.Trim(strings.TrimSpace(part), `"'`)
			value = part
		}
		if key == "" || value == "" {
			return "", false
		}
		if idx > 0 {
			literal += ","
		}
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return "", false
		}
		literal += string(keyJSON) + ":"
		valueExpr, ok := c.compileJSONValue(value)
		if !ok {
			return "", false
		}
		flushLiteral()
		fragments = append(fragments, valueExpr)
	}
	literal += "}"
	flushLiteral()
	return strings.Join(fragments, " + "), true
}

func (c *gopressBodyContext) compileJSONValue(expr string) (string, bool) {
	expr = strings.TrimSpace(expr)
	switch {
	case isStringLiteral(expr):
		value, err := strconv.Unquote(expr)
		if err != nil {
			return "", false
		}
		data, err := json.Marshal(value)
		if err != nil {
			return "", false
		}
		return strconv.Quote(string(data)), true
	case expr == "true" || expr == "false" || expr == "null":
		return expr, true
	case isIntegerLiteral(expr) || isNumberLiteral(expr):
		return expr, true
	case strings.HasPrefix(expr, "req."):
		c.emit.addImport("strconv")
		return "strconv.Quote(" + c.compileExpr(expr) + ")", true
	}
	compiled := c.compileExpr(expr)
	if local, ok := c.locals[expr]; ok {
		switch local.kind {
		case "string":
			c.emit.addImport("strconv")
			return "strconv.Quote(" + compiled + ")", true
		case "int":
			c.emit.addImport("strconv")
			return "strconv.Itoa(" + compiled + ")", true
		case "float64":
			c.emit.addImport("strconv")
			return "strconv.FormatFloat(" + compiled + ", 'f', -1, 64)", true
		case "bool":
			c.emit.addImport("strconv")
			return "strconv.FormatBool(" + compiled + ")", true
		}
	}
	return "", false
}

func (c *gopressBodyContext) replaceObjectArgs(expr string) string {
	for {
		idx := strings.Index(expr, "({")
		if idx < 0 {
			return expr
		}
		open := idx + 1
		close := findMatching(expr, open, '{', '}')
		if close < 0 {
			return expr
		}
		expr = expr[:open] + c.compileObjectLiteral(expr[open:close+1]) + expr[close+1:]
	}
}

func (c *gopressBodyContext) compileObjectLiteral(expr string) string {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(expr), "{"), "}"))
	if body == "" {
		return "map[string]any{}"
	}
	parts := splitTopLevelArgs(body)
	var maps []string
	var entries []string
	flushEntries := func() {
		if len(entries) == 0 {
			return
		}
		maps = append(maps, "map[string]any{"+strings.Join(entries, ", ")+"}")
		entries = nil
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "...") {
			flushEntries()
			maps = append(maps, c.compileExpr(strings.TrimSpace(strings.TrimPrefix(part, "..."))))
			continue
		}
		if idx := findTopLevelColon(part); idx >= 0 {
			key := strings.Trim(strings.TrimSpace(part[:idx]), `"'`)
			value := strings.TrimSpace(part[idx+1:])
			entries = append(entries, fmt.Sprintf("%q: %s", key, c.compileExpr(value)))
			continue
		}
		key := strings.Trim(strings.TrimSpace(part), `"'`)
		if key == "" {
			continue
		}
		entries = append(entries, fmt.Sprintf("%q: %s", key, c.compileExpr(part)))
	}
	flushEntries()
	if len(maps) == 0 {
		return "map[string]any{}"
	}
	if len(maps) == 1 {
		return maps[0]
	}
	c.emit.needsMergeJSON = true
	return "godeMergeJSON(" + strings.Join(maps, ", ") + ")"
}

func (c *gopressBodyContext) replaceBuilderRefs(expr string) string {
	for name := range c.builders {
		expr = replaceIdentifier(expr, name, name+".String()")
	}
	return expr
}

func compileRequestMember(expr string) string {
	return replaceRequestMembers(expr, false)
}

func (c *gopressBodyContext) replaceRequestMembers(expr string) string {
	return replaceRequestMembers(expr, c.directParams)
}

func replaceRequestMembers(expr string, directParams bool) string {
	expr = gopressRequestMemberRE.ReplaceAllStringFunc(expr, func(match string) string {
		parts := strings.Split(match, ".")
		key := parts[2]
		switch parts[1] {
		case "params":
			if directParams {
				return fmt.Sprintf("req.Param(%q)", key)
			}
			return fmt.Sprintf("req.Params[%q]", key)
		case "query":
			return fmt.Sprintf("req.Query[%q]", key)
		case "body":
			return fmt.Sprintf("req.Body[%q]", key)
		case "headers":
			return fmt.Sprintf("req.Headers[%q]", strings.ToLower(key))
		case "cookies":
			return fmt.Sprintf("req.Cookies[%q]", key)
		default:
			return match
		}
	})
	expr = strings.ReplaceAll(expr, "req.method", "req.Method")
	expr = strings.ReplaceAll(expr, "req.path", "req.Path")
	return expr
}

func discoverStringBuilderVars(body string) map[string]bool {
	out := map[string]bool{}
	for _, match := range gopressBuilderVarRE.FindAllStringSubmatch(body, -1) {
		name := match[1]
		if hasAddAssign(body, name) {
			out[name] = true
		}
	}
	return out
}

func discoverJSONByteBufferVars(body string, builders map[string]bool) map[string]int {
	out := map[string]int{}
	for name := range builders {
		if !isJSONSendVar(body, name) {
			continue
		}
		capacity := estimateJSONByteBufferCapacity(body, name)
		if capacity < 64 {
			capacity = 64
		}
		out[name] = capacity
	}
	return out
}

func isJSONSendVar(body string, name string) bool {
	compact := stripWhitespaceOutsideStrings(body)
	for _, quote := range []string{`"`, `'`} {
		if strings.Contains(compact, ".type("+quote+"application/json"+quote+").send("+name+")") {
			return true
		}
	}
	return false
}

func stripWhitespaceOutsideStrings(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	inString := rune(0)
	escaped := false
	for _, r := range src {
		if inString != 0 {
			out.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == inString {
				inString = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			inString = r
			out.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func estimateJSONByteBufferCapacity(body string, name string) int {
	total := 0
	for pos := 0; pos < len(body); {
		pos = skipGopressSeparators(body, pos)
		if pos >= len(body) {
			break
		}
		if hasKeywordAt(body, pos, "for") {
			loopEnd, capacity, ok := estimateLoopByteBufferCapacity(body, pos, name)
			if ok {
				total += capacity
				pos = loopEnd
				continue
			}
		}
		stmt, next := readGopressStatement(body, pos)
		total += estimateAppendExprCapacity(stmt, name)
		pos = next
	}
	return total
}

func estimateLoopByteBufferCapacity(src string, pos int, name string) (int, int, bool) {
	openHeader := skipSpace(src, pos+len("for"))
	if openHeader >= len(src) || src[openHeader] != '(' {
		return pos, 0, false
	}
	closeHeader := findMatching(src, openHeader, '(', ')')
	if closeHeader < 0 {
		return pos, 0, false
	}
	openBody := skipSpace(src, closeHeader+1)
	if openBody >= len(src) || src[openBody] != '{' {
		return pos, 0, false
	}
	closeBody := findMatching(src, openBody, '{', '}')
	if closeBody < 0 {
		return pos, 0, false
	}
	iterations := estimateForIterations(src[openHeader+1 : closeHeader])
	if iterations <= 0 {
		iterations = 1
	}
	bodyCapacity := estimateJSONByteBufferCapacity(src[openBody+1:closeBody], name)
	return closeBody + 1, bodyCapacity * iterations, true
}

func estimateForIterations(header string) int {
	parts := splitTopLevelSemicolons(header)
	if len(parts) != 3 {
		return 0
	}
	condition := strings.TrimSpace(parts[1])
	for _, op := range []string{"<=", "<"} {
		idx := strings.Index(condition, op)
		if idx < 0 {
			continue
		}
		boundText := strings.TrimSpace(condition[idx+len(op):])
		bound, err := strconv.Atoi(boundText)
		if err != nil {
			return 0
		}
		if op == "<=" {
			return bound + 1
		}
		return bound
	}
	return 0
}

func estimateAppendExprCapacity(stmt string, name string) int {
	stmt = strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
	prefix := name + " +="
	if !strings.HasPrefix(stmt, prefix) {
		return 0
	}
	expr := strings.TrimSpace(strings.TrimPrefix(stmt, prefix))
	total := 0
	for _, part := range splitTopLevelPlus(expr) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if isStringLiteral(part) {
			if value, err := strconv.Unquote(part); err == nil {
				total += len(value)
				continue
			}
		}
		total += 24
	}
	return total
}

func zeroReturnStatement(goType string) string {
	switch goType {
	case "string":
		return `return ""`
	case "bool":
		return "return false"
	case "float64":
		return "return 0"
	case "map[string]any", "any":
		return "return nil"
	default:
		return "return nil"
	}
}

func skipGopressSeparators(src string, pos int) int {
	for pos < len(src) {
		if unicode.IsSpace(rune(src[pos])) || src[pos] == ';' {
			pos++
			continue
		}
		break
	}
	return pos
}

func readGopressStatement(src string, pos int) (string, int) {
	depth := 0
	inString := byte(0)
	escaped := false
	for i := pos; i < len(src); i++ {
		ch := src[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inString = ch
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case ';', '\n':
			if depth == 0 {
				if ch == '\n' && isGopressChainContinuation(src, i+1) {
					continue
				}
				return src[pos:i], i + 1
			}
		}
	}
	return src[pos:], len(src)
}

func isGopressChainContinuation(src string, pos int) bool {
	pos = skipSpace(src, pos)
	return pos < len(src) && src[pos] == '.'
}

func readRestLine(src string, pos int) string {
	end := strings.IndexByte(src[pos:], '\n')
	if end < 0 {
		return src[pos:]
	}
	return src[pos : pos+end]
}

func hasKeywordAt(src string, pos int, keyword string) bool {
	if pos < 0 || pos+len(keyword) > len(src) || !strings.HasPrefix(src[pos:], keyword) {
		return false
	}
	if pos > 0 && isIdentPartRune(rune(src[pos-1])) {
		return false
	}
	after := pos + len(keyword)
	return after >= len(src) || !isIdentPartRune(rune(src[after]))
}

func splitTopLevelSemicolons(src string) []string {
	var out []string
	var current strings.Builder
	depth := 0
	inString := rune(0)
	escaped := false
	for _, r := range src {
		if inString != 0 {
			current.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == inString {
				inString = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			inString = r
			current.WriteRune(r)
		case '(', '{', '[':
			depth++
			current.WriteRune(r)
		case ')', '}', ']':
			depth--
			current.WriteRune(r)
		case ';':
			if depth == 0 {
				out = append(out, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	out = append(out, strings.TrimSpace(current.String()))
	return out
}

func splitTopLevelPlus(src string) []string {
	var out []string
	var current strings.Builder
	depth := 0
	inString := rune(0)
	escaped := false
	for _, r := range src {
		if inString != 0 {
			current.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == inString {
				inString = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			inString = r
			current.WriteRune(r)
		case '(', '{', '[':
			depth++
			current.WriteRune(r)
		case ')', '}', ']':
			depth--
			current.WriteRune(r)
		case '+':
			if depth == 0 {
				out = append(out, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		out = append(out, strings.TrimSpace(current.String()))
	}
	return out
}

func isGopressConstLiteral(expr string) bool {
	expr = strings.TrimSpace(expr)
	return isIntegerLiteral(expr) ||
		isDecimalLiteral(expr) ||
		isStringLiteral(expr) ||
		expr == "true" ||
		expr == "false"
}

func isIntegerLiteral(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	if expr[0] == '-' {
		expr = expr[1:]
	}
	if expr == "" {
		return false
	}
	for _, r := range expr {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isNumberLiteral(expr string) bool {
	expr = strings.TrimSpace(expr)
	return isIntegerLiteral(expr) || isDecimalLiteral(expr)
}

func paramAppearsInForCondition(body string, name string) bool {
	for pos := 0; pos < len(body); {
		idx := strings.Index(body[pos:], "for")
		if idx < 0 {
			return false
		}
		idx += pos
		if !hasKeywordAt(body, idx, "for") {
			pos = idx + len("for")
			continue
		}
		open := skipSpace(body, idx+len("for"))
		if open >= len(body) || body[open] != '(' {
			pos = idx + len("for")
			continue
		}
		close := findMatching(body, open, '(', ')')
		if close < 0 {
			return false
		}
		parts := splitTopLevelSemicolons(body[open+1 : close])
		if len(parts) == 3 && containsIdentifier(parts[1], name) {
			return true
		}
		pos = close + 1
	}
	return false
}

func paramHasFloatyUse(body string, name string) bool {
	if strings.Contains(body, "Math.") || containsDecimalLiteral(body) {
		return true
	}
	for idx := 0; idx < len(body); idx++ {
		if body[idx] != '/' {
			continue
		}
		leftStart := idx - 1
		for leftStart >= 0 && unicode.IsSpace(rune(body[leftStart])) {
			leftStart--
		}
		leftEnd := leftStart + 1
		for leftStart >= 0 && isIdentPartRune(rune(body[leftStart])) {
			leftStart--
		}
		rightStart := idx + 1
		for rightStart < len(body) && unicode.IsSpace(rune(body[rightStart])) {
			rightStart++
		}
		rightEnd := rightStart
		for rightEnd < len(body) && isIdentPartRune(rune(body[rightEnd])) {
			rightEnd++
		}
		if body[leftStart+1:leftEnd] == name || body[rightStart:rightEnd] == name {
			return true
		}
	}
	return false
}

func containsDecimalLiteral(src string) bool {
	for i := 0; i < len(src); i++ {
		if src[i] != '.' || i == 0 || i+1 >= len(src) {
			continue
		}
		if src[i-1] >= '0' && src[i-1] <= '9' && src[i+1] >= '0' && src[i+1] <= '9' {
			return true
		}
	}
	return false
}

func isSimpleCallStatement(stmt string) bool {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" || !isIdentStartRune(rune(stmt[0])) {
		return false
	}
	end := 1
	for end < len(stmt) && isIdentPartRune(rune(stmt[end])) {
		end++
	}
	open := skipSpace(stmt, end)
	if open >= len(stmt) || stmt[open] != '(' {
		return false
	}
	close := findMatching(stmt, open, '(', ')')
	return close == len(stmt)-1
}

func isPostIncDec(stmt string) bool {
	stmt = strings.TrimSpace(stmt)
	if strings.HasSuffix(stmt, "++") || strings.HasSuffix(stmt, "--") {
		return isIdentifier(strings.TrimSpace(stmt[:len(stmt)-2]))
	}
	return false
}

func isAssignableTarget(left string) bool {
	left = strings.TrimSpace(left)
	if isIdentifier(left) {
		return true
	}
	open := strings.IndexByte(left, '[')
	if open <= 0 || !strings.HasSuffix(left, "]") {
		return false
	}
	return isIdentifier(strings.TrimSpace(left[:open])) && strings.TrimSpace(left[open+1:len(left)-1]) != ""
}

func parsePerformanceNowDelta(expr string) (string, bool) {
	expr = strings.TrimSpace(expr)
	const prefix = "performance.now()"
	if !strings.HasPrefix(expr, prefix) {
		return "", false
	}
	pos := skipSpace(expr, len(prefix))
	if pos >= len(expr) || expr[pos] != '-' {
		return "", false
	}
	pos = skipSpace(expr, pos+1)
	name := strings.TrimSpace(expr[pos:])
	return name, isIdentifier(name)
}

func parseJSONTypeSend(expr string) (string, bool) {
	expr = strings.TrimSpace(expr)
	const prefix = "res.type"
	if !strings.HasPrefix(expr, prefix) {
		return "", false
	}
	openType := skipSpace(expr, len(prefix))
	if openType >= len(expr) || expr[openType] != '(' {
		return "", false
	}
	closeType := findMatching(expr, openType, '(', ')')
	if closeType < 0 {
		return "", false
	}
	contentType, err := strconv.Unquote(strings.TrimSpace(expr[openType+1 : closeType]))
	if err != nil || contentType != "application/json" {
		return "", false
	}
	rest := strings.TrimSpace(expr[closeType+1:])
	const sendPrefix = ".send"
	if !strings.HasPrefix(rest, sendPrefix) {
		return "", false
	}
	openSend := skipSpace(rest, len(sendPrefix))
	if openSend >= len(rest) || rest[openSend] != '(' {
		return "", false
	}
	closeSend := findMatching(rest, openSend, '(', ')')
	if closeSend != len(rest)-1 {
		return "", false
	}
	name := strings.TrimSpace(rest[openSend+1 : closeSend])
	return name, isIdentifier(name)
}

func replaceIdentifier(src string, name string, replacement string) string {
	var out strings.Builder
	for pos := 0; pos < len(src); {
		idx := strings.Index(src[pos:], name)
		if idx < 0 {
			out.WriteString(src[pos:])
			break
		}
		idx += pos
		end := idx + len(name)
		if (idx == 0 || !isIdentPartRune(rune(src[idx-1]))) && (end >= len(src) || !isIdentPartRune(rune(src[end]))) {
			out.WriteString(src[pos:idx])
			out.WriteString(replacement)
			pos = end
			continue
		}
		out.WriteString(src[pos:end])
		pos = end
	}
	return out.String()
}

func hasAddAssign(src string, name string) bool {
	for pos := 0; pos < len(src); {
		idx := strings.Index(src[pos:], name)
		if idx < 0 {
			return false
		}
		idx += pos
		end := idx + len(name)
		if (idx == 0 || !isIdentPartRune(rune(src[idx-1]))) && (end >= len(src) || !isIdentPartRune(rune(src[end]))) {
			next := skipSpace(src, end)
			if strings.HasPrefix(src[next:], "+=") {
				return true
			}
		}
		pos = end
	}
	return false
}

func containsIdentifier(src string, name string) bool {
	for pos := 0; pos < len(src); {
		idx := strings.Index(src[pos:], name)
		if idx < 0 {
			return false
		}
		idx += pos
		end := idx + len(name)
		if (idx == 0 || !isIdentPartRune(rune(src[idx-1]))) && (end >= len(src) || !isIdentPartRune(rune(src[end]))) {
			return true
		}
		pos = end
	}
	return false
}

func isIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !isIdentStartRune(rune(value[0])) {
		return false
	}
	for _, r := range value[1:] {
		if !isIdentPartRune(r) {
			return false
		}
	}
	return true
}

func isDecimalLiteral(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	if expr[0] == '-' {
		expr = expr[1:]
	}
	if expr == "" {
		return false
	}
	dot := false
	digitsBefore := 0
	digitsAfter := 0
	for _, r := range expr {
		switch {
		case r == '.':
			if dot {
				return false
			}
			dot = true
		case r >= '0' && r <= '9':
			if dot {
				digitsAfter++
			} else {
				digitsBefore++
			}
		default:
			return false
		}
	}
	return dot && digitsBefore > 0 && digitsAfter > 0
}

func findTopLevelColon(src string) int {
	depth := 0
	inString := byte(0)
	escaped := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inString = ch
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case ':':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isSupportedGopressMethod(method string) bool {
	switch strings.ToLower(method) {
	case "use", "all", "get", "post", "put", "patch", "delete", "options", "head",
		"route.all", "route.get", "route.post", "route.put", "route.patch", "route.delete", "route.options", "route.head":
		return true
	default:
		return false
	}
}

func isRouteMethod(method string) bool {
	switch strings.ToLower(method) {
	case "all", "get", "post", "put", "patch", "delete", "options", "head":
		return true
	default:
		return false
	}
}

func isErrorArrow(src string) bool {
	arrow := strings.Index(src, "=>")
	if arrow < 0 {
		return false
	}
	params := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(src[:arrow]), "async"))
	if strings.HasPrefix(params, "(") && strings.HasSuffix(params, ")") {
		params = strings.TrimSpace(params[1 : len(params)-1])
	}
	return isErrorParamList(params)
}

func isErrorParamList(params string) bool {
	parts := splitTopLevelArgs(params)
	if len(parts) < 4 {
		return false
	}
	first := strings.TrimSpace(parts[0])
	name, _, _ := strings.Cut(first, ":")
	return strings.TrimSpace(name) == "err"
}

func splitTopLevelArgs(src string) []string {
	var out []string
	var current strings.Builder
	depth := 0
	inString := rune(0)
	escaped := false
	for _, r := range src {
		if inString != 0 {
			current.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == inString {
				inString = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			inString = r
			current.WriteRune(r)
		case '(', '{', '[':
			depth++
			current.WriteRune(r)
		case ')', '}', ']':
			depth--
			current.WriteRune(r)
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		out = append(out, strings.TrimSpace(current.String()))
	}
	return out
}

func findMatching(src string, open int, left byte, right byte) int {
	depth := 0
	inString := byte(0)
	escaped := false
	for i := open; i < len(src); i++ {
		ch := src[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		if ch == left {
			depth++
		}
		if ch == right {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func skipSpace(src string, pos int) int {
	for pos < len(src) && unicode.IsSpace(rune(src[pos])) {
		pos++
	}
	return pos
}

func inSpans(pos int, spans [][2]int) bool {
	for _, span := range spans {
		if pos >= span[0] && pos < span[1] {
			return true
		}
	}
	return false
}

func isStringLiteral(value string) bool {
	_, err := strconv.Unquote(value)
	return err == nil
}

func insideCall(value string) string {
	open := strings.Index(value, "(")
	close := strings.LastIndex(value, ")")
	if open < 0 || close < open {
		return ""
	}
	return value[open+1 : close]
}

func isIdentStartRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isIdentPartRune(r rune) bool {
	return isIdentStartRune(r) || unicode.IsDigit(r)
}
