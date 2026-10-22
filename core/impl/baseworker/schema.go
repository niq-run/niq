package baseworker

// Schema constructors for the capability contract. Parameters describes the
// request, Response describes the reply; these keep the declarations readable
// and consistent across workers (a dozen tools would otherwise each hand-roll
// the same JSON Schema literals).
//
// They build documentation, not validation: nothing enforces a reply's shape.
// Declare the shape anyway — an agent (and especially a PTC script, which is
// written before anything runs) plans its parse from it.

// ObjectSchema builds an object schema from its properties.
func ObjectSchema(props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props}
}

// ArraySchema wraps an item schema into an array schema.
func ArraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

// StringSchema is a plain-text reply; desc documents its convention (e.g. the
// line layout of a command's output), and may be empty.
func StringSchema(desc string) map[string]any {
	if desc == "" {
		return map[string]any{"type": "string"}
	}
	return map[string]any{"type": "string", "description": desc}
}
