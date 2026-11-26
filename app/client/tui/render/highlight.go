// A small, dependency-free syntax highlighter for fenced code blocks.
//
// It is approximate on purpose: it colours comments, strings, numbers and
// keywords — the four things that make code scannable — and leaves everything
// else in the terminal's default colour. There is no parser and no grammar; a
// language is just a comment syntax, a string-quote set and a keyword list. When
// a fence names no language (or an unknown one), a generic spec still picks up
// the C-style `//` and shell `#` comments, quoted strings and numbers.
package render

import (
	"strings"

	"github.com/niq-run/niq/app/client/tui/term"
)

// langSpec is what distinguishes one language's highlighting from another.
type langSpec struct {
	lineComments []string
	blockOpen    string
	blockClose   string
	quotes       string
	keywords     map[string]bool
}

// highlighter carries the only cross-line state that matters: whether we are
// inside a block comment.
type highlighter struct {
	spec    langSpec
	inBlock bool
}

func newHighlighter(langName string) *highlighter {
	return &highlighter{spec: langFor(langName)}
}

// langFor resolves a fence info string ("go", "py", "ts", …) to a spec.
func langFor(name string) langSpec {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "go", "golang":
		return langSpec{
			lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/",
			quotes: "`\"'",
			keywords: wordSet("break case chan const continue default defer else fallthrough for func go goto " +
				"if import interface map package range return select struct switch type var true false nil iota"),
		}
	case "py", "python":
		return langSpec{
			lineComments: []string{"#"},
			quotes:       "\"'",
			keywords: wordSet("False None True and as assert async await break class continue def del elif else " +
				"except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield self"),
		}
	case "js", "javascript", "ts", "typescript", "jsx", "tsx":
		return langSpec{
			lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/",
			quotes: "`\"'",
			keywords: wordSet("var let const function return if else for while do switch case break continue new " +
				"throw try catch finally class extends super import export default async await yield typeof " +
				"instanceof in of this null undefined true false interface type enum implements namespace public " +
				"private protected readonly declare abstract"),
		}
	case "json":
		return langSpec{quotes: "\"", keywords: wordSet("true false null")}
	case "sh", "bash", "shell", "zsh", "console":
		return langSpec{
			lineComments: []string{"#"},
			quotes:       "\"'",
			keywords: wordSet("if then else elif fi for while until do done case esac function in return local " +
				"export echo exit set unset read source alias"),
		}
	case "rs", "rust":
		return langSpec{
			lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/",
			quotes: "\"'",
			keywords: wordSet("fn let mut const static struct enum impl trait pub use mod crate self super match if " +
				"else loop while for in return break continue where async await move ref true false"),
		}
	default:
		return langSpec{
			lineComments: []string{"//", "#"}, blockOpen: "/*", blockClose: "*/",
			quotes:   "\"'`",
			keywords: wordSet("true false null if else for while return function func var let const new class import export def"),
		}
	}
}

func wordSet(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// line highlights one source line, applying the styles and carrying block-comment
// state into the next line.
func (h *highlighter) line(s string) string {
	var b strings.Builder
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			b.WriteString(plain.String())
			plain.Reset()
		}
	}

	i := 0
	for i < len(s) {
		if h.inBlock {
			if idx := strings.Index(s[i:], h.spec.blockClose); idx >= 0 {
				end := i + idx + len(h.spec.blockClose)
				flush()
				b.WriteString(term.CodeComment(s[i:end]))
				i = end
				h.inBlock = false
			} else {
				flush()
				b.WriteString(term.CodeComment(s[i:]))
				i = len(s)
			}
			continue
		}

		// Start of a block comment: enter the state; the branch above consumes it
		// on the next iteration.
		if h.spec.blockOpen != "" && strings.HasPrefix(s[i:], h.spec.blockOpen) {
			h.inBlock = true
			continue
		}

		// Line comment: the rest of the line.
		hit := false
		for _, lc := range h.spec.lineComments {
			if strings.HasPrefix(s[i:], lc) {
				flush()
				b.WriteString(term.CodeComment(s[i:]))
				i = len(s)
				hit = true
				break
			}
		}
		if hit {
			continue
		}

		c := s[i]
		// String literal, quoted; honours backslash escapes.
		if strings.IndexByte(h.spec.quotes, c) >= 0 {
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == c {
					j++
					break
				}
				j++
			}
			if j > len(s) {
				j = len(s)
			}
			flush()
			b.WriteString(term.CodeString(s[i:j]))
			i = j
			continue
		}

		// Number (tolerates hex/exponent/decimals and a trailing suffix).
		if c >= '0' && c <= '9' {
			j := i
			for j < len(s) && (isIdentPart(s[j]) || s[j] == '.') {
				j++
			}
			flush()
			b.WriteString(term.CodeNumber(s[i:j]))
			i = j
			continue
		}

		// Identifier or keyword.
		if isIdentStart(c) {
			j := i
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}
			word := s[i:j]
			if h.spec.keywords[word] {
				flush()
				b.WriteString(term.CodeKeyword(word))
			} else {
				plain.WriteString(word)
			}
			i = j
			continue
		}

		plain.WriteByte(c)
		i++
	}
	flush()
	return b.String()
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentPart(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}
