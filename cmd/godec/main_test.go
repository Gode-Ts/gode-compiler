package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCheckReturnsDiagnosticsWithoutWritingOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	out := filepath.Join(dir, "gen")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "bad.ts"), []byte(`export class User {}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := run([]string{"check", src, "--format", "json", "--out", out}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), `"code": "GODE_SUBSET_001"`) {
		t.Fatalf("expected JSON diagnostic, got stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("check should not write output directory")
	}
}

func TestRunCompileWritesGeneratedGo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	out := filepath.Join(dir, "gen")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "user.ts"), []byte(`
export type User = {
  id: string
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := run([]string{"compile", src, "--out", out, "--package", "api"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	got, err := os.ReadFile(filepath.Join(out, "gode_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "type User struct") {
		t.Fatalf("expected generated struct, got:\n%s", got)
	}
}
