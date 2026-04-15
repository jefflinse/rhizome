package daggo

import (
	"context"
	"fmt"
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
// before Run returns ErrCycleLimit.
func WithMaxNodeExecs(n int) CompileOption {
	return func(c *compileConfig) {
		c.maxNodeExecs = n
	}
}

// CompiledGraph is an immutable, validated graph ready for execution.
type CompiledGraph[S any] struct {
	nodes            map[string]NodeFunc[S]
	edges            map[string]string
	conditionalEdges map[string]func(S) string
	maxNodeExecs     int
}

// Run executes the graph from the entry node until End is reached.
// Returns the final state on success, or the partial state and error on failure.
func (cg *CompiledGraph[S]) Run(ctx context.Context, initial S) (S, error) {
	current := cg.resolveNext(Start, initial)
	state := initial
	execCounts := make(map[string]int, len(cg.nodes))

	for current != End {
		if err := ctx.Err(); err != nil {
			return state, err
		}

		if execCounts[current] >= cg.maxNodeExecs {
			return state, fmt.Errorf("%w: %q executed %d times", ErrCycleLimit, current, cg.maxNodeExecs)
		}
		execCounts[current]++

		fn, ok := cg.nodes[current]
		if !ok {
			return state, fmt.Errorf("%w: %q", ErrInvalidRoute, current)
		}

		var err error
		state, err = fn(ctx, state)
		if err != nil {
			return state, err
		}

		current = cg.resolveNext(current, state)
	}

	return state, nil
}

// resolveNext determines the next node after the given node.
// Priority: conditional edge > static edge > implicit End.
func (cg *CompiledGraph[S]) resolveNext(from string, state S) string {
	if router, ok := cg.conditionalEdges[from]; ok {
		return router(state)
	}
	if to, ok := cg.edges[from]; ok {
		return to
	}
	return End
}
