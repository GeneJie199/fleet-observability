package compat

import (
	"regexp"
	"strings"
	"unicode"
)

var underscores = regexp.MustCompile(`_+`)

func metricName(raw string) string {
	raw = strings.TrimSpace(raw)
	var out strings.Builder
	for i, r := range raw {
		valid := unicode.IsLetter(r) || r == '_' || r == ':' || (i > 0 && unicode.IsDigit(r)) || (i > 0 && (r == '.' || r == '-'))
		if valid {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	name := underscores.ReplaceAllString(out.String(), "_")
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "metric_" + name
	}
	name = strings.TrimRight(name, "_")
	if name == "" {
		return "metric"
	}
	return name
}

func labelName(raw string) string {
	raw = strings.TrimSpace(raw)
	var out strings.Builder
	for i, r := range raw {
		if unicode.IsLetter(r) || r == '_' || (i > 0 && (unicode.IsDigit(r) || r == '.' || r == '-')) {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	name := underscores.ReplaceAllString(out.String(), "_")
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "label_" + name
	}
	name = strings.TrimRight(name, "_")
	if name == "" {
		return "label"
	}
	return name
}

func splitUnescaped(raw string, separator byte) []string {
	parts := []string{}
	start := 0
	escaped := false
	quoted := false
	for i := 0; i < len(raw); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch raw[i] {
		case '\\':
			escaped = true
		case '"':
			quoted = !quoted
		default:
			if raw[i] == separator && !quoted {
				parts = append(parts, raw[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, raw[start:])
}

func unescape(raw string) string {
	replacer := strings.NewReplacer(`\,`, ",", `\ `, " ", `\=`, "=", `\\`, `\`)
	return replacer.Replace(raw)
}
