package names

import "strings"

var initialisms = map[string]string{
	"id":   "ID",
	"url":  "URL",
	"api":  "API",
	"http": "HTTP",
	"json": "JSON",
	"sql":  "SQL",
	"ip":   "IP",
}

func Exported(name string) string {
	parts := splitIdentifier(name)
	for i, part := range parts {
		lower := strings.ToLower(part)
		if initialism, ok := initialisms[lower]; ok {
			parts[i] = initialism
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func Local(name string) string {
	if name == "" {
		return name
	}
	return strings.ToLower(name[:1]) + name[1:]
}

func splitIdentifier(name string) []string {
	if strings.Contains(name, "_") || strings.Contains(name, "-") {
		raw := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' })
		parts := raw[:0]
		for _, part := range raw {
			if part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			return parts
		}
	}
	return []string{name}
}
