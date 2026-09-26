package llm

// Context describes the full input for an LLM call: system prompt, message
// history, tools, and generation parameters.
type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []ToolDef
	Model        string
	Temperature  *float32
	MaxTokens    *int
	TopP         *float32
	Stop         []string

	// ReasoningEffort controls reasoning depth for o-series models: "low", "medium", "high".
	ReasoningEffort *string

	// JSONMode enables structured JSON output where supported.
	JSONMode bool

	// Seed provides deterministic sampling where supported.
	Seed *int
}

// Clone returns a deep copy of the Context.
func (c *Context) Clone() *Context {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Messages = make([]Message, len(c.Messages))
	copy(cp.Messages, c.Messages)
	cp.Tools = make([]ToolDef, len(c.Tools))
	copy(cp.Tools, c.Tools)
	if c.Stop != nil {
		cp.Stop = make([]string, len(c.Stop))
		copy(cp.Stop, c.Stop)
	}
	return &cp
}

// SetTemperature is a convenience wrapper for setting a float32 pointer.
func (c *Context) SetTemperature(t float32) *Context {
	c.Temperature = &t
	return c
}

// SetMaxTokens is a convenience wrapper for setting an int pointer.
func (c *Context) SetMaxTokens(n int) *Context {
	c.MaxTokens = &n
	return c
}

// SetTopP is a convenience wrapper for setting a float32 pointer.
func (c *Context) SetTopP(p float32) *Context {
	c.TopP = &p
	return c
}

// SetSeed is a convenience wrapper for setting an int pointer.
func (c *Context) SetSeed(seed int) *Context {
	c.Seed = &seed
	return c
}

// SetReasoningEffort is a convenience wrapper for reasoning effort.
func (c *Context) SetReasoningEffort(effort string) *Context {
	c.ReasoningEffort = &effort
	return c
}
