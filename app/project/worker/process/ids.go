package process

import "strings"

// sanitizeID makes a worker id safe as a directory name. It mirrors
// worker.SanitizeID in the parent package: the supervisor writes logs and the
// child's NIQ_STATE_DIR under the same <root>/<id> layout the declaration store
// cleans up, so the two rules must agree.
//
// It is duplicated rather than imported because this package must not depend on
// the declaration model, while the parent package already depends on this one.
func sanitizeID(path string) string {
	s := strings.TrimPrefix(path, "/")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
