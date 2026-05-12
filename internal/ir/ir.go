package ir

import fast "gode.dev/gode-compiler/internal/frontend/ast"

type Type = fast.Type
type Expr = fast.Expr
type Statement = fast.Statement

type Module struct {
	PackageName  string        `json:"packageName"`
	UsesAsync    bool          `json:"usesAsync,omitempty"`
	Declarations []Declaration `json:"declarations"`
}

type Declaration struct {
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	GoName     string      `json:"goName,omitempty"`
	Exported   bool        `json:"exported,omitempty"`
	Async      bool        `json:"async,omitempty"`
	Fields     []Field     `json:"fields,omitempty"`
	Params     []Param     `json:"params,omitempty"`
	ReturnType *Type       `json:"returnType,omitempty"`
	Body       []Statement `json:"body,omitempty"`
}

type Field struct {
	Name     string `json:"name"`
	GoName   string `json:"goName"`
	Optional bool   `json:"optional,omitempty"`
	Type     *Type  `json:"type"`
	JSONName string `json:"jsonName,omitempty"`
}

type Param struct {
	Name   string `json:"name"`
	GoName string `json:"goName"`
	Type   *Type  `json:"type"`
}
