# Gode Compiler

`godec` compiles a strict backend-oriented TypeScript subset into idiomatic Go.

The first compiler version is intentionally not a general JavaScript runtime transpiler. It accepts explicit TypeScript types, validates a documented subset, and emits stable diagnostics for unsupported constructs.

## Commands

```bash
godec check ./src
godec compile ./src --out ./gen --package api
godec version
```

## V1 Defaults

- TypeScript `number` lowers to Go `float64`.
- Optional object fields lower to pointers and `json:",omitempty"`.
- `async/await` uses Gode concurrency semantics, lowering independent awaits to Go `errgroup`.
- V1 emits a single Go package.

