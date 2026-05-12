package parser_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gode-Ts/gode-compiler/internal/frontend/parser"
)

func TestParseNormalizesBackendTypeScript(t *testing.T) {
	src := []byte(`
export type User = {
  id: string
  age: number
  active?: boolean
}

export function isAdult(user: User): boolean {
  return user.age >= 18
}
`)

	mod, diags := parser.ParseFile("input.ts", src)
	if diags.HasErrors() {
		t.Fatalf("expected no diagnostics, got:\n%s", diags.String())
	}

	got, err := json.MarshalIndent(mod, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	want := `{
  "file": "input.ts",
  "declarations": [
    {
      "kind": "type_alias",
      "name": "User",
      "exported": true,
      "type": {
        "kind": "object",
        "fields": [
          {
            "name": "id",
            "type": {
              "kind": "primitive",
              "name": "string"
            }
          },
          {
            "name": "age",
            "type": {
              "kind": "primitive",
              "name": "number"
            }
          },
          {
            "name": "active",
            "optional": true,
            "type": {
              "kind": "primitive",
              "name": "boolean"
            }
          }
        ]
      }
    },
    {
      "kind": "function",
      "name": "isAdult",
      "exported": true,
      "params": [
        {
          "name": "user",
          "type": {
            "kind": "reference",
            "name": "User"
          }
        }
      ],
      "returnType": {
        "kind": "primitive",
        "name": "boolean"
      },
      "body": [
        {
          "kind": "return",
          "expr": {
            "kind": "binary",
            "op": "\u003e=",
            "left": {
              "kind": "member",
              "object": {
                "kind": "identifier",
                "name": "user"
              },
              "property": "age"
            },
            "right": {
              "kind": "number",
              "value": "18"
            }
          }
        }
      ]
    }
  ]
}`

	if strings.TrimSpace(string(got)) != strings.TrimSpace(want) {
		t.Fatalf("normalized AST mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestParseRejectsUnsupportedConstructsWithStableDiagnostics(t *testing.T) {
	_, diags := parser.ParseFile("input.ts", []byte(`export class User {}`))
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics")
	}
	if got := diags[0].Code; got != "GODE_SUBSET_001" {
		t.Fatalf("expected GODE_SUBSET_001, got %s", got)
	}
}
