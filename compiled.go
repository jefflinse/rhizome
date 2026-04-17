package rhizome

import (
	"context"
	"encoding"
	"fmt"
	"slices"
)

// DefaultMaxNodeExecs is the default maximum number of times a single node can
// execute before Run returns ErrCycleLimit.
const DefaultMaxNodeExecs = 10

// CompileOption configures graph compilation.
type CompileOption func(*compileConfig)

type compileConfig struct {
	maxNodeExecs    int
	checkpointStore CheckpointStore
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
	threadID     string
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
	checkpointStore  CheckpointStore
	snapshot         func(ctx context.Context, threadID, nodeName string, state S) error
}

// nodeExecutor invokes a node function, threading the middleware chain
// around it. The chain is built once per Run and reused for every node.
type nodeExecutor[S any] func(ctx context.Context, name string, fn NodeFunc[S], state S) (S, error)

// Run executes the graph from the entry node until End is reached.
// Returns the final state on success, or the partial state and error on failure.
//
// If the graph was compiled with WithCheckpointing, WithThreadID is required
// and state is persisted to the configured CheckpointStore after each node.
func (cg *CompiledGraph[S]) Run(ctx context.Context, initial S, opts ...RunOption[S]) (S, error) {
	cfg := cg.buildRunConfig(opts)
	if cg.checkpointStore != nil && cfg.threadID == "" {
		return initial, ErrThreadIDRequired
	}

	current, err := cg.resolveNext(ctx, Start, initial)
	if err != nil {
		return initial, fmt.Errorf("rhizome: router %q: %w", Start, err)
	}

	return cg.execute(ctx, initial, current, cfg)
}

// Resume loads the latest checkpoint for threadID and continues execution
// from the node after the one that produced the checkpoint. The empty
// parameter is an uninitialized instance of S used as the target for
// unmarshaling the saved state — typically &YourState{} when S is a
// pointer type. The populated state is returned on success.
//
// Resume requires the graph to have been compiled with WithCheckpointing.
// The threadID passed here overrides any WithThreadID option provided.
func (cg *CompiledGraph[S]) Resume(ctx context.Context, threadID string, empty S, opts ...RunOption[S]) (S, error) {
	if cg.checkpointStore == nil {
		return empty, ErrCheckpointingDisabled
	}
	if threadID == "" {
		return empty, ErrThreadIDRequired
	}

	nodeName, data, err := cg.checkpointStore.Load(ctx, threadID)
	if err != nil {
		return empty, err
	}

	unm, ok := any(empty).(encoding.BinaryUnmarshaler)
	if !ok {
		return empty, ErrCheckpointRequiresSnapshotter
	}
	if err := unm.UnmarshalBinary(data); err != nil {
		return empty, fmt.Errorf("rhizome: unmarshal checkpoint at %q: %w", nodeName, err)
	}

	cfg := cg.buildRunConfig(opts)
	cfg.threadID = threadID

	current, err := cg.resolveNext(ctx, nodeName, empty)
	if err != nil {
		return empty, fmt.Errorf("rhizome: router %q: %w", nodeName, err)
	}

	return cg.execute(ctx, empty, current, cfg)
}

// buildRunConfig applies options and returns the resulting config.
// Cross-cutting validation (such as requiring a thread ID when checkpointing
// is enabled) is the caller's responsibility, because Run and Resume have
// different rules for how the thread ID is supplied.
func (cg *CompiledGraph[S]) buildRunConfig(opts []RunOption[S]) runConfig[S] {
	var cfg runConfig[S]
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// execute runs the main loop shared by Run and Resume. state is the current
// state and current is the first node to execute (may be End, in which case
// the loop exits immediately).
func (cg *CompiledGraph[S]) execute(ctx context.Context, state S, current string, cfg runConfig[S]) (S, error) {
	maxExecs := cg.maxNodeExecs
	if cfg.maxNodeExecs != nil {
		maxExecs = *cfg.maxNodeExecs
	}

	exec := buildExecutor(cfg.middleware)
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
		var err error
		state, err = exec(ctx, nodeName, fn, state)
		if err != nil {
			return state, fmt.Errorf("rhizome: node %q: %w", nodeName, err)
		}

		if err := cg.snapshot(ctx, cfg.threadID, nodeName, state); err != nil {
			return state, fmt.Errorf("rhizome: checkpoint %q: %w", nodeName, err)
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
