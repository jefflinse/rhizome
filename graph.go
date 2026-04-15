// Package daggo provides a lightweight, generic graph execution engine.
package daggo

import (
	"context"
	"fmt"
	"maps"
)

const (
	Start = "__start__" // Virtual entry point; use in AddEdge to set the entrypoint.
	End   = "__end__"   // Virtual terminal; use in AddEdge or return from a router to end execution.
)

// NodeFunc is a function that transforms state.
type NodeFunc[S any] func(ctx context.Context, state S) (S, error)

// Graph is a mutable builder for defining nodes and edges.
// Call Compile to validate and produce an executable CompiledGraph.
type Graph[S any] struct {
	nodes            map[string]NodeFunc[S]
	edges            map[string]string         // from -> to (static)
	conditionalEdges map[string]func(S) string // from -> router
}

// New creates an empty graph.
func New[S any]() *Graph[S] {
	return &Graph[S]{
		nodes:            make(map[string]NodeFunc[S]),
		edges:            make(map[string]string),
		conditionalEdges: make(map[string]func(S) string),
	}
}

// AddNode registers a named node with the given function.
func (g *Graph[S]) AddNode(name string, fn NodeFunc[S]) error {
	if name == Start || name == End {
		return fmt.Errorf("%w: %q", ErrReservedName, name)
	}
	if fn == nil {
		return fmt.Errorf("daggo: nil node function for %q", name)
	}
	if _, exists := g.nodes[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateNode, name)
	}
	g.nodes[name] = fn
	return nil
}

// AddEdge adds a static edge from one node to another.
// Use Start and End constants for entry and exit points.
func (g *Graph[S]) AddEdge(from, to string) error {
	if from == End {
		return fmt.Errorf("%w: cannot add edge from End", ErrReservedName)
	}
	if to == Start {
		return fmt.Errorf("%w: cannot add edge to Start", ErrReservedName)
	}
	if _, exists := g.conditionalEdges[from]; exists {
		return fmt.Errorf("%w: %q already has a conditional edge", ErrConflictingEdge, from)
	}
	if _, exists := g.edges[from]; exists {
		return fmt.Errorf("%w: from %q", ErrDuplicateEdge, from)
	}
	g.edges[from] = to
	return nil
}

// AddConditionalEdge adds a dynamic routing function for a node.
// The router receives the current state and returns the name of the
// next node to execute, or End to terminate.
func (g *Graph[S]) AddConditionalEdge(from string, router func(S) string) error {
	if from == End {
		return fmt.Errorf("%w: cannot add conditional edge from End", ErrReservedName)
	}
	if router == nil {
		return fmt.Errorf("daggo: nil router function for %q", from)
	}
	if _, exists := g.edges[from]; exists {
		return fmt.Errorf("%w: %q already has a static edge", ErrConflictingEdge, from)
	}
	if _, exists := g.conditionalEdges[from]; exists {
		return fmt.Errorf("%w: from %q", ErrDuplicateEdge, from)
	}
	g.conditionalEdges[from] = router
	return nil
}

// Compile validates the graph structure and returns an immutable, executable
// CompiledGraph. Validation checks:
//   - At least one edge from Start exists
//   - All static edge targets reference existing nodes or End
//   - Every node is reachable from Start
func (g *Graph[S]) Compile(opts ...CompileOption) (*CompiledGraph[S], error) {
	cfg := compileConfig{maxNodeExecs: DefaultMaxNodeExecs}
	for _, opt := range opts {
		opt(&cfg)
	}

	// 1. Entrypoint exists.
	_, hasStaticEntry := g.edges[Start]
	_, hasConditionalEntry := g.conditionalEdges[Start]
	if !hasStaticEntry && !hasConditionalEntry {
		return nil, ErrNoEntrypoint
	}

	// 2. All static edge targets exist.
	for from, to := range g.edges {
		if from != Start {
			if _, ok := g.nodes[from]; !ok {
				return nil, fmt.Errorf("%w: edge source %q", ErrNodeNotFound, from)
			}
		}
		if to != End {
			if _, ok := g.nodes[to]; !ok {
				return nil, fmt.Errorf("%w: edge target %q", ErrNodeNotFound, to)
			}
		}
	}

	for from := range g.conditionalEdges {
		if from != Start {
			if _, ok := g.nodes[from]; !ok {
				return nil, fmt.Errorf("%w: conditional edge source %q", ErrNodeNotFound, from)
			}
		}
	}

	// 3. Every node is reachable from Start.
	if err := g.checkReachability(); err != nil {
		return nil, err
	}

	// Build immutable copies.
	nodes := make(map[string]NodeFunc[S], len(g.nodes))
	maps.Copy(nodes, g.nodes)
	edges := make(map[string]string, len(g.edges))
	maps.Copy(edges, g.edges)
	condEdges := make(map[string]func(S) string, len(g.conditionalEdges))
	maps.Copy(condEdges, g.conditionalEdges)

	return &CompiledGraph[S]{
		nodes:            nodes,
		edges:            edges,
		conditionalEdges: condEdges,
		maxNodeExecs:     cfg.maxNodeExecs,
	}, nil
}

func (g *Graph[S]) checkReachability() error {
	reachable := make(map[string]bool)
	hasConditional := false

	var queue []string

	if target, ok := g.edges[Start]; ok && target != End {
		queue = append(queue, target)
	}
	if _, ok := g.conditionalEdges[Start]; ok {
		hasConditional = true
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if reachable[current] {
			continue
		}
		reachable[current] = true

		if _, ok := g.conditionalEdges[current]; ok {
			hasConditional = true
		}

		if target, ok := g.edges[current]; ok && target != End {
			if !reachable[target] {
				queue = append(queue, target)
			}
		}
	}

	if hasConditional {
		return nil
	}

	for name := range g.nodes {
		if !reachable[name] {
			return fmt.Errorf("%w: %q", ErrUnreachableNode, name)
		}
	}

	return nil
}
