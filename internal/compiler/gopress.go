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

	"gode.dev/gode-compiler/internal/config"
	"gode.dev/gode-compiler/internal/diagnostics"
	"gode.dev/gode-compiler/internal/ir"
	"gode.dev/gode-compiler/internal/names"
)

type gopressApp struct {
	packageName string
	appName     string
	vars        map[string]string
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
	app := &gopressApp{packageName: cfg.Package, vars: map[string]string{}}
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

func findFunctionSpans(src string) [][2]int {
	var spans [][2]int
	re := regexp.MustCompile(`export\s+(?:async\s+)?function\s+[A-Za-z_][A-Za-z0-9_]*\s*\([^)]*\)\s*:\s*Promise<[^>]+>\s*\{`)
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
	w(")")
	w("")
	for _, handler := range a.handlers {
		if handler.IsError {
			w("func %s(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {", handler.GoName)
		} else {
			w("func %s(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {", handler.GoName)
		}
		for _, line := range compileGopressBody(handler.Body) {
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
		return compileInlineHandler(trimmed)
	}
	if _, ok := a.vars[trimmed]; ok {
		return trimmed
	}
	return names.Exported(trimmed)
}

func (a *gopressApp) compileErrorHandlerArg(arg string) string {
	trimmed := strings.TrimSpace(arg)
	if strings.Contains(trimmed, "=>") {
		return compileInlineErrorHandler(trimmed)
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

func compileInlineHandler(src string) string {
	bodyStart := strings.Index(src, "{")
	bodyEnd := strings.LastIndex(src, "}")
	if bodyStart < 0 || bodyEnd < bodyStart {
		return "func(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error { return nil }"
	}
	lines := compileGopressBody(src[bodyStart+1 : bodyEnd])
	return fmt.Sprintf("func(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {\n%s\nreturn nil\n}", strings.Join(lines, "\n"))
}

func compileInlineErrorHandler(src string) string {
	bodyStart := strings.Index(src, "{")
	bodyEnd := strings.LastIndex(src, "}")
	if bodyStart < 0 || bodyEnd < bodyStart {
		return "func(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error { return nil }"
	}
	lines := compileGopressBody(src[bodyStart+1 : bodyEnd])
	return fmt.Sprintf("func(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {\n%s\nreturn nil\n}", strings.Join(lines, "\n"))
}

func compileGopressBody(body string) []string {
	var out []string
	remaining := body
	for {
		idx := strings.Index(remaining, "if")
		if idx < 0 {
			break
		}
		before := strings.TrimSpace(remaining[:idx])
		out = append(out, compileGopressStatements(before)...)
		openCond := strings.Index(remaining[idx:], "(")
		if openCond < 0 {
			break
		}
		openCond += idx
		closeCond := findMatching(remaining, openCond, '(', ')')
		openBody := strings.Index(remaining[closeCond:], "{")
		if closeCond < 0 || openBody < 0 {
			break
		}
		openBody += closeCond
		closeBody := findMatching(remaining, openBody, '{', '}')
		if closeBody < 0 {
			break
		}
		out = append(out, fmt.Sprintf("if %s {", compileGopressExpr(remaining[openCond+1:closeCond])))
		out = append(out, compileGopressStatements(remaining[openBody+1:closeBody])...)
		out = append(out, "}")
		remaining = remaining[closeBody+1:]
	}
	out = append(out, compileGopressStatements(remaining)...)
	return out
}

func compileGopressStatements(src string) []string {
	var out []string
	for _, stmt := range splitStatements(src) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if strings.HasPrefix(stmt, "return ") {
			out = append(out, "return "+compileGopressExpr(strings.TrimSpace(strings.TrimPrefix(stmt, "return "))))
			continue
		}
		if strings.HasPrefix(stmt, "res.") || strings.HasPrefix(stmt, "next(") {
			out = append(out, "return "+compileGopressExpr(stmt))
			continue
		}
	}
	return out
}

func compileGopressExpr(expr string) string {
	expr = strings.TrimSpace(strings.TrimSuffix(expr, ";"))
	expr = strings.ReplaceAll(expr, "===", "==")
	expr = strings.ReplaceAll(expr, "!==", "!=")
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
	return expr
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
	var entries []string
	for _, part := range parts {
		key, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"'`)
		entries = append(entries, fmt.Sprintf("%q: %s", key, compileGopressExpr(value)))
	}
	return "map[string]any{" + strings.Join(entries, ", ") + "}"
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

func splitStatements(src string) []string {
	var out []string
	var current strings.Builder
	depth := 0
	inString := rune(0)
	for _, r := range src {
		if inString != 0 {
			current.WriteRune(r)
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
		case ';', '\n':
			if depth == 0 {
				out = append(out, current.String())
				current.Reset()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		out = append(out, current.String())
	}
	return out
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
