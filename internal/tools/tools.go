// Package tools defines a registry of callable tools that an LLM may invoke.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Definition describes a tool to the model (name, purpose, JSON Schema args).
type Definition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Execute runs the tool with parsed arguments and returns text for the model.
type Execute func(ctx context.Context, args map[string]any) (string, error)

// Tool is a named, executable capability.
type Tool struct {
	Definition Definition
	Execute    Execute
}

// Registry holds the tools available to one run of clark.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds or replaces a tool by name.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Definition.Name] = t
}

// RegisterFunc adds a tool from plain fields, returning it for convenience.
func (r *Registry) RegisterFunc(name, description string, parameters map[string]any, fn Execute) Tool {
	t := Tool{
		Definition: Definition{Name: name, Description: description, Parameters: parameters},
		Execute:    fn,
	}
	r.Register(t)
	return t
}

// List returns all registered tools (order not guaranteed).
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// Has reports whether a tool with the given name is registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// Execute runs the named tool, decoding args JSON into the map.
func (r *Registry) Execute(ctx context.Context, name string, argsJSON []byte) (string, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}

	args := make(map[string]any)
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			// Some models emit arguments as a JSON string wrapping an object.
			var nested string
			if uerr := json.Unmarshal(argsJSON, &nested); uerr == nil {
				args = make(map[string]any)
				if uerr := json.Unmarshal([]byte(nested), &args); uerr != nil {
					return "", fmt.Errorf("invalid arguments for %q: %w", name, err)
				}
			} else {
				return "", fmt.Errorf("invalid arguments for %q: %w", name, err)
			}
		}
	}
	return t.Execute(ctx, args)
}

// StringArg returns a string argument by name.
func StringArg(args map[string]any, name string) string {
	if v, ok := args[name].(string); ok {
		return v
	}
	return ""
}

// BoolArg returns a boolean argument by name.
func BoolArg(args map[string]any, name string) bool {
	v, _ := args[name].(bool)
	return v
}

// IntArg returns an integer argument by name, falling back to def.
func IntArg(args map[string]any, name string, def int) int {
	switch v := args[name].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

type ctxKey int

const masterKey ctxKey = iota

// WithMaster marks a context as belonging to the Master (full privileges).
func WithMaster(ctx context.Context) context.Context {
	return context.WithValue(ctx, masterKey, true)
}

// IsMaster reports whether the context belongs to the Master.
func IsMaster(ctx context.Context) bool {
	v, _ := ctx.Value(masterKey).(bool)
	return v
}
