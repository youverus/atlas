package tool

import (
	"sort"

	"pi-go/internal/wire"
)

// Registry holds the executable tools for a session, preserving declaration
// order for the model.
type Registry struct {
	order  []string
	byName map[string]Tool
}

// NewRegistry builds a registry. Later duplicates replace earlier ones.
func NewRegistry(tools ...Tool) *Registry {
	registry := &Registry{byName: map[string]Tool{}}
	for _, t := range tools {
		registry.Add(t)
	}
	return registry
}

// Add registers or replaces a tool.
func (r *Registry) Add(t Tool) {
	name := t.Name()
	if _, exists := r.byName[name]; !exists {
		r.order = append(r.order, name)
	}
	r.byName[name] = t
}

// Replace makes the registry hold exactly these tools, in this order.
//
// It is what a set that changes at runtime needs, which Add cannot express: a
// tool that disappeared has to stop being callable, and leaving it registered
// would let the model call something the server no longer has. The whole set is
// replaced rather than compared by name because a tool can be re-declared under
// its old name with a new schema, and a difference in kind that no name shows is
// exactly the one worth picking up.
func (r *Registry) Replace(tools ...Tool) {
	r.order = make([]string, 0, len(tools))
	r.byName = make(map[string]Tool, len(tools))
	for _, t := range tools {
		r.Add(t)
	}
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Tools returns the registered tools in declaration order.
func (r *Registry) Tools() []Tool {
	tools := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.byName[name]; ok {
			tools = append(tools, t)
		}
	}
	return tools
}

// Declarations returns the wire declarations sent to the model.
func (r *Registry) Declarations() []wire.Tool {
	tools := r.Tools()
	declarations := make([]wire.Tool, 0, len(tools))
	for _, t := range tools {
		declarations = append(declarations, Declaration(t))
	}
	return declarations
}

// Names returns the registered names, sorted. Useful for diagnostics.
func (r *Registry) Names() []string {
	names := append([]string(nil), r.order...)
	sort.Strings(names)
	return names
}
