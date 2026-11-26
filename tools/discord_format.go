package tools

import (
	"regexp"
	"strings"
)

var tableDelimiterRe = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)

// ConvertTables rewrites markdown tables, which Discord shows as raw pipes,
// into lists. A two-column row becomes "- **name**: value"; a wider row
// becomes a bold name line followed by one "- Header: value" item per column,
// with a blank line between rows.
// Tables inside code blocks are left alone.
func ConvertTables(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inCode := false
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			inCode = !inCode
		}
		if inCode || !isTableStart(lines, i) {
			out = append(out, lines[i])
			continue
		}
		header := tableCells(lines[i])
		i += 2
		for row := 0; i < len(lines) && strings.Contains(lines[i], "|"); i, row = i+1, row+1 {
			if row > 0 && len(header) > 2 {
				out = append(out, "")
			}
			out = append(out, tableRow(header, tableCells(lines[i]))...)
		}
		i--
	}
	return strings.Join(out, "\n")
}

func isTableStart(lines []string, i int) bool {
	return i+1 < len(lines) &&
		strings.Contains(lines[i], "|") &&
		strings.Contains(lines[i+1], "|") &&
		tableDelimiterRe.MatchString(lines[i+1])
}

func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := strings.Split(line, "|")
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}

func tableRow(header, cells []string) []string {
	name := cells[0]
	if name != "" && !strings.Contains(name, "**") {
		name = "**" + name + "**"
	}
	var values []string
	for i, c := range cells[1:] {
		if strings.Trim(c, "-—– ") == "" {
			continue
		}
		if len(header) > 2 && i+1 < len(header) && header[i+1] != "" {
			c = header[i+1] + ": " + c
		}
		values = append(values, c)
	}
	if len(header) <= 2 {
		if len(values) == 0 {
			return []string{"- " + name}
		}
		if name == "" {
			return []string{"- " + values[0]}
		}
		return []string{"- " + name + ": " + strings.Join(values, " ")}
	}
	var lines []string
	if name != "" {
		lines = append(lines, name)
	}
	for _, v := range values {
		lines = append(lines, "- "+v)
	}
	return lines
}
