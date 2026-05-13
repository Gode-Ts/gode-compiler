package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gode-Ts/gode-compiler/internal/compiler"
	"github.com/Gode-Ts/gode-compiler/internal/config"
)

const version = "0.1.13"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: godec <check|compile|version> [path] [flags]")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "godec %s\n", version)
		return 0
	case "check":
		return runCompileLike(args[1:], stdout, stderr, false)
	case "compile":
		return runCompileLike(args[1:], stdout, stderr, true)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runCompileLike(args []string, stdout, stderr io.Writer, emit bool) int {
	fs := flag.NewFlagSet("godec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path")
	out := fs.String("out", "", "output directory")
	pkg := fs.String("package", "", "Go package name")
	format := fs.String("format", "human", "diagnostic format: human|json")
	emitOnError := fs.Bool("emit-on-error", false, "emit Go even when diagnostics contain errors")
	strict := fs.Bool("strict", true, "enable strict subset validation")
	flagArgs, positional := splitFlagArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "failed to load config: %v\n", err)
		return 2
	}
	cfg = cfg.WithPackage(*pkg).WithOut(*out)
	cfg.EmitOnError = *emitOnError
	cfg.Strict = *strict
	entry := cfg.Entry
	if len(positional) > 0 {
		entry = positional[0]
	}
	result := compiler.CompileDir(entry, cfg)
	if len(result.Diagnostics) > 0 {
		if *format == "json" {
			fmt.Fprintln(stdout, result.Diagnostics.JSON())
		} else {
			fmt.Fprintln(stdout, result.Diagnostics.String())
		}
	}
	if result.Diagnostics.HasErrors() {
		return 1
	}
	if !emit {
		return 0
	}
	if cfg.Out == "" {
		fmt.Fprintln(stderr, "missing --out")
		return 2
	}
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		fmt.Fprintf(stderr, "failed to create output directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(cfg.Out, "gode_gen.go"), []byte(result.Go), 0o644); err != nil {
		fmt.Fprintf(stderr, "failed to write output: %v\n", err)
		return 1
	}
	if result.Metadata != "" {
		if err := os.WriteFile(filepath.Join(cfg.Out, "gode_routes.json"), []byte(result.Metadata), 0o644); err != nil {
			fmt.Fprintf(stderr, "failed to write route metadata: %v\n", err)
			return 1
		}
	}
	return 0
}

func splitFlagArgs(args []string) ([]string, []string) {
	boolFlags := map[string]bool{
		"--emit-on-error": true,
		"--strict":        true,
	}
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := arg
		if idx := strings.Index(arg, "="); idx >= 0 {
			name = arg[:idx]
		}
		if boolFlags[name] || strings.Contains(arg, "=") {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return flags, positional
}
