// Package tools owns the static catalogue of tools that the LLM can call.
// It replaces the dynamic `tools/list` + `tools/call` flow we used to drive
// through the MCP server. Each tool maps to one Clicksign REST endpoint
// (or, in the case of select_account, a local session mutation).
package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Tool is the descriptor + executor of a single LLM-facing function.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON schema (OpenAI's `parameters` field)
	Run         func(ctx context.Context, phone string, args map[string]any) (string, error)
}

// Runner is the dependency injected into the conversation layer. It mirrors
// the previous mcpclient.Manager contract minus the session lifecycle.
type Runner interface {
	List(ctx context.Context, phone string) ([]Tool, error)
	Call(ctx context.Context, phone, name string, args map[string]any) (string, error)
}

// ErrToolNotFound is returned by Call when the LLM asks for a tool we do
// not expose.
var ErrToolNotFound = errors.New("tools: tool not found")

// StaticRunner is the trivial Runner backed by an in-memory slice.
type StaticRunner struct {
	mu      sync.RWMutex
	catalog []Tool
	byName  map[string]Tool
}

func NewStaticRunner(catalog []Tool) *StaticRunner {
	byName := make(map[string]Tool, len(catalog))
	for _, t := range catalog {
		byName[t.Name] = t
	}
	return &StaticRunner{catalog: catalog, byName: byName}
}

func (s *StaticRunner) List(_ context.Context, _ string) ([]Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, len(s.catalog))
	copy(out, s.catalog)
	return out, nil
}

func (s *StaticRunner) Call(ctx context.Context, phone, name string, args map[string]any) (string, error) {
	s.mu.RLock()
	t, ok := s.byName[name]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrToolNotFound, name)
	}
	return t.Run(ctx, phone, args)
}
