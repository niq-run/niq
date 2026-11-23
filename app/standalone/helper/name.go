package helper

import "strings"

// SafeName turns an arbitrary string into a safe path segment: the name of a
// directory or a file, on any platform. Letters, digits, '-', '_' and '.' are
// kept, a leading slash is dropped, and everything else — including '/', a
// space, a colon or a control character — becomes '_'.
//
// It is the single rule behind every id that becomes a directory name: project
// ids (store.ProjectDir), worker state dirs and log dirs, and the name an
// imported template is filed under. Those callers must agree, or the same id
// resolves to two different directories; `store.SanitizeID` and
// `worker.SanitizeID` are thin aliases for this function.
//
// For an id that is already valid — letters, digits, '-', '_', '.' — it returns
// the id unchanged, which is what makes it safe to apply to persisted names.
func SafeName(s string) string {
	s = strings.TrimPrefix(s, "/")
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
