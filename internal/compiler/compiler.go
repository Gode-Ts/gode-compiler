package compiler

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Gode-Ts/gode-compiler/internal/backend/goemit"
	"github.com/Gode-Ts/gode-compiler/internal/config"
	"github.com/Gode-Ts/gode-compiler/internal/diagnostics"
	fast "github.com/Gode-Ts/gode-compiler/internal/frontend/ast"
	"github.com/Gode-Ts/gode-compiler/internal/frontend/parser"
	"github.com/Gode-Ts/gode-compiler/internal/ir"
	"github.com/Gode-Ts/gode-compiler/internal/semantics"
)

type Result struct {
	IR          ir.Module
	Go          string
	Metadata    string
	Diagnostics diagnostics.List
}

func CompileFile(path string, src []byte, cfg config.Config) Result {
	if isGopressSource(src, cfg) {
		return CompileGopress(path, src, cfg)
	}
	mod, diags := parser.ParseFile(path, src)
	if diags.HasErrors() {
		return Result{Diagnostics: diags}
	}
	irMod, semDiags := semantics.Lower(mod, cfg)
	diags = append(diags, semDiags...)
	if diags.HasErrors() && !cfg.EmitOnError {
		return Result{IR: irMod, Diagnostics: diags}
	}
	goSrc, genDiags := goemit.Emit(irMod)
	diags = append(diags, genDiags...)
	return Result{IR: irMod, Go: goSrc, Diagnostics: diags}
}

func CompileDir(path string, cfg config.Config) Result {
	files, err := collectTSFiles(path)
	if err != nil {
		return Result{Diagnostics: diagnostics.List{diagnostics.Errorf(path, diagnostics.Position{Line: 1, Column: 1}, "GODE_PARSE_001", "%s", err)}}
	}
	if cfg.Framework == "gopress" {
		var combined []byte
		for _, file := range files {
			src, err := os.ReadFile(file)
			if err != nil {
				return Result{Diagnostics: diagnostics.List{diagnostics.Errorf(file, diagnostics.Position{Line: 1, Column: 1}, "GODE_PARSE_001", "%s", err)}}
			}
			combined = append(combined, src...)
			combined = append(combined, '\n')
		}
		return CompileGopress(path, combined, cfg)
	}
	var combined fast.Module
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			return Result{Diagnostics: diagnostics.List{diagnostics.Errorf(file, diagnostics.Position{Line: 1, Column: 1}, "GODE_PARSE_001", "%s", err)}}
		}
		mod, diags := parser.ParseFile(file, src)
		if diags.HasErrors() {
			return Result{Diagnostics: diags}
		}
		if combined.File == "" {
			combined.File = file
		}
		combined.Declarations = append(combined.Declarations, mod.Declarations...)
	}
	irMod, diags := semantics.Lower(&combined, cfg)
	if diags.HasErrors() && !cfg.EmitOnError {
		return Result{IR: irMod, Diagnostics: diags}
	}
	goSrc, genDiags := goemit.Emit(irMod)
	diags = append(diags, genDiags...)
	return Result{IR: irMod, Go: goSrc, Diagnostics: diags}
}

func collectTSFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{root}, nil
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".d.ts") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}
