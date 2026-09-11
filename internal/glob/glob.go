// Package glob matches full upstream IDs, including namespace separators.
package glob

import (
	"fmt"
	"regexp"
	"strings"
)

type Pattern struct{ expression *regexp.Regexp }

// Compile accepts *, ?, bracket classes/ranges, and backslash escapes. Unlike
// filesystem globs, wildcards match slashes and matching never ignores case.
func Compile(pattern string) (Pattern, error) {
	var out strings.Builder
	out.WriteString(`(?s)\A`)
	chars := []rune(pattern)
	for i := 0; i < len(chars); i++ {
		switch chars[i] {
		case '*':
			out.WriteString(".*")
		case '?':
			out.WriteByte('.')
		case '\\':
			i++
			if i == len(chars) {
				return Pattern{}, fmt.Errorf("invalid glob: trailing escape")
			}
			out.WriteString(regexp.QuoteMeta(string(chars[i])))
		case '[':
			out.WriteByte('[')
			i++
			if i < len(chars) && (chars[i] == '!' || chars[i] == '^') {
				out.WriteByte('^')
				i++
			}
			start := i
			for ; i < len(chars) && chars[i] != ']'; i++ {
				if chars[i] == '\\' {
					i++
					if i == len(chars) {
						return Pattern{}, fmt.Errorf("invalid glob: trailing class escape")
					}
					out.WriteString(regexp.QuoteMeta(string(chars[i])))
				} else if chars[i] == '[' {
					out.WriteString(`\[`)
				} else {
					out.WriteRune(chars[i])
				}
			}
			if i == len(chars) || i == start {
				return Pattern{}, fmt.Errorf("invalid glob: unclosed or empty class")
			}
			out.WriteByte(']')
		default:
			out.WriteString(regexp.QuoteMeta(string(chars[i])))
		}
	}
	out.WriteString(`\z`)
	compiled, err := regexp.Compile(out.String())
	if err != nil {
		return Pattern{}, fmt.Errorf("invalid glob: %w", err)
	}
	return Pattern{expression: compiled}, nil
}

func (p Pattern) Match(value string) bool {
	return p.expression != nil && p.expression.MatchString(value)
}

func Any(patterns []Pattern, value string) bool {
	for _, pattern := range patterns {
		if pattern.Match(value) {
			return true
		}
	}
	return false
}

func CompileAll(patterns []string) ([]Pattern, error) {
	result := make([]Pattern, 0, len(patterns))
	for _, text := range patterns {
		pattern, err := Compile(text)
		if err != nil {
			return nil, err
		}
		result = append(result, pattern)
	}
	return result, nil
}
