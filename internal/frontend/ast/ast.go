package ast

import (
	"bytes"
	"encoding/json"
)

type Module struct {
	File         string        `json:"file"`
	Declarations []Declaration `json:"declarations"`
}

type Declaration struct {
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	Exported   bool        `json:"exported,omitempty"`
	Async      bool        `json:"async,omitempty"`
	Declare    bool        `json:"-"`
	Type       *Type       `json:"type,omitempty"`
	Params     []Param     `json:"params,omitempty"`
	ReturnType *Type       `json:"returnType,omitempty"`
	Body       []Statement `json:"body,omitempty"`
}

type Param struct {
	Name string `json:"name"`
	Type *Type  `json:"type"`
}

type Field struct {
	Name     string `json:"name"`
	Optional bool   `json:"optional,omitempty"`
	Type     *Type  `json:"type"`
}

type Type struct {
	Kind   string  `json:"kind"`
	Name   string  `json:"name,omitempty"`
	Inner  *Type   `json:"inner,omitempty"`
	Key    *Type   `json:"key,omitempty"`
	Value  *Type   `json:"value,omitempty"`
	Fields []Field `json:"fields,omitempty"`
}

type Statement struct {
	Kind string      `json:"kind"`
	Name string      `json:"name,omitempty"`
	Type *Type       `json:"type,omitempty"`
	Expr *Expr       `json:"expr,omitempty"`
	Then []Statement `json:"then,omitempty"`
	Else []Statement `json:"else,omitempty"`
	Body []Statement `json:"body,omitempty"`
}

type Expr struct {
	Kind     string        `json:"kind"`
	Op       string        `json:"op,omitempty"`
	Left     *Expr         `json:"left,omitempty"`
	Right    *Expr         `json:"right,omitempty"`
	Object   *Expr         `json:"object,omitempty"`
	Property string        `json:"property,omitempty"`
	Name     string        `json:"name,omitempty"`
	Value    string        `json:"value,omitempty"`
	Callee   *Expr         `json:"callee,omitempty"`
	Args     []Expr        `json:"args,omitempty"`
	Expr     *Expr         `json:"expr,omitempty"`
	Entries  []ObjectEntry `json:"entries,omitempty"`
}

func (e Expr) MarshalJSON() ([]byte, error) {
	type expr Expr
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(expr(e)); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

type ObjectEntry struct {
	Key   string `json:"key"`
	Value *Expr  `json:"value"`
}

func Primitive(name string) *Type {
	return &Type{Kind: "primitive", Name: name}
}

func Reference(name string) *Type {
	return &Type{Kind: "reference", Name: name}
}
