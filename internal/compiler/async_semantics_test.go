package compiler_test

import (
	"strings"
	"testing"

	"gode.dev/gode-compiler/internal/compiler"
	"gode.dev/gode-compiler/internal/config"
)

func TestDependentAwaitsAreSequential(t *testing.T) {
	src := []byte(`
export type User = {
  id: string
}

export type Profile = {
  id: string
}

declare function fetchUser(id: string): Promise<User>
declare function fetchProfile(id: string): Promise<Profile>

export async function loadProfile(id: string): Promise<Profile> {
  const user = await fetchUser(id)
  const profile = await fetchProfile(user.id)
  return profile
}
`)

	result := compiler.CompileFile("input.ts", src, config.Default().WithPackage("api"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	if strings.Contains(result.Go, "errgroup.WithContext") {
		t.Fatalf("dependent awaits must not be grouped in errgroup:\n%s", result.Go)
	}
	want := "user, err := FetchUser(ctx, id)\n\tif err != nil"
	if !strings.Contains(result.Go, want) {
		t.Fatalf("expected first await to be sequential with error handling, got:\n%s", result.Go)
	}
	want = "profile, err := FetchProfile(ctx, user.ID)\n\tif err != nil"
	if !strings.Contains(result.Go, want) {
		t.Fatalf("expected dependent await to run after user is available, got:\n%s", result.Go)
	}
}

func TestReturnAwaitLowersToErrorAwareReturn(t *testing.T) {
	src := []byte(`
export type User = {
  id: string
}

declare function fetchUser(id: string): Promise<User>

export async function loadUser(id: string): Promise<User> {
  return await fetchUser(id)
}
`)

	result := compiler.CompileFile("input.ts", src, config.Default().WithPackage("api"))
	if result.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.Diagnostics.String())
	}
	if strings.Contains(result.Go, "return FetchUser(ctx, id), nil") {
		t.Fatalf("return await emitted invalid tuple return:\n%s", result.Go)
	}
	if !strings.Contains(result.Go, "return FetchUser(ctx, id)") {
		t.Fatalf("return await should delegate the async call directly, got:\n%s", result.Go)
	}
}
