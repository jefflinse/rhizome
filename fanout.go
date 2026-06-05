package rhizome

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
)

// BranchResult is one branch's outcome from a Fanout. Results are passed to the
// reduce function in split order — Index equals the position of the branch's
// input in the slice returned by split — so reduce can align each result with
// its input and decide what "success" means.
//
// When Err is non-nil, Value is the zero value of R.
type BranchResult[R any] struct {
	Index int
	Value R
	Err   error
}

// FanoutOption configures a Fanout node.
type FanoutOption func(*fanoutConfig)

type fanoutConfig struct {
	concurrency   int
	cancelOnError bool
}

// WithFanoutConcurrency bounds how many branches run concurrently within a
// single Fanout. A value <= 0 means unbounded — every branch launches at once.
//
// This bound is LOCAL to one Fanout: each Fanout has its own limiter, so
// nesting Fanouts can never deadlock on it. It is goroutine and memory hygiene,
// not a global resource ceiling. To cap an expensive shared resource (for
// example, concurrent network or model calls) across an entire execution, gate
// that resource at the leaf operation inside branch, not here — a single
// limiter shared across nested fanouts to bound branch goroutines would
// deadlock, because outer branches block waiting on inner branches that can
// never acquire a slot.
func WithFanoutConcurrency(n int) FanoutOption {
	return func(c *fanoutConfig) { c.concurrency = n }
}

// WithFanoutCancelOnError switches a Fanout to fail-fast: the first branch that
// returns an error (or panics) cancels the context passed to the other branches
// and that error is returned from the node directly, without calling reduce.
//
// Without this option (the default), every branch runs to completion and reduce
// is always called with the full set of BranchResults — including any errors —
// so reduce can implement quorum, voting, or best-of-N over partial success.
func WithFanoutCancelOnError() FanoutOption {
	return func(c *fanoutConfig) { c.cancelOnError = true }
}

// Fanout returns a NodeFunc[S] that runs work concurrently and folds the
// results back into the state. From the enclosing graph's perspective it is an
// ordinary single node.
//
// On each execution it:
//
//  1. calls split to derive a slice of independent branch inputs from the
//     current state;
//  2. runs branch over each input concurrently, bounded by
//     WithFanoutConcurrency;
//  3. calls reduce with the original (pre-split) state and every branch's
//     result, in split order, and returns reduce's output as the node's state.
//
// split MUST return independent values: two branches sharing mutable state
// (for example, the same pointer) is a data race Fanout cannot prevent. A
// common pattern is to deep-copy the state once per branch.
//
// branch is an ordinary func, not a NodeFunc, so its input and output types may
// differ. To use a compiled subgraph as a branch, wrap its Run in a closure —
// that closure is also where you attach per-branch RunOptions:
//
//	branch := func(ctx context.Context, s *State) (*State, error) {
//		return sub.Run(ctx, s, rhizome.WithMiddleware(rhizome.Retry[*State]()))
//	}
//
// Each subgraph branch runs with its own independent execution state (cycle
// counters, middleware), so nesting fanouts and graphs composes safely.
//
// Error handling depends on WithFanoutCancelOnError; see that option. A panic
// in a branch is recovered and surfaced as that branch's error wrapping
// ErrNodePanic, so one bad branch cannot crash the process. If split returns an
// error the node fails immediately and neither branch nor reduce runs. If the
// caller's context is cancelled the node returns that error without calling
// reduce. An empty slice from split calls reduce with a nil result slice.
func Fanout[S, B, R any](
	split func(context.Context, S) ([]B, error),
	branch func(context.Context, B) (R, error),
	reduce func(context.Context, S, []BranchResult[R]) (S, error),
	opts ...FanoutOption,
) NodeFunc[S] {
	var cfg fanoutConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(ctx context.Context, state S) (S, error) {
		items, err := split(ctx, state)
		if err != nil {
			return state, err
		}
		if len(items) == 0 {
			return reduce(ctx, state, nil)
		}

		results := make([]BranchResult[R], len(items))

		// runCtx is the context branches run under. In fail-fast mode it is
		// cancellable so the first error can stop siblings. In the default mode
		// it is the caller's context unchanged.
		runCtx := ctx
		var cancel context.CancelFunc
		if cfg.cancelOnError {
			runCtx, cancel = context.WithCancel(ctx)
			defer cancel()
		}

		// Optional local concurrency limiter: a buffered channel used as a
		// counting semaphore. nil means unbounded.
		var sem chan struct{}
		if cfg.concurrency > 0 {
			sem = make(chan struct{}, cfg.concurrency)
		}

		var (
			wg       sync.WaitGroup
			errOnce  sync.Once
			firstErr error
		)

		recordFirstErr := func(err error) {
			errOnce.Do(func() {
				firstErr = err
				if cancel != nil {
					cancel()
				}
			})
		}

		for i, item := range items {
			// Acquire a slot before spawning so we never hold more than
			// `concurrency` live branch goroutines. Abort acquisition if the
			// run context is already done (fail-fast cancel or caller cancel).
			if sem != nil {
				select {
				case sem <- struct{}{}:
				case <-runCtx.Done():
					results[i] = BranchResult[R]{Index: i, Err: runCtx.Err()}
					continue
				}
			}

			wg.Add(1)
			go func(i int, item B) {
				defer wg.Done()
				if sem != nil {
					defer func() { <-sem }()
				}
				defer func() {
					if r := recover(); r != nil {
						err := fmt.Errorf("%w: branch %d: %v\n%s", ErrNodePanic, i, r, debug.Stack())
						results[i] = BranchResult[R]{Index: i, Err: err}
						if cfg.cancelOnError {
							recordFirstErr(err)
						}
					}
				}()

				value, err := branch(runCtx, item)
				results[i] = BranchResult[R]{Index: i, Value: value, Err: err}
				if err != nil && cfg.cancelOnError {
					recordFirstErr(err)
				}
			}(i, item)
		}

		wg.Wait()

		if cfg.cancelOnError && firstErr != nil {
			return state, firstErr
		}
		// A cancelled caller context aborts the node rather than reducing over
		// partial or cancelled results — consistent with the sequential
		// executor, which checks ctx between nodes.
		if err := ctx.Err(); err != nil {
			return state, err
		}
		return reduce(ctx, state, results)
	}
}
