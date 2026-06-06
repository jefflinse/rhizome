package rhizome

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// concTracker records the peak number of branches running at once.
type concTracker struct {
	mu     sync.Mutex
	active int
	peak   int
}

func (c *concTracker) enter() {
	c.mu.Lock()
	c.active++
	if c.active > c.peak {
		c.peak = c.active
	}
	c.mu.Unlock()
}

func (c *concTracker) leave() {
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
}

func (c *concTracker) peakValue() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

func TestFanout_RunAll_SumOfDoubles(t *testing.T) {
	node := Fanout(
		func(_ context.Context, n int) ([]int, error) {
			xs := make([]int, n)
			for i := range xs {
				xs[i] = i + 1
			}
			return xs, nil
		},
		func(_ context.Context, x int) (int, error) { return x * 2, nil },
		func(_ context.Context, _ int, rs []BranchResult[int]) (int, error) {
			sum := 0
			for _, r := range rs {
				if r.Err != nil {
					return 0, r.Err
				}
				sum += r.Value
			}
			return sum, nil
		},
	)

	got, err := node(context.Background(), 4) // 1,2,3,4 -> 2,4,6,8 -> 20
	must(t, err)
	if got != 20 {
		t.Fatalf("got %d, want 20", got)
	}
}

func TestFanout_PreservesSplitOrder(t *testing.T) {
	const n = 6
	node := Fanout(
		func(_ context.Context, _ []int) ([]int, error) {
			xs := make([]int, n)
			for i := range xs {
				xs[i] = i
			}
			return xs, nil
		},
		func(_ context.Context, i int) (int, error) {
			// Earlier indices sleep longer, so completion order is the reverse
			// of split order. Results must still come back in split order.
			time.Sleep(time.Duration(n-i) * 2 * time.Millisecond)
			return i * 10, nil
		},
		func(_ context.Context, _ []int, rs []BranchResult[int]) ([]int, error) {
			out := make([]int, len(rs))
			for j, r := range rs {
				if r.Index != j {
					return nil, fmt.Errorf("result at position %d has Index %d", j, r.Index)
				}
				out[j] = r.Value
			}
			return out, nil
		},
	)

	got, err := node(context.Background(), nil)
	must(t, err)
	for j := 0; j < n; j++ {
		if got[j] != j*10 {
			t.Fatalf("out[%d] = %d, want %d", j, got[j], j*10)
		}
	}
}

func TestFanout_ConcurrencyBounded(t *testing.T) {
	var tr concTracker
	node := Fanout(
		func(_ context.Context, n int) ([]int, error) {
			return make([]int, n), nil
		},
		func(_ context.Context, _ int) (int, error) {
			tr.enter()
			defer tr.leave()
			time.Sleep(10 * time.Millisecond)
			return 0, nil
		},
		func(_ context.Context, _ int, _ []BranchResult[int]) (int, error) {
			return tr.peakValue(), nil
		},
		WithFanoutConcurrency(3),
	)

	peak, err := node(context.Background(), 8)
	must(t, err)
	if peak == 0 {
		t.Fatal("no branches ran")
	}
	if peak > 3 {
		t.Fatalf("peak concurrency %d exceeded bound of 3", peak)
	}
}

func TestFanout_ConcurrencySerialWhenOne(t *testing.T) {
	var tr concTracker
	node := Fanout(
		func(_ context.Context, n int) ([]int, error) { return make([]int, n), nil },
		func(_ context.Context, _ int) (int, error) {
			tr.enter()
			defer tr.leave()
			time.Sleep(2 * time.Millisecond)
			return 0, nil
		},
		func(_ context.Context, _ int, _ []BranchResult[int]) (int, error) {
			return tr.peakValue(), nil
		},
		WithFanoutConcurrency(1),
	)

	peak, err := node(context.Background(), 5)
	must(t, err)
	if peak != 1 {
		t.Fatalf("peak concurrency %d, want exactly 1 (serial)", peak)
	}
}

func TestFanout_Unbounded_RunsInParallel(t *testing.T) {
	var tr concTracker
	node := Fanout(
		func(_ context.Context, n int) ([]int, error) { return make([]int, n), nil },
		func(_ context.Context, _ int) (int, error) {
			tr.enter()
			defer tr.leave()
			time.Sleep(10 * time.Millisecond)
			return 0, nil
		},
		func(_ context.Context, _ int, _ []BranchResult[int]) (int, error) {
			return tr.peakValue(), nil
		},
	)

	peak, err := node(context.Background(), 6)
	must(t, err)
	if peak < 2 {
		t.Fatalf("peak concurrency %d, expected genuine parallelism (>= 2)", peak)
	}
}

func TestFanout_RunAll_CollectsErrors(t *testing.T) {
	errBoom := errors.New("boom")
	var captured []BranchResult[int]

	node := Fanout(
		func(_ context.Context, _ int) ([]int, error) { return []int{0, 1, 2, 3}, nil },
		func(_ context.Context, i int) (int, error) {
			if i%2 == 1 {
				return 0, errBoom
			}
			return i, nil
		},
		func(_ context.Context, _ int, rs []BranchResult[int]) (int, error) {
			captured = rs
			return 0, nil
		},
	)

	_, err := node(context.Background(), 0)
	must(t, err)
	if len(captured) != 4 {
		t.Fatalf("reduce saw %d results, want 4", len(captured))
	}
	for i, r := range captured {
		if i%2 == 1 {
			if !errors.Is(r.Err, errBoom) {
				t.Errorf("branch %d: err = %v, want errBoom", i, r.Err)
			}
		} else {
			if r.Err != nil {
				t.Errorf("branch %d: unexpected err %v", i, r.Err)
			}
			if r.Value != i {
				t.Errorf("branch %d: value = %d, want %d", i, r.Value, i)
			}
		}
	}
}

func TestFanout_CancelOnError_ReturnsErrWithoutReduce(t *testing.T) {
	errBoom := errors.New("boom")
	var reduceCalled atomic.Bool

	node := Fanout(
		func(_ context.Context, _ int) ([]int, error) { return []int{0, 1, 2, 3}, nil },
		func(ctx context.Context, i int) (int, error) {
			if i == 0 {
				return 0, errBoom
			}
			// Siblings block until the fail-fast cancellation arrives.
			<-ctx.Done()
			return 0, ctx.Err()
		},
		func(_ context.Context, _ int, _ []BranchResult[int]) (int, error) {
			reduceCalled.Store(true)
			return 0, nil
		},
		WithFanoutCancelOnError(),
	)

	_, err := node(context.Background(), 0)
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
	if reduceCalled.Load() {
		t.Fatal("reduce was called in fail-fast mode")
	}
}

func TestFanout_EmptySplit_CallsReduceWithNil(t *testing.T) {
	called := false
	var args []BranchResult[int]

	node := Fanout(
		func(_ context.Context, _ int) ([]int, error) { return nil, nil },
		func(_ context.Context, _ int) (int, error) {
			t.Error("branch ran for an empty split")
			return 0, nil
		},
		func(_ context.Context, _ int, rs []BranchResult[int]) (int, error) {
			called = true
			args = rs
			return 42, nil
		},
	)

	got, err := node(context.Background(), 0)
	must(t, err)
	if !called {
		t.Fatal("reduce was not called for an empty split")
	}
	if args != nil {
		t.Fatalf("reduce args = %v, want nil", args)
	}
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func TestFanout_SplitError_Propagates(t *testing.T) {
	errSplit := errors.New("split failed")

	node := Fanout(
		func(_ context.Context, _ int) ([]int, error) { return nil, errSplit },
		func(_ context.Context, _ int) (int, error) {
			t.Error("branch ran after split error")
			return 0, nil
		},
		func(_ context.Context, _ int, _ []BranchResult[int]) (int, error) {
			t.Error("reduce ran after split error")
			return 0, nil
		},
	)

	_, err := node(context.Background(), 0)
	if !errors.Is(err, errSplit) {
		t.Fatalf("err = %v, want errSplit", err)
	}
}

func TestFanout_CallerCancel_ReturnsCtxErrWithoutReduce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reduceCalled atomic.Bool

	node := Fanout(
		func(_ context.Context, _ int) ([]int, error) { return []int{0, 1, 2, 3}, nil },
		func(ctx context.Context, i int) (int, error) {
			if i == 0 {
				cancel() // caller aborts mid-flight
			}
			<-ctx.Done()
			return 0, ctx.Err()
		},
		func(_ context.Context, _ int, _ []BranchResult[int]) (int, error) {
			reduceCalled.Store(true)
			return 0, nil
		},
	)

	_, err := node(ctx, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if reduceCalled.Load() {
		t.Fatal("reduce was called after caller cancellation")
	}
}

func TestFanout_SubgraphAsBranch(t *testing.T) {
	// Inner graph: doubles its integer state.
	inner := New[int]()
	must(t, inner.AddNode("double", func(_ context.Context, x int) (int, error) { return x * 2, nil }))
	must(t, inner.AddEdge(Start, "double"))
	must(t, inner.AddEdge("double", End))
	sub, err := inner.Compile()
	must(t, err)

	node := Fanout(
		func(_ context.Context, base int) ([]int, error) {
			return []int{base, base + 1, base + 2}, nil
		},
		func(ctx context.Context, x int) (int, error) {
			return sub.Run(ctx, x) // a compiled subgraph as the branch
		},
		func(_ context.Context, base int, rs []BranchResult[int]) (int, error) {
			sum := base
			for _, r := range rs {
				if r.Err != nil {
					return 0, r.Err
				}
				sum += r.Value
			}
			return sum, nil
		},
		WithFanoutConcurrency(2),
	)

	// base=10 -> items 10,11,12 -> doubled 20,22,24 -> 10+20+22+24 = 76
	got, err := node(context.Background(), 10)
	must(t, err)
	if got != 76 {
		t.Fatalf("got %d, want 76", got)
	}
}

func TestFanout_BranchPanic_RecoveredAsError(t *testing.T) {
	var captured []BranchResult[int]

	node := Fanout(
		func(_ context.Context, _ int) ([]int, error) { return []int{0, 1, 2}, nil },
		func(_ context.Context, i int) (int, error) {
			if i == 1 {
				panic("branch blew up")
			}
			return i, nil
		},
		func(_ context.Context, _ int, rs []BranchResult[int]) (int, error) {
			captured = rs
			return 0, nil
		},
	)

	_, err := node(context.Background(), 0)
	must(t, err)
	if !errors.Is(captured[1].Err, ErrNodePanic) {
		t.Fatalf("branch 1 err = %v, want wrapping ErrNodePanic", captured[1].Err)
	}
	if captured[0].Err != nil || captured[0].Value != 0 {
		t.Errorf("branch 0 result = %+v, want value 0 no error", captured[0])
	}
	if captured[2].Err != nil || captured[2].Value != 2 {
		t.Errorf("branch 2 result = %+v, want value 2 no error", captured[2])
	}
}

func TestFanout_BranchPanic_FailFast(t *testing.T) {
	var reduceCalled atomic.Bool

	node := Fanout(
		func(_ context.Context, _ int) ([]int, error) { return []int{0, 1, 2, 3}, nil },
		func(ctx context.Context, i int) (int, error) {
			if i == 1 {
				panic("branch blew up")
			}
			<-ctx.Done()
			return 0, ctx.Err()
		},
		func(_ context.Context, _ int, _ []BranchResult[int]) (int, error) {
			reduceCalled.Store(true)
			return 0, nil
		},
		WithFanoutCancelOnError(),
	)

	_, err := node(context.Background(), 0)
	if !errors.Is(err, ErrNodePanic) {
		t.Fatalf("err = %v, want wrapping ErrNodePanic", err)
	}
	if reduceCalled.Load() {
		t.Fatal("reduce was called after a fail-fast panic")
	}
}
