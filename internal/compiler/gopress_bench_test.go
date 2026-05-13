package compiler_test

import (
	"testing"

	"github.com/Gode-Ts/gode-compiler/internal/compiler"
	"github.com/Gode-Ts/gode-compiler/internal/config"
)

var gopressBenchmarkSource = []byte(`
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

app.get("/json", async (req: Request, res: Response) => {
  const chunk = "{\"id\":1,\"name\":\"note\"}"
  let payload = "{\"items\":["
  for (let i = 0; i < 1000; i++) {
    if (i > 0) {
      payload += ","
    }
    payload += chunk
  }
  payload += "]}"
  return res.type("application/json").send(payload)
})

export default app
`)

func BenchmarkCompileGopressBenchmarkApp(b *testing.B) {
	cfg := config.Default().WithFramework("gopress").WithPackage("main")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result := compiler.CompileFile("app.ts", gopressBenchmarkSource, cfg)
		if result.Diagnostics.HasErrors() {
			b.Fatalf("unexpected diagnostics: %s", result.Diagnostics.String())
		}
	}
}
