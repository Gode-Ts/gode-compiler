package compiler_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gode.dev/gode-compiler/internal/compiler"
	"gode.dev/gode-compiler/internal/config"
)

func TestGoldenFixtures(t *testing.T) {
	fixtures, err := os.ReadDir(filepath.Join("..", "..", "testdata", "golden"))
	if err != nil {
		t.Fatal(err)
	}

	for _, fixture := range fixtures {
		if !fixture.IsDir() {
			continue
		}
		t.Run(fixture.Name(), func(t *testing.T) {
			dir := filepath.Join("..", "..", "testdata", "golden", fixture.Name())
			inputPath := filepath.Join(dir, "input.ts")
			input, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatal(err)
			}

			result := compiler.CompileFile(inputPath, input, config.Default().WithPackage("api"))

			if wantDiagnostics, err := os.ReadFile(filepath.Join(dir, "want.diagnostics")); err == nil {
				got := strings.TrimSpace(result.Diagnostics.String())
				want := strings.TrimSpace(string(wantDiagnostics))
				if got != want {
					t.Fatalf("diagnostics mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
				}
				return
			}

			if result.Diagnostics.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
			}

			wantIR, err := os.ReadFile(filepath.Join(dir, "want.ir.json"))
			if err != nil {
				t.Fatal(err)
			}
			gotIRBytes, err := json.MarshalIndent(result.IR, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(gotIRBytes)) != strings.TrimSpace(string(wantIR)) {
				t.Fatalf("IR mismatch\nwant:\n%s\n\ngot:\n%s", wantIR, gotIRBytes)
			}

			wantGo, err := os.ReadFile(filepath.Join(dir, "want.go"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(result.Go) != strings.TrimSpace(string(wantGo)) {
				t.Fatalf("Go output mismatch\nwant:\n%s\n\ngot:\n%s", wantGo, result.Go)
			}
		})
	}
}
