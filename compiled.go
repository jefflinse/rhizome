package daggo

import (
	"context"
	"fmt"
)

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
	state := initial
	current := cg.resolveNext(Start, state)
	execCounts := make(map[string]int)

	for current != End {
		if err := ctx.Err(); err != nil {
			return state, err
		}

		fn, ok := cg.nodes[current]
		if !ok {
			return state, fmt.Errorf("%w: %q", ErrInvalidRoute, current)
		}

		if execCounts[current] >= cg.maxNodeExecs {
			return state, fmt.Errorf("%w: %q executed %d times", ErrCycleLimit, current, cg.maxNodeExecs)
		}
		execCounts[current]++

		var err error
		state, err = fn(ctx, state)
		if err != nil {
			return state, err
		}

		current = cg.resolveNext(current, state)
	}

	return state, nil
}

func (cg *CompiledGraph[S]) resolveNext(current string, state S) string {
	if router, ok := cg.conditionalEdges[current]; ok {
		return router(state)
	}
	if target, ok := cg.edges[current]; ok {
		return target
	}
	return End
}
