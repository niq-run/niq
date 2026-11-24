package process

import "github.com/niq-run/niq/app/standalone/helper"

// sanitizeID makes a worker id safe as a directory name. The supervisor writes
// logs and the child's NIQ_STATE_DIR under the same <root>/<id> layout the rest
// of the form uses, so this delegates to the one rule (helper.SafeName) instead
// of copying it: one id must not resolve to two different directories. It is
// still local so this package never imports the declaration model above it.
func sanitizeID(id string) string { return helper.SafeName(id) }
