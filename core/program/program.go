// Package program defines the types for niq's Program abstraction — the
// "source code" that Reason Workers load and that compiles into tool calls.
//
// A Program has two orthogonal dimensions:
//   - ContentType: what the program says (instruction / playbook)
//   - FormType:   how the program is written (prompt / script)
//
// Programs have a three-layer structure:
//
//	Program → EntryContent (always loaded first)
//	        → Contents (loaded progressively via program.load)
package program

// ContentType describes what a Program says — its role in guiding the
// worker's behaviour.
type ContentType string

const (
	// ContentTypeInstruction is a binding constraint or objective rule.
	// It describes what must (or must not) happen — guardrails, policies,
	// and invariants that shape the worker's decisions.
	ContentTypeInstruction ContentType = "instruction"

	// ContentTypePlaybook is a procedural how-to.
	// It describes a sequence of steps to follow for a specific scenario.
	ContentTypePlaybook ContentType = "playbook"
)

// FormType describes the language a Program is written in.
type FormType string

const (
	// FormTypePrompt is natural language, "compiled" by the LLM at inference time.
	FormTypePrompt FormType = "prompt"

	// FormTypeScript is a formal DSL, interpreted by the Program Worker into tool calls.
	FormTypeScript FormType = "script"
)

// Meta identifies a Program and describes what it does.
// It is used for search, filtering, and display.
//
// Locked programs cannot be modified or deleted via meta-extensions
// (e.g. the register, edit, delete tools). They are
// defined by the system operator and form the immutable core of the
// Worker's identity and behaviour.
type Meta struct {
	Name        string      `json:"name"`
	ContentType ContentType `json:"content_type"`
	Description string      `json:"description,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Locked      bool        `json:"locked,omitempty"`
}

// Program is a loadable logical unit — niq's equivalent of a "skill".
// It carries domain knowledge (prompt) or executable logic (script), and is
// the building block of a Worker's capability.
//
// EntryContent is always loaded first. It describes what the program does
// and what sub-contents are available. Contents are loaded progressively
// via program.load at runtime.
type Program struct {
	Meta
	EntryContent ProgramContent   `json:"entry_content"`
	Contents     []ProgramContent `json:"contents,omitempty"`
}

// ProgramContent is a single content item within a Program.
// It is the unit of progressive loading: EntryContent is always loaded,
// Contents are loaded on demand via program.load.
type ProgramContent struct {
	FormType FormType `json:"form_type"`
	Path     string   `json:"path,omitempty"` // relative path within the program directory
	Content  string   `json:"content,omitempty"`
}
