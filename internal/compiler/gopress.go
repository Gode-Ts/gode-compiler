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
	packageName string
	path        string
	source      string
	appName     string
	vars        map[string]string
	helpers     []gopressFunction
	handlers    []gopressHandler
	calls       []gopressCall
	diags       diagnostics.List
}

type gopressHandler struct {
	Name    string
	GoName  string
	Body    string
	IsError bool
}

type gopressFunction struct {
	Name       string
	Params     []gopressParam
	Body       string
	ReturnType string
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
	app := &gopressApp{packageName: cfg.Package, path: path, source: src, vars: map[string]string{}}
	if app.packageName == "" {
		app.packageName = "main"
	}
	if !strings.Contains(src, "gopress") {
		app.diags = append(app.diags, diagnostics.Errorf(path, diagnostics.Position{Line: 1, Column: 1}, "GODE_BIND_002", "gopress app must import from \"gopress\""))
	}
	for _, match := range regexp.MustCompile(`const\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(gopress|Router)\s*\(\s*\)`).FindAllStringSubmatch(src, -1) {
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
	re := regexp.MustCompile(`export\s+(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)\s*:\s*Promise<[^>]+>\s*\{`)
	for _, loc := range re.FindAllStringSubmatchIndex(src, -1) {
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
	re := regexp.MustCompile(`(?:^|[;\n])\s*(?:export\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)\s*(?::\s*([A-Za-z_][A-Za-z0-9_<>\[\]\s|]*))?\s*\{`)
	for _, loc := range re.FindAllStringSubmatchIndex(src, -1) {
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
		functions = append(functions, gopressFunction{
			Name:       name,
			Params:     parseGopressParams(params),
			Body:       body,
			ReturnType: inferGopressReturnType(declaredReturn, body),
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
	if regexp.MustCompile(`\breturn\s*\{`).MatchString(body) {
		return "map[string]any"
	}
	if strings.Contains(body, "return ") {
		return "any"
	}
	return ""
}

func findFunctionSpans(src string) [][2]int {
	var spans [][2]int
	re := regexp.MustCompile(`(?:^|[;\n])\s*(?:export\s+)?(?:async\s+)?function\s+[A-Za-z_][A-Za-z0-9_]*\s*\([^)]*\)\s*(?::\s*[A-Za-z_][A-Za-z0-9_<>\[\]\s|]*)?\s*\{`)
	for _, loc := range re.FindAllStringIndex(src, -1) {
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
	w("%q", "github.com/Gode-Ts/gopress")
	if strings.Contains(a.source, "performance.now()") {
		w("%q", "time")
	}
	w(")")
	w("")
	if strings.Contains(a.source, "...") {
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
		params := make([]string, 0, len(helper.Params))
		for _, param := range helper.Params {
			params = append(params, fmt.Sprintf("%s %s", param.Name, param.GoType))
		}
		returnType := ""
		if helper.ReturnType != "" {
			returnType = " " + helper.ReturnType
		}
		w("func %s(%s)%s {", helper.Name, strings.Join(params, ", "), returnType)
		lines, bodyDiags := compileGopressBody(a.path, helper.Body)
		a.diags = append(a.diags, bodyDiags...)
		for _, line := range lines {
			w("%s", line)
		}
		if helper.ReturnType != "" {
			w("%s", zeroReturnStatement(helper.ReturnType))
		}
		w("}")
		w("")
	}
	for _, handler := range a.handlers {
		if handler.IsError {
			w("func %s(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {", handler.GoName)
		} else {
			w("func %s(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {", handler.GoName)
		}
		lines, bodyDiags := compileGopressBody(a.path, handler.Body)
		a.diags = append(a.diags, bodyDiags...)
		for _, line := range lines {
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
	for _, call := range a.calls {
		for _, line := range a.emitGopressCall(call) {
			w("%s", line)
		}
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

func (a *gopressApp) emitGopressCall(call gopressCall) []string {
	method := strings.ToLower(call.Method)
	switch method {
	case "use":
		return a.emitUseCall(call)
	case "all", "get", "post", "put", "patch", "delete", "options", "head":
		if len(call.Args) < 2 {
			return nil
		}
		args := []string{call.Args[0]}
		for _, arg := range call.Args[1:] {
			args = append(args, a.compileHandlerArg(arg))
		}
		return []string{fmt.Sprintf("%s.%s(%s)", call.Receiver, names.Exported(method), strings.Join(args, ", "))}
	case "route.all", "route.get", "route.post", "route.put", "route.patch", "route.delete", "route.options", "route.head":
		if len(call.Args) < 2 {
			return nil
		}
		routeMethod := strings.TrimPrefix(method, "route.")
		args := make([]string, 0, len(call.Args)-1)
		for _, arg := range call.Args[1:] {
			args = append(args, a.compileHandlerArg(arg))
		}
		return []string{fmt.Sprintf("%s.Route(%s).%s(%s)", call.Receiver, call.Args[0], names.Exported(routeMethod), strings.Join(args, ", "))}
	default:
		return nil
	}
}

func (a *gopressApp) emitUseCall(call gopressCall) []string {
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
			lines = append(lines, fmt.Sprintf("%s.UseError(%s)", call.Receiver, a.compileErrorHandlerArg(trimmed)))
			continue
		}
		useArgs := []string{}
		if prefix != "" {
			useArgs = append(useArgs, prefix)
		}
		useArgs = append(useArgs, a.compileUseArg(trimmed))
		lines = append(lines, fmt.Sprintf("%s.Use(%s)", call.Receiver, strings.Join(useArgs, ", ")))
	}
	return lines
}

func (a *gopressApp) compileUseArg(arg string) string {
	trimmed := strings.TrimSpace(arg)
	switch {
	case trimmed == "gopress.json()":
		return "gopress.JSON()"
	case strings.HasPrefix(trimmed, "gopress.static("):
		return "gopress.Static(" + insideCall(trimmed) + ")"
	case isStringLiteral(trimmed):
		return trimmed
	default:
		return a.compileHandlerArg(trimmed)
	}
}

func (a *gopressApp) compileHandlerArg(arg string) string {
	trimmed := strings.TrimSpace(arg)
	if strings.Contains(trimmed, "=>") {
		return a.compileInlineHandler(trimmed)
	}
	if _, ok := a.vars[trimmed]; ok {
		return trimmed
	}
	return names.Exported(trimmed)
}

func (a *gopressApp) compileErrorHandlerArg(arg string) string {
	trimmed := strings.TrimSpace(arg)
	if strings.Contains(trimmed, "=>") {
		return a.compileInlineErrorHandler(trimmed)
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

func (a *gopressApp) compileInlineHandler(src string) string {
	bodyStart := strings.Index(src, "{")
	bodyEnd := strings.LastIndex(src, "}")
	if bodyStart < 0 || bodyEnd < bodyStart {
		return "func(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error { return nil }"
	}
	lines, bodyDiags := compileGopressBody(a.path, src[bodyStart+1:bodyEnd])
	a.diags = append(a.diags, bodyDiags...)
	return fmt.Sprintf("func(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {\n%s\nreturn nil\n}", strings.Join(lines, "\n"))
}

func (a *gopressApp) compileInlineErrorHandler(src string) string {
	bodyStart := strings.Index(src, "{")
	bodyEnd := strings.LastIndex(src, "}")
	if bodyStart < 0 || bodyEnd < bodyStart {
		return "func(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error { return nil }"
	}
	lines, bodyDiags := compileGopressBody(a.path, src[bodyStart+1:bodyEnd])
	a.diags = append(a.diags, bodyDiags...)
	return fmt.Sprintf("func(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {\n%s\nreturn nil\n}", strings.Join(lines, "\n"))
}

func compileGopressBody(path string, body string) ([]string, diagnostics.List) {
	var out []string
	var diags diagnostics.List
	for pos := 0; pos < len(body); {
		pos = skipGopressSeparators(body, pos)
		if pos >= len(body) {
			break
		}
		switch {
		case hasKeywordAt(body, pos, "if"):
			lines, next, blockDiags := compileGopressIf(path, body, pos)
			out = append(out, lines...)
			diags = append(diags, blockDiags...)
			pos = next
		case hasKeywordAt(body, pos, "for"):
			lines, next, blockDiags := compileGopressFor(path, body, pos)
			out = append(out, lines...)
			diags = append(diags, blockDiags...)
			pos = next
		default:
			stmt, next := readGopressStatement(body, pos)
			lines, stmtDiags := compileGopressStatement(path, stmt)
			out = append(out, lines...)
			diags = append(diags, stmtDiags...)
			pos = next
		}
	}
	return out, diags
}

func compileGopressIf(path string, src string, pos int) ([]string, int, diagnostics.List) {
	openCond := skipSpace(src, pos+len("if"))
	if openCond >= len(src) || src[openCond] != '(' {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	closeCond := findMatching(src, openCond, '(', ')')
	if closeCond < 0 {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	openBody := skipSpace(src, closeCond+1)
	if openBody >= len(src) || src[openBody] != '{' {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	closeBody := findMatching(src, openBody, '{', '}')
	if closeBody < 0 {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	bodyLines, diags := compileGopressBody(path, src[openBody+1:closeBody])
	out := []string{fmt.Sprintf("if %s {", compileGopressExpr(src[openCond+1:closeCond]))}
	out = append(out, bodyLines...)
	next := closeBody + 1
	elsePos := skipGopressSeparators(src, next)
	if hasKeywordAt(src, elsePos, "else") {
		afterElse := skipSpace(src, elsePos+len("else"))
		switch {
		case hasKeywordAt(src, afterElse, "if"):
			elseLines, elseNext, elseDiags := compileGopressIf(path, src, afterElse)
			out = append(out, "} else {")
			out = append(out, elseLines...)
			out = append(out, "}")
			diags = append(diags, elseDiags...)
			return out, elseNext, diags
		case afterElse < len(src) && src[afterElse] == '{':
			closeElse := findMatching(src, afterElse, '{', '}')
			if closeElse < 0 {
				diags = append(diags, unsupportedGopressStatement(path, readRestLine(src, elsePos)))
				out = append(out, "}")
				return out, len(src), diags
			}
			elseBody, elseDiags := compileGopressBody(path, src[afterElse+1:closeElse])
			out = append(out, "} else {")
			out = append(out, elseBody...)
			out = append(out, "}")
			diags = append(diags, elseDiags...)
			return out, closeElse + 1, diags
		}
		diags = append(diags, unsupportedGopressStatement(path, readRestLine(src, elsePos)))
	}
	out = append(out, "}")
	return out, next, diags
}

func compileGopressFor(path string, src string, pos int) ([]string, int, diagnostics.List) {
	openHeader := skipSpace(src, pos+len("for"))
	if openHeader >= len(src) || src[openHeader] != '(' {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	closeHeader := findMatching(src, openHeader, '(', ')')
	if closeHeader < 0 {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	openBody := skipSpace(src, closeHeader+1)
	if openBody >= len(src) || src[openBody] != '{' {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	closeBody := findMatching(src, openBody, '{', '}')
	if closeBody < 0 {
		return nil, len(src), diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	parts := splitTopLevelSemicolons(src[openHeader+1 : closeHeader])
	if len(parts) != 3 {
		return nil, closeBody + 1, diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	init, ok := compileGopressForInit(parts[0])
	if !ok {
		return nil, closeBody + 1, diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	post, ok := compileGopressForPost(parts[2])
	if !ok {
		return nil, closeBody + 1, diagnostics.List{unsupportedGopressStatement(path, readRestLine(src, pos))}
	}
	bodyLines, diags := compileGopressBody(path, src[openBody+1:closeBody])
	out := []string{fmt.Sprintf("for %s; %s; %s {", init, compileGopressExpr(parts[1]), post)}
	out = append(out, bodyLines...)
	out = append(out, "}")
	return out, closeBody + 1, diags
}

func compileGopressStatement(path string, stmt string) ([]string, diagnostics.List) {
	stmt = strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
	if stmt == "" {
		return nil, nil
	}
	if line, ok := compileGopressVarDecl(stmt, false); ok {
		return []string{line}, nil
	}
	if strings.HasPrefix(stmt, "return ") {
		return []string{"return " + compileGopressExpr(strings.TrimSpace(strings.TrimPrefix(stmt, "return ")))}, nil
	}
	if strings.HasPrefix(stmt, "res.") || strings.HasPrefix(stmt, "next(") {
		return []string{"return " + compileGopressExpr(stmt)}, nil
	}
	if line, ok := compileGopressAssignment(stmt); ok {
		return []string{line}, nil
	}
	if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*\(`).MatchString(stmt) {
		return []string{compileGopressExpr(stmt)}, nil
	}
	return nil, diagnostics.List{unsupportedGopressStatement(path, stmt)}
}

func compileGopressForInit(stmt string) (string, bool) {
	if line, ok := compileGopressVarDecl(stmt, true); ok {
		return line, true
	}
	return compileGopressAssignment(stmt)
}

func compileGopressForPost(stmt string) (string, bool) {
	return compileGopressAssignment(stmt)
}

func compileGopressVarDecl(stmt string, forClause bool) (string, bool) {
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
		return "", false
	}
	namePart, expr, hasValue := strings.Cut(stmt, "=")
	if !hasValue {
		return "", false
	}
	name, _, _ := strings.Cut(strings.TrimSpace(namePart), ":")
	name = strings.TrimSpace(strings.TrimSuffix(name, "?"))
	if name == "" {
		return "", false
	}
	expr = strings.TrimSpace(expr)
	compiled := compileGopressExpr(expr)
	if (keyword == "let" || keyword == "var" || forClause) && isIntegerLiteral(expr) {
		compiled = expr + ".0"
	}
	if keyword == "const" && !forClause && isGopressConstLiteral(expr) {
		return fmt.Sprintf("const %s = %s", name, compiled), true
	}
	return fmt.Sprintf("%s := %s", name, compiled), true
}

func compileGopressAssignment(stmt string) (string, bool) {
	stmt = strings.TrimSpace(stmt)
	if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\+\+|--)$`).MatchString(stmt) {
		return stmt, true
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
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\[[^\]]+\])?$`).MatchString(left) || right == "" {
			return "", false
		}
		return fmt.Sprintf("%s %s %s", left, op, compileGopressExpr(right)), true
	}
	return "", false
}

func unsupportedGopressStatement(path string, stmt string) diagnostics.Diagnostic {
	stmt = strings.TrimSpace(stmt)
	if len(stmt) > 80 {
		stmt = stmt[:77] + "..."
	}
	return diagnostics.Errorf(path, diagnostics.Position{Line: 1, Column: 1}, "GODE_SUBSET_001", "unsupported gopress statement %q", stmt)
}

func compileGopressExpr(expr string) string {
	expr = strings.TrimSpace(strings.TrimSuffix(expr, ";"))
	expr = strings.ReplaceAll(expr, "===", "==")
	expr = strings.ReplaceAll(expr, "!==", "!=")
	if expr == "performance.now()" {
		return "time.Now()"
	}
	if match := regexp.MustCompile(`^performance\.now\(\)\s*-\s*([A-Za-z_][A-Za-z0-9_]*)$`).FindStringSubmatch(expr); len(match) == 2 {
		return fmt.Sprintf("float64(time.Since(%s).Microseconds()) / 1000.0", match[1])
	}
	if strings.HasPrefix(expr, "next(") {
		return expr
	}
	if strings.HasPrefix(expr, "res.") {
		return compileResponseChain(expr)
	}
	if strings.HasPrefix(expr, "{") {
		return compileObjectLiteral(expr)
	}
	if strings.HasPrefix(expr, "req.") {
		return compileRequestMember(expr)
	}
	return replaceRequestMembers(expr)
}

func compileResponseChain(expr string) string {
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
	out = replaceRequestMembers(out)
	out = replaceObjectArgs(out)
	return out
}

func replaceObjectArgs(expr string) string {
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
		expr = expr[:open] + compileObjectLiteral(expr[open:close+1]) + expr[close+1:]
	}
}

func compileObjectLiteral(expr string) string {
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
			maps = append(maps, compileGopressExpr(strings.TrimSpace(strings.TrimPrefix(part, "..."))))
			continue
		}
		if idx := findTopLevelColon(part); idx >= 0 {
			key := strings.Trim(strings.TrimSpace(part[:idx]), `"'`)
			value := strings.TrimSpace(part[idx+1:])
			entries = append(entries, fmt.Sprintf("%q: %s", key, compileGopressExpr(value)))
			continue
		}
		key := strings.Trim(strings.TrimSpace(part), `"'`)
		if key == "" {
			continue
		}
		entries = append(entries, fmt.Sprintf("%q: %s", key, compileGopressExpr(part)))
	}
	flushEntries()
	if len(maps) == 0 {
		return "map[string]any{}"
	}
	if len(maps) == 1 {
		return maps[0]
	}
	return "godeMergeJSON(" + strings.Join(maps, ", ") + ")"
}

func compileRequestMember(expr string) string {
	return replaceRequestMembers(expr)
}

func replaceRequestMembers(expr string) string {
	re := regexp.MustCompile(`req\.(params|query|body|headers|cookies)\.([A-Za-z_][A-Za-z0-9_]*)`)
	expr = re.ReplaceAllStringFunc(expr, func(match string) string {
		parts := strings.Split(match, ".")
		key := parts[2]
		switch parts[1] {
		case "params":
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
				return src[pos:i], i + 1
			}
		}
	}
	return src[pos:], len(src)
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

func isGopressConstLiteral(expr string) bool {
	expr = strings.TrimSpace(expr)
	return isIntegerLiteral(expr) ||
		regexp.MustCompile(`^-?[0-9]+\.[0-9]+$`).MatchString(expr) ||
		isStringLiteral(expr) ||
		expr == "true" ||
		expr == "false"
}

func isIntegerLiteral(expr string) bool {
	return regexp.MustCompile(`^-?[0-9]+$`).MatchString(strings.TrimSpace(expr))
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
