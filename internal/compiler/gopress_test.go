package compiler_test

import (
	"strings"
	"testing"

	"github.com/Gode-Ts/gode-compiler/internal/compiler"
	"github.com/Gode-Ts/gode-compiler/internal/config"
)

func TestGopressInlineExpressAppCompilesToGoBuilder(t *testing.T) {
	src := []byte(`
import gopress, { Router, Request, Response, NextFunction } from "gopress"

const app = gopress()

app.use(gopress.json())

app.get("/users/:id", async (req: Request, res: Response) => {
  res.status(200).json({ id: req.params.id })
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`"github.com/Gode-Ts/gopress"`,
		`"net/http"`,
		`"strconv"`,
		"func BuildGopressApp() *gopress.App",
		"app := gopress.New()",
		"app.Use(gopress.JSON())",
		`app.HandleRawParam("GET", "/users/:id", "id", func(w http.ResponseWriter, request *http.Request, param string) error {`,
		`godeJSON := make([]byte, 0, `,
		`godeJSON = append(godeJSON, "{\"id\":"...)`,
		`godeJSON = strconv.AppendQuote(godeJSON, param)`,
		`return gopress.WriteJSONBytes(w, 200, godeJSON)`,
		"return app",
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, unwanted := range []string{
		"app.HandleFastOptions(",
		"app.HandleRawParams(",
		"res.StatusJSON(",
		`"{\"id\":"+`,
		"req.Param(",
		"params.Get(",
		"res.Status(200).JSONBytes(",
	} {
		if strings.Contains(result.Go, unwanted) {
			t.Fatalf("generated Go should not contain %q:\n%s", unwanted, result.Go)
		}
	}
	for _, want := range []string{`"framework": "gopress"`, `"method": "GET"`, `"path": "/users/:id"`} {
		if !strings.Contains(result.Metadata, want) {
			t.Fatalf("route metadata missing %q:\n%s", want, result.Metadata)
		}
	}
}

func TestGopressInlineFastHandlerDoesNotEmitUnreachableReturnNil(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.get("/ok", async (req: Request, res: Response) => {
  return res.status(200).send("ok")
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	if strings.Contains(result.Go, "return res.Status(200).Send(\"ok\")\n\t\treturn nil") {
		t.Fatalf("generated handler should not include unreachable return nil:\n%s", result.Go)
	}
}

func TestGopressNamedHandlersAndNextRoute(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response, NextFunction } from "gopress"

const app = gopress()

app.get("/users/:id", firstUser)
app.get("/users/:id", secondUser)

export async function firstUser(req: Request, res: Response, next: NextFunction): Promise<void> {
  if (req.params.id == "0") {
    return next("route")
  }
  res.send("first")
}

export async function secondUser(req: Request, res: Response): Promise<void> {
  return res.status(200).send(req.params.id)
}

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		"func FirstUser(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error",
		`if req.Params["id"] == "0" {`,
		`return next("route")`,
		`return res.Send("first")`,
		"app.Get(\"/users/:id\", FirstUser)",
		"app.Get(\"/users/:id\", SecondUser)",
		`return res.Status(200).Send(req.Params["id"])`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
}

func TestGopressInlineHandlerWithQueryUsesRawRequestPath(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.get("/search", async (req: Request, res: Response) => {
  return res.status(200).json({ page: req.query.page })
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`"net/http"`,
		`app.HandleRaw("GET", "/search", func(w http.ResponseWriter, request *http.Request) error {`,
		`godeJSON := make([]byte, 0, `,
		`godeJSON = append(godeJSON, "{\"page\":"...)`,
		`godeJSON = strconv.AppendQuote(godeJSON, gopress.QueryValue(request, "page"))`,
		`return gopress.WriteJSONBytes(w, 200, godeJSON)`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, unwanted := range []string{
		"app.HandleFastOptions(",
		`app.Get("/search"`,
		"req.Query",
		"res.Status(200).JSONBytes(",
	} {
		if strings.Contains(result.Go, unwanted) {
			t.Fatalf("generated Go should not contain %q:\n%s", unwanted, result.Go)
		}
	}
}

func TestGopressUnsupportedAPIDiagnostic(t *testing.T) {
	src := []byte(`
import gopress from "gopress"
const app = gopress()
app.engine("html", viewEngine)
export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if !result.Diagnostics.HasErrors() {
		t.Fatalf("expected unsupported API diagnostic, got Go:\n%s", result.Go)
	}
	if !strings.Contains(result.Diagnostics.String(), "GODE_SUBSET_001") || !strings.Contains(result.Diagnostics.String(), "unsupported gopress API") {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
}

func TestGopressRouterRouteBuilderAndErrorMiddleware(t *testing.T) {
	src := []byte(`
import gopress, { Router, Request, Response, NextFunction } from "gopress"

const app = gopress()
const users = Router()

users.route("/:id").get(async (req: Request, res: Response) => {
  return res.type("text/plain").send(req.method + ":" + req.path + ":" + req.params.id)
})

app.use("/users", users)
app.use((err: Error, req: Request, res: Response, next: NextFunction) => {
  return res.status(500).send("error")
})
app.get("/go", async (req: Request, res: Response) => {
  return res.redirect("/users/1")
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		"users := gopress.Router()",
		`users.HandleRawParam("GET", "/:id", "id", func(w http.ResponseWriter, request *http.Request, param string) error {`,
		`return gopress.WriteRawString(w, 200, "text/plain", request.Method+":"+request.URL.Path+":"+param)`,
		`app.Use("/users", users)`,
		`app.UseError(func(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {`,
		`return res.Status(500).Send("error")`,
		`app.HandleRaw("GET", "/go", func(w http.ResponseWriter, request *http.Request) error {`,
		`w.Header().Set("Location", "/users/1")`,
		`return gopress.WriteRawString(w, 302, "text/plain", "Found")`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, unwanted := range []string{
		`users.HandleFastOptions("GET", "/:id"`,
		`req.Param("id")`,
		`res.Type("text/plain").Send`,
		`app.HandleFastOptions("GET", "/go"`,
		`res.Redirect("/users/1")`,
	} {
		if strings.Contains(result.Go, unwanted) {
			t.Fatalf("generated Go should not contain %q:\n%s", unwanted, result.Go)
		}
	}
	for _, want := range []string{`"target": "users"`, `"path": "/users"`, `"path": "/:id"`} {
		if !strings.Contains(result.Metadata, want) {
			t.Fatalf("route metadata missing %q:\n%s", want, result.Metadata)
		}
	}
}

func TestGopressBenchmarkHandlerPreservesLoopBody(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

function runLoop(iterations: number) {
  let sum = 0
  for (let i = 0; i < iterations; i++) {
    sum += i
  }
  return { iterations, sum }
}

app.get("/bench", async (req: Request, res: Response) => {
  const iterations = 1000000
  const start = performance.now()
  const result = runLoop(iterations)
  const durationMs = performance.now() - start
  return res.status(200).json({ runtime: "gopress", durationMs, ...result })
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`"time"`,
		`"strconv"`,
		"type runLoopResult struct",
		"func runLoop(iterations int) runLoopResult",
		"sum := 0",
		"for i := 0; i < iterations; i++ {",
		"sum += i",
		"return runLoopResult{iterations: iterations, sum: sum}",
		"const iterations = 1000000",
		"start := time.Now()",
		"result := runLoop(iterations)",
		"durationMs := float64(time.Since(start).Microseconds()) / 1000.0",
		`app.HandleRaw("GET", "/bench", func(w http.ResponseWriter, request *http.Request) error {`,
		"godeJSON := make([]byte, 0, ",
		`godeJSON = append(godeJSON, "{\"runtime\":\"gopress\",\"durationMs\":"...)`,
		`godeJSON = strconv.AppendFloat(godeJSON, durationMs, 'f', -1, 64)`,
		`godeJSON = append(godeJSON, ",\"iterations\":"...)`,
		`godeJSON = strconv.AppendInt(godeJSON, int64(result.iterations), 10)`,
		`godeJSON = append(godeJSON, ",\"sum\":"...)`,
		`godeJSON = strconv.AppendInt(godeJSON, int64(result.sum), 10)`,
		`return gopress.WriteJSONBytes(w, 200, godeJSON)`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, unwanted := range []string{
		"func godeMergeJSON(parts ...map[string]any) map[string]any",
		"godeMergeJSON(",
		"map[string]any",
		"gopress.WriteJSON(w, 200",
		"res.StatusJSON(",
	} {
		if strings.Contains(result.Go, unwanted) {
			t.Fatalf("generated Go should not contain %q:\n%s", unwanted, result.Go)
		}
	}
	if strings.Contains(result.Go, `return map[string]any{"iterations": iterations, "sum": sum}
	return nil`) {
		t.Fatalf("generated helper should not include unreachable return nil:\n%s", result.Go)
	}
}

func TestGopressKnownObjectJSONUsesByteWriter(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.get("/metrics", async (req: Request, res: Response) => {
  const count = 42
  const name = "gode"
  return res.status(201).json({ ok: true, count, name })
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`app.HandleRaw("GET", "/metrics", func(w http.ResponseWriter, request *http.Request) error {`,
		`godeJSON := make([]byte, 0, `,
		`godeJSON = append(godeJSON, "{\"ok\":"...)`,
		`godeJSON = strconv.AppendBool(godeJSON, true)`,
		`godeJSON = append(godeJSON, ",\"count\":"...)`,
		`godeJSON = strconv.AppendInt(godeJSON, int64(count), 10)`,
		`godeJSON = append(godeJSON, ",\"name\":"...)`,
		`godeJSON = strconv.AppendQuote(godeJSON, name)`,
		`return gopress.WriteJSONBytes(w, 201, godeJSON)`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, unwanted := range []string{
		"res.StatusJSON(",
		`" + "`,
		"map[string]any",
		"gopress.WriteJSON(w, 201",
	} {
		if strings.Contains(result.Go, unwanted) {
			t.Fatalf("generated Go should not contain %q:\n%s", unwanted, result.Go)
		}
	}
}

func TestGopressUnsupportedHandlerStatementReportsDiagnostic(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.get("/bad", async (req: Request, res: Response) => {
  console.log("this must not be ignored")
  return res.send("ok")
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if !result.Diagnostics.HasErrors() {
		t.Fatalf("expected unsupported statement diagnostic, got Go:\n%s", result.Go)
	}
	if !strings.Contains(result.Diagnostics.String(), "GODE_SUBSET_001") || !strings.Contains(result.Diagnostics.String(), "unsupported gopress statement") {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
}

func TestGopressJSONStringAccumulatorUsesByteBuffer(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.get("/json", async (req: Request, res: Response) => {
  const start = performance.now()
  let payload = "{\"items\":["
  for (let i = 0; i < 3; i++) {
    if (i > 0) {
      payload += ","
    }
    payload += "{\"id\":" + i + ",\"name\":\"note\"}"
  }
  payload += "]}"
  const durationMs = performance.now() - start
  return res.type("application/json").send(payload)
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`"strconv"`,
		`"net/http"`,
		`app.HandleRaw("GET", "/json", func(w http.ResponseWriter, request *http.Request) error {`,
		"payload := make([]byte, 0, ",
		`payload = append(payload, "{\"items\":["...)`,
		`payload = append(payload, ","...)`,
		`payload = append(payload, "{\"id\":"...)`,
		`payload = strconv.AppendInt(payload, int64(i), 10)`,
		`payload = append(payload, ",\"name\":\"note\"}"...)`,
		`payload = append(payload, "]}"...)`,
		`return gopress.WriteJSONBytes(w, 200, payload)`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	if strings.Contains(result.Go, `"strings"`) || strings.Contains(result.Go, "strings.Builder") {
		t.Fatalf("generated Go should use []byte for JSON payloads, not strings.Builder:\n%s", result.Go)
	}
	if strings.Contains(result.Go, "payload +=") {
		t.Fatalf("generated Go should not concatenate payload repeatedly:\n%s", result.Go)
	}
}

func TestGopressJSONStringAccumulatorKeepsConstStringParts(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.get("/json", async (req: Request, res: Response) => {
  const chunk = "{\"id\":1}"
  let payload = "["
  payload += chunk
  payload += "]"
  return res.type("application/json").send(payload)
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`const chunk = "{\"id\":1}"`,
		`payload = append(payload, chunk...)`,
		`app.HandleRaw("GET", "/json", func(w http.ResponseWriter, request *http.Request) error {`,
		`return gopress.WriteJSONBytes(w, 200, payload)`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	if strings.Contains(result.Go, "FormatFloat(chunk") {
		t.Fatalf("generated Go should not treat const string chunk as a number:\n%s", result.Go)
	}
}

func TestGopressJSONByteBufferWithStatusUsesRawBytes(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.post("/json", async (req: Request, res: Response) => {
  let payload = "{"
  payload += "\"created\":true"
  payload += "}"
  return res.status(201).type("application/json").send(payload)
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`app.HandleRaw("POST", "/json", func(w http.ResponseWriter, request *http.Request) error {`,
		"payload := make([]byte, 0, ",
		`return gopress.WriteJSONBytes(w, 201, payload)`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, unwanted := range []string{
		`res.Status(201).Type("application/json").Send`,
		`gopress.WriteRawString(w, 201, "application/json"`,
	} {
		if strings.Contains(result.Go, unwanted) {
			t.Fatalf("generated Go should not contain %q:\n%s", unwanted, result.Go)
		}
	}
}

func TestGopressRedirectWithStatusUsesRawHandler(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.get("/old", async (req: Request, res: Response) => {
  return res.redirect(301, "/new")
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`app.HandleRaw("GET", "/old", func(w http.ResponseWriter, request *http.Request) error {`,
		`w.Header().Set("Location", "/new")`,
		`return gopress.WriteRawString(w, 301, "text/plain", "Moved Permanently")`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, unwanted := range []string{
		`app.HandleFastOptions("GET", "/old"`,
		`res.Redirect(301, "/new")`,
	} {
		if strings.Contains(result.Go, unwanted) {
			t.Fatalf("generated Go should not contain %q:\n%s", unwanted, result.Go)
		}
	}
}

func TestGopressJSONByteBufferDetectionAllowsWhitespace(t *testing.T) {
	src := []byte(`
import gopress, { Request, Response } from "gopress"

const app = gopress()

app.get("/json", async (req: Request, res: Response) => {
  let payload = "["
  payload += "{\"id\":1}"
  payload += "]"
  return res.type("application/json")
    .send(payload)
})

export default app
`)

	result := compiler.CompileFile("app.ts", src, config.Default().WithFramework("gopress").WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	for _, want := range []string{
		`app.HandleRaw("GET", "/json", func(w http.ResponseWriter, request *http.Request) error {`,
		"payload := make([]byte, 0, ",
		`return gopress.WriteJSONBytes(w, 200, payload)`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
}

func TestGopressCompilerAvoidsRegexAllocationChurn(t *testing.T) {
	cfg := config.Default().WithFramework("gopress").WithPackage("main")
	allocs := testing.AllocsPerRun(10, func() {
		result := compiler.CompileFile("app.ts", gopressBenchmarkSource, cfg)
		if result.Diagnostics.HasErrors() {
			t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
		}
	})
	if allocs > 5000 {
		t.Fatalf("gopress compiler allocations too high: got %.0f allocs/run, want <= 5000", allocs)
	}
}
