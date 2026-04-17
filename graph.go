// Package rhizome provides a lightweight, generic graph execution engine.
package rhizome

import (
	"context"
	"fmt"
	"maps"
	"slices"
)

const (
	Start = "__start__" // Virtual entry point; use in AddEdge to set the entrypoint.
	End   = "__end__"   // Virtual terminal; use in AddEdge or return from a router to end execution.
)

// NodeFunc is a function that transforms state.
type NodeFunc[S any] func(ctx context.Context, state S) (S, error)

// Router decides which node to execute next based on the current state.
// It must return one of the target names declared when the conditional
// edge was registered, or End.
type Router[S any] func(ctx context.Context, state S) (string, error)

// Middleware wraps node execution. It receives the node name, the current state,
// and the next function in the chain. Call next to continue execution.
type Middleware[S any] func(ctx context.Context, node string, state S, next NodeFunc[S]) (S, error)

// conditionalEdge bundles a router with the set of node names it is
// allowed to return. Declaring targets up front keeps the graph
// statically analyzable: reachability and target-existence checks run
// at Compile time instead of being deferred to the first bad run.
type conditionalEdge[S any] struct {
	router  Router[S]
	targets []string
}

// Graph is a mutable builder for defining nodes and edges.
// Call Compile to validate and produce an executable CompiledGraph.
type Graph[S any] struct {
	nodes            map[string]NodeFunc[S]
	edges            map[string]string             // from -> to (static)
	conditionalEdges map[string]conditionalEdge[S] // from -> router + declared targets
}

// New creates an empty graph.
func New[S any]() *Graph[S] {
	return &Graph[S]{
		nodes:            make(map[string]NodeFunc[S]),
		edges:            make(map[string]string),
		conditionalEdges: make(map[string]conditionalEdge[S]),
	}
}

// AddNode registers a named node with the given function.
func (g *Graph[S]) AddNode(name string, fn NodeFunc[S]) error {
	if name == Start || name == End {
		return fmt.Errorf("%w: %q", ErrReservedName, name)
	}
	if fn == nil {
		return fmt.Errorf("rhizome: nil node function for %q", name)
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
//
// Targets declares the complete set of node names the router may return
// (End is permitted). Declaring targets allows Compile to verify
// reachability and to catch typos; at runtime, a router returning a
// name not in targets yields ErrUndeclaredTarget.
func (g *Graph[S]) AddConditionalEdge(from string, router Router[S], targets ...string) error {
	if from == End {
		return fmt.Errorf("%w: cannot add conditional edge from End", ErrReservedName)
	}
	if router == nil {
		return fmt.Errorf("rhizome: nil router function for %q", from)
	}
	if len(targets) == 0 {
		return fmt.Errorf("%w: from %q", ErrNoTargets, from)
	}
	if slices.Contains(targets, Start) {
		return fmt.Errorf("%w: target cannot be Start", ErrReservedName)
	}
	if _, exists := g.edges[from]; exists {
		return fmt.Errorf("%w: %q already has a static edge", ErrConflictingEdge, from)
	}
	if _, exists := g.conditionalEdges[from]; exists {
		return fmt.Errorf("%w: from %q", ErrDuplicateEdge, from)
	}
	dedup := make([]string, 0, len(targets))
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		if seen[t] {
			continue
		}
		seen[t] = true
		dedup = append(dedup, t)
	}
	g.conditionalEdges[from] = conditionalEdge[S]{router: router, targets: dedup}
	return nil
}

// Compile validates the graph structure and returns an immutable, executable
// CompiledGraph. Validation checks:
//   - At least one edge from Start exists
//   - All edge targets (static and declared conditional) reference existing nodes or End
//   - Every declared conditional target references an existing node or End
//   - Every registered node has an outgoing edge (static or conditional)
//   - Every node is reachable from Start
func (g *Graph[S]) Compile(opts ...CompileOption) (*CompiledGraph[S], error) {
	cfg := compileConfig{maxNodeExecs: DefaultMaxNodeExecs}
	for _, opt := range opts {
		opt(&cfg)
	}

	_, hasStaticEntry := g.edges[Start]
	_, hasConditionalEntry := g.conditionalEdges[Start]
	if !hasStaticEntry && !hasConditionalEntry {
		return nil, ErrNoEntrypoint
	}

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

	for from, ce := range g.conditionalEdges {
		if from != Start {
			if _, ok := g.nodes[from]; !ok {
				return nil, fmt.Errorf("%w: conditional edge source %q", ErrNodeNotFound, from)
			}
		}
		for _, t := range ce.targets {
			if t == End {
				continue
			}
			if _, ok := g.nodes[t]; !ok {
				return nil, fmt.Errorf("%w: conditional edge target %q from %q", ErrNodeNotFound, t, from)
			}
		}
	}

	for name := range g.nodes {
		_, hasStatic := g.edges[name]
		_, hasConditional := g.conditionalEdges[name]
		if !hasStatic && !hasConditional {
			return nil, fmt.Errorf("%w: %q", ErrNoOutgoingEdge, name)
		}
	}

	if err := g.checkReachability(); err != nil {
		return nil, err
	}

	snapshot, err := buildSnapshotFn[S](cfg.checkpointStore)
	if err != nil {
		return nil, err
	}

	nodes := make(map[string]NodeFunc[S], len(g.nodes))
	maps.Copy(nodes, g.nodes)
	edges := make(map[string]string, len(g.edges))
	maps.Copy(edges, g.edges)
	condEdges := make(map[string]conditionalEdge[S], len(g.conditionalEdges))
	for k, v := range g.conditionalEdges {
		condEdges[k] = conditionalEdge[S]{
			router:  v.router,
			targets: slices.Clone(v.targets),
		}
	}

	return &CompiledGraph[S]{
		nodes:            nodes,
		edges:            edges,
		conditionalEdges: condEdges,
		maxNodeExecs:     cfg.maxNodeExecs,
		checkpointStore:  cfg.checkpointStore,
		snapshot:         snapshot,
	}, nil
}

// buildSnapshotFn returns the closure that persists state after each node.
// When store is nil it returns a no-op so the executor can invoke it
// unconditionally. When store is non-nil it verifies at compile time that
// S satisfies Snapshotter and returns a closure that marshals state and
// calls store.Save.
func buildSnapshotFn[S any](store CheckpointStore) (func(ctx context.Context, threadID, nodeName string, state S) error, error) {
	if store == nil {
		return func(context.Context, string, string, S) error { return nil }, nil
	}

	var zero S
	if _, ok := any(zero).(Snapshotter); !ok {
		return nil, fmt.Errorf("%w: %T", ErrCheckpointRequiresSnapshotter, zero)
	}

	return func(ctx context.Context, threadID, nodeName string, state S) error {
		data, err := any(state).(Snapshotter).MarshalBinary()
		if err != nil {
			return fmt.Errorf("marshal state at %q: %w", nodeName, err)
		}
		return store.Save(ctx, threadID, nodeName, data)
	}, nil
}

func (g *Graph[S]) checkReachability() error {
	reachable := make(map[string]bool)
	var queue []string

	enqueue := func(target string) {
		if target == End || reachable[target] {
			return
		}
		queue = append(queue, target)
	}

	if target, ok := g.edges[Start]; ok {
		enqueue(target)
	}
	if ce, ok := g.conditionalEdges[Start]; ok {
		for _, t := range ce.targets {
			enqueue(t)
		}
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if reachable[current] {
			continue
		}
		reachable[current] = true

		if target, ok := g.edges[current]; ok {
			enqueue(target)
		}
		if ce, ok := g.conditionalEdges[current]; ok {
			for _, t := range ce.targets {
				enqueue(t)
			}
		}
	}

	for name := range g.nodes {
		if !reachable[name] {
			return fmt.Errorf("%w: %q", ErrUnreachableNode, name)
		}
	}

	return nil
}
