package compiler_test

import (
	"strings"
	"testing"

	"github.com/Gode-Ts/gode-compiler/internal/compiler"
	"github.com/Gode-Ts/gode-compiler/internal/config"
)

func TestDeclareTypeProvidesTypeWithoutEmittingStruct(t *testing.T) {
	src := []byte(`
declare type GodeContext = {
  path: string
}

declare type GodeResponse = {
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
	if strings.Contains(result.Go, "type GodeContext struct") || strings.Contains(result.Go, "type GodeResponse struct") {
		t.Fatalf("declare type should not emit Go structs:\n%s", result.Go)
	}
	if !strings.Contains(result.Go, "func GetUser(ctx context.Context, godeCtx GodeContext) (GodeResponse, error)") {
		t.Fatalf("expected function to reference ambient types, got:\n%s", result.Go)
	}
}
