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
// Meta carries yaml tags as well as json tags: it is the schema of a
// Program's YAML frontmatter, and yaml.v3 ignores json tags — without them it
// would derive keys by lowercasing the field name ("ContentType" →
// "contenttype"), which silently never matches "content_type" in the file and
// leaves ContentType empty.
type Meta struct {
	Name        string      `json:"name" yaml:"name"`
	ContentType ContentType `json:"content_type" yaml:"content_type,omitempty"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Tags        []string    `json:"tags,omitempty" yaml:"tags,omitempty"`
	Locked      bool        `json:"locked,omitempty" yaml:"locked,omitempty"`
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

	// Path is this Program's root addressing path. It is an abstract
	// address, not a filesystem location: only the Backend that stores the
	// Program knows where it actually lives, and that mapping never leaves
	// the Backend.
	//
	// It is always equal to Name. Sub-contents hang off it as
	// "{name}/path/to/file.type"; the entry content is the special case
	// whose Path is exactly "{name}".
	Path string `json:"path,omitempty"`

	EntryContent ProgramContent   `json:"entry_content"`
	Contents     []ProgramContent `json:"contents,omitempty"`
}

// ProgramContent is a single content item within a Program.
// It is the unit of progressive loading: EntryContent is always loaded,
// Contents are loaded on demand via program.load.
type ProgramContent struct {
	// Name is the Program this content belongs to. Every content carries it
	// so a content is self-describing once detached from its Program.
	Name string `json:"name,omitempty"`

	FormType FormType `json:"form_type"`

	// Path is the addressing path used to load this content:
	// "{program name}/path/to/file.type". The entry content is the special
	// case where it is just "{program name}".
	Path string `json:"path,omitempty"`

	Content string `json:"content,omitempty"`
}
