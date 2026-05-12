package compiler_test

import (
	"strings"
	"testing"

	"github.com/Gode-Ts/gode-compiler/internal/compiler"
	"github.com/Gode-Ts/gode-compiler/internal/config"
)

func TestAsyncHandlerParamNamedCtxDoesNotCollideWithGoContext(t *testing.T) {
	src := []byte(`
export type GodeContext = {
  path: string
}

export type GodeResponse = {
  status: number
  body: string
  contentType: string
}

export async function getUser(ctx: GodeContext): Promise<GodeResponse> {
  return { status: 200, body: ctx.path, contentType: "text/plain" }
}
`)

	result := compiler.CompileFile("input.ts", src, config.Default().WithPackage("main"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	if strings.Contains(result.Go, "func GetUser(ctx context.Context, ctx GodeContext)") {
		t.Fatalf("async function emitted duplicate ctx parameters:\n%s", result.Go)
	}
	if !strings.Contains(result.Go, "func GetUser(ctx context.Context, godeCtx GodeContext) (GodeResponse, error)") {
		t.Fatalf("expected user ctx param to be renamed to godeCtx, got:\n%s", result.Go)
	}
	if !strings.Contains(result.Go, "return GodeResponse{Status: 200, Body: godeCtx.Path, ContentType: \"text/plain\"}, nil") {
		t.Fatalf("expected renamed parameter references in function body, got:\n%s", result.Go)
	}
}
