package rhizome

import (
	"context"
	"fmt"
	"slices"
)

// DefaultMaxNodeExecs is the default maximum number of times a single node can
// execute before Run returns ErrCycleLimit.
const DefaultMaxNodeExecs = 10

// CompileOption configures graph compilation.
type CompileOption func(*compileConfig)

type compileConfig struct {
	maxNodeExecs int
}

// WithMaxNodeExecs sets the maximum number of times a single node can execute
// before Run returns ErrCycleLimit. This is the default for every Run on the
// compiled graph; individual Run calls can override with WithRunMaxNodeExecs.
func WithMaxNodeExecs(n int) CompileOption {
	return func(c *compileConfig) {
		c.maxNodeExecs = n
	}
}

// RunOption configures a single Run invocation.
type RunOption[S any] func(*runConfig[S])

type runConfig[S any] struct {
	middleware   []Middleware[S]
	maxNodeExecs *int
}

// WithMiddleware adds middleware to a Run invocation. Middleware executes in
// the order provided: the first middleware is the outermost wrapper.
// Multiple calls to WithMiddleware append to the chain.
func WithMiddleware[S any](mw ...Middleware[S]) RunOption[S] {
	return func(cfg *runConfig[S]) {
		cfg.middleware = append(cfg.middleware, mw...)
	}
}

// WithRunMaxNodeExecs overrides the compile-time max node execution count
// for this Run invocation only.
func WithRunMaxNodeExecs[S any](n int) RunOption[S] {
	return func(cfg *runConfig[S]) {
		cfg.maxNodeExecs = &n
	}
}

// CompiledGraph is an immutable, validated graph ready for execution.
//
// CompiledGraph is safe for concurrent use. Run may be called from multiple
// goroutines simultaneously; each invocation uses only local state and never
// mutates the graph.
type CompiledGraph[S any] struct {
	nodes            map[string]NodeFunc[S]
	edges            map[string]string
	conditionalEdges map[string]conditionalEdge[S]
	maxNodeExecs     int
}

// nodeExecutor invokes a node function, threading the middleware chain
// around it. The chain is built once per Run and reused for every node.
type nodeExecutor[S any] func(ctx context.Context, name string, fn NodeFunc[S], state S) (S, error)

// Run executes the graph from the entry node until End is reached.
// Returns the final state on success, or the partial state and error on failure.
func (cg *CompiledGraph[S]) Run(ctx context.Context, initial S, opts ...RunOption[S]) (S, error) {
	var cfg runConfig[S]
	for _, opt := range opts {
		opt(&cfg)
	}

	maxExecs := cg.maxNodeExecs
	if cfg.maxNodeExecs != nil {
		maxExecs = *cfg.maxNodeExecs
	}

	execute := buildExecutor(cfg.middleware)

	state := initial
	current, err := cg.resolveNext(ctx, Start, state)
	if err != nil {
		return state, fmt.Errorf("rhizome: router %q: %w", Start, err)
	}

	execCounts := make(map[string]int, len(cg.nodes))

	for current != End {
		if err := ctx.Err(); err != nil {
			return state, err
		}

		if execCounts[current] >= maxExecs {
			return state, fmt.Errorf("%w: %q executed %d times", ErrCycleLimit, current, maxExecs)
		}
		execCounts[current]++

		fn, ok := cg.nodes[current]
		if !ok {
			return state, fmt.Errorf("%w: %q", ErrInvalidRoute, current)
		}

		nodeName := current
		state, err = execute(ctx, nodeName, fn, state)
		if err != nil {
			return state, fmt.Errorf("rhizome: node %q: %w", nodeName, err)
		}

		current, err = cg.resolveNext(ctx, nodeName, state)
		if err != nil {
			return state, fmt.Errorf("rhizome: router %q: %w", nodeName, err)
		}
	}

	return state, nil
}

// buildExecutor composes the middleware chain once and returns a function
// that invokes a node function with the full chain wrapped around it.
func buildExecutor[S any](middleware []Middleware[S]) nodeExecutor[S] {
	executor := nodeExecutor[S](func(ctx context.Context, _ string, fn NodeFunc[S], s S) (S, error) {
		return fn(ctx, s)
	})
	for i := len(middleware) - 1; i >= 0; i-- {
		mw := middleware[i]
		inner := executor
		executor = func(ctx context.Context, name string, fn NodeFunc[S], s S) (S, error) {
			next := func(ctx context.Context, s S) (S, error) {
				return inner(ctx, name, fn, s)
			}
			return mw(ctx, name, s, next)
		}
	}
	return executor
}

// resolveNext determines the next node after the given node.
// Priority: conditional edge > static edge. Compile guarantees every
// reachable non-End position has one or the other.
func (cg *CompiledGraph[S]) resolveNext(ctx context.Context, from string, state S) (string, error) {
	if ce, ok := cg.conditionalEdges[from]; ok {
		next, err := ce.router(ctx, state)
		if err != nil {
			return "", err
		}
		if next == Start {
			return "", fmt.Errorf("%w: returned Start", ErrInvalidRoute)
		}
		if !slices.Contains(ce.targets, next) {
			return "", fmt.Errorf("%w: %q not in declared targets %v", ErrUndeclaredTarget, next, ce.targets)
		}
		return next, nil
	}
	if to, ok := cg.edges[from]; ok {
		return to, nil
	}
	return "", fmt.Errorf("%w: %q", ErrNoOutgoingEdge, from)
}
