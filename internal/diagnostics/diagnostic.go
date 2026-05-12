package diagnostics

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	File     string   `json:"file"`
	Start    Position `json:"start"`
	End      Position `json:"end,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

type List []Diagnostic

func (l List) HasErrors() bool {
	for _, diag := range l {
		if diag.Severity == Error {
			return true
		}
	}
	return false
}

func (l List) Sorted() List {
	out := append(List(nil), l...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Start.Line != out[j].Start.Line {
			return out[i].Start.Line < out[j].Start.Line
		}
		if out[i].Start.Column != out[j].Start.Column {
			return out[i].Start.Column < out[j].Start.Column
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func (l List) String() string {
	if len(l) == 0 {
		return ""
	}
	lines := make([]string, 0, len(l))
	for _, diag := range l.Sorted() {
		line := diag.Start.Line
		if line == 0 {
			line = 1
		}
		col := diag.Start.Column
		if col == 0 {
			col = 1
		}
		msg := fmt.Sprintf("%s:%d:%d %s %s: %s", diag.File, line, col, diag.Code, diag.Severity, diag.Message)
		if diag.Hint != "" {
			msg += " hint: " + diag.Hint
		}
		lines = append(lines, msg)
	}
	return strings.Join(lines, "\n")
}

func (l List) JSON() string {
	if len(l) == 0 {
		return "[]"
	}
	data, err := json.MarshalIndent(l.Sorted(), "", "  ")
	if err != nil {
		return "[]"
	}
	return string(data)
}

func Errorf(file string, pos Position, code, format string, args ...any) Diagnostic {
	return Diagnostic{
		Code:     code,
		Severity: Error,
		Message:  fmt.Sprintf(format, args...),
		File:     file,
		Start:    pos,
	}
}
