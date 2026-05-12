package compiler_test

import (
	"strings"
	"testing"

	"gode.dev/gode-compiler/internal/compiler"
	"gode.dev/gode-compiler/internal/config"
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
		"func BuildGopressApp() *gopress.App",
		"app := gopress.New()",
		"app.Use(gopress.JSON())",
		`app.Get("/users/:id", func(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {`,
		`return res.Status(200).JSON(map[string]any{"id": req.Params["id"]})`,
		"return app",
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, want := range []string{`"framework": "gopress"`, `"method": "GET"`, `"path": "/users/:id"`} {
		if !strings.Contains(result.Metadata, want) {
			t.Fatalf("route metadata missing %q:\n%s", want, result.Metadata)
		}
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
		`users.Route("/:id").Get(func(req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {`,
		`return res.Type("text/plain").Send(req.Method + ":" + req.Path + ":" + req.Params["id"])`,
		`app.Use("/users", users)`,
		`app.UseError(func(err error, req *gopress.Request, res *gopress.Response, next gopress.NextFunc) error {`,
		`return res.Status(500).Send("error")`,
		`return res.Redirect("/users/1")`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, result.Go)
		}
	}
	for _, want := range []string{`"target": "users"`, `"path": "/users"`, `"path": "/:id"`} {
		if !strings.Contains(result.Metadata, want) {
			t.Fatalf("route metadata missing %q:\n%s", want, result.Metadata)
		}
	}
}
