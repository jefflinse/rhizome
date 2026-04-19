package rhizome

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// singleNodeGraph wires Start -> "n" -> End with the given node function.
func singleNodeGraph(t *testing.T, fn NodeFunc[int]) *CompiledGraph[int] {
	t.Helper()
	g := New[int]()
	must(t, g.AddNode("n", fn))
	must(t, g.AddEdge(Start, "n"))
	must(t, g.AddEdge("n", End))
	return mustCompile(t, g)
}

func TestRecover_CatchesPanic(t *testing.T) {
	cg := singleNodeGraph(t, func(_ context.Context, _ int) (int, error) {
		panic("kaboom")
	})

	result, err := cg.Run(context.Background(), 7, WithMiddleware(Recover[int]()))
	if !errors.Is(err, ErrNodePanic) {
		t.Fatalf("err = %v, want wrapping ErrNodePanic", err)
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("err text does not include panic value: %v", err)
	}
	if !strings.Contains(err.Error(), `"n"`) {
		t.Errorf("err text does not include node name: %v", err)
	}
	if result != 7 {
		t.Errorf("result = %d, want 7 (input state)", result)
	}
}

func TestRecover_PassesThroughNormalErrors(t *testing.T) {
	sentinel := errors.New("boom")
	cg := singleNodeGraph(t, func(_ context.Context, n int) (int, error) {
		return n, sentinel
	})

	_, err := cg.Run(context.Background(), 0, WithMiddleware(Recover[int]()))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping sentinel", err)
	}
	if errors.Is(err, ErrNodePanic) {
		t.Errorf("non-panic error should not be wrapped as ErrNodePanic")
	}
}

func TestTimeout_TripsDeadline(t *testing.T) {
	cg := singleNodeGraph(t, func(ctx context.Context, n int) (int, error) {
		select {
		case <-time.After(time.Second):
			return n, nil
		case <-ctx.Done():
			return n, ctx.Err()
		}
	})

	_, err := cg.Run(context.Background(), 0, WithMiddleware(Timeout[int](10*time.Millisecond)))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want wrapping DeadlineExceeded", err)
	}
}

func TestTimeout_DoesNotAffectFastNodes(t *testing.T) {
	cg := singleNodeGraph(t, func(_ context.Context, n int) (int, error) { return n + 1, nil })

	result, err := cg.Run(context.Background(), 0, WithMiddleware(Timeout[int](time.Second)))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if result != 1 {
		t.Fatalf("result = %d, want 1", result)
	}
}

func TestTimeout_NonPositiveIsNoop(t *testing.T) {
	// A timeout of 0 must not trip — the node runs to completion.
	cg := singleNodeGraph(t, func(_ context.Context, n int) (int, error) {
		time.Sleep(20 * time.Millisecond)
		return n + 1, nil
	})

	result, err := cg.Run(context.Background(), 0, WithMiddleware(Timeout[int](0)))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if result != 1 {
		t.Fatalf("result = %d, want 1", result)
	}
}

func TestRetry_SucceedsAfterTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	cg := singleNodeGraph(t, func(_ context.Context, n int) (int, error) {
		if attempts.Add(1) <= 2 {
			return n, errors.New("transient")
		}
		return n + 1, nil
	})

	result, err := cg.Run(context.Background(), 0,
		WithMiddleware(Retry[int](WithBackoff(func(int) time.Duration { return 0 }))))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if result != 1 {
		t.Fatalf("result = %d, want 1", result)
	}
}

func TestRetry_ExhaustsAttemptsAndReturnsLastError(t *testing.T) {
	var attempts atomic.Int32
	sentinel := errors.New("persistent")
	cg := singleNodeGraph(t, func(_ context.Context, n int) (int, error) {
		attempts.Add(1)
		return n, sentinel
	})

	_, err := cg.Run(context.Background(), 0,
		WithMiddleware(Retry[int](
			WithMaxAttempts(4),
			WithBackoff(func(int) time.Duration { return 0 }),
		)))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping sentinel", err)
	}
	if got := attempts.Load(); got != 4 {
		t.Fatalf("attempts = %d, want 4", got)
	}
}

func TestRetry_ClassifierOptsOut(t *testing.T) {
	var attempts atomic.Int32
	fatal := errors.New("do-not-retry")
	cg := singleNodeGraph(t, func(_ context.Context, n int) (int, error) {
		attempts.Add(1)
		return n, fatal
	})

	_, err := cg.Run(context.Background(), 0,
		WithMiddleware(Retry[int](
			WithRetryIf(func(err error) bool { return !errors.Is(err, fatal) }),
			WithBackoff(func(int) time.Duration { return 0 }),
		)))
	if !errors.Is(err, fatal) {
		t.Fatalf("err = %v, want wrapping fatal", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (classifier should have stopped retries)", got)
	}
}

func TestRetry_DefaultSkipsContextCanceled(t *testing.T) {
	var attempts atomic.Int32
	// Node returns context.Canceled directly. Default classifier should not retry it.
	cg := singleNodeGraph(t, func(_ context.Context, n int) (int, error) {
		attempts.Add(1)
		return n, context.Canceled
	})

	_, err := cg.Run(context.Background(), 0, WithMiddleware(Retry[int]()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapping context.Canceled", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (context errors should not retry)", got)
	}
}

func TestRetry_SleepAbortsOnContextCancel(t *testing.T) {
	var attempts atomic.Int32
	cg := singleNodeGraph(t, func(_ context.Context, n int) (int, error) {
		attempts.Add(1)
		return n, errors.New("transient")
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first attempt fails, while the backoff sleep is pending.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := cg.Run(ctx, 0,
		WithMiddleware(Retry[int](
			WithMaxAttempts(10),
			WithBackoff(func(int) time.Duration { return time.Second }),
		)))
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapping context.Canceled", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed = %v, cancel should have aborted the backoff sleep", elapsed)
	}
	if got := attempts.Load(); got > 2 {
		t.Fatalf("attempts = %d, want at most 2", got)
	}
}

func TestRetry_ComposedWithTimeoutGivesEachAttemptItsOwnDeadline(t *testing.T) {
	// Timeout inside Retry: each attempt gets its own 50ms deadline.
	// The node sleeps 200ms, so every attempt trips its timeout; retries
	// must not retry the context.DeadlineExceeded (default classifier).
	var attempts atomic.Int32
	cg := singleNodeGraph(t, func(ctx context.Context, n int) (int, error) {
		attempts.Add(1)
		select {
		case <-time.After(200 * time.Millisecond):
			return n, nil
		case <-ctx.Done():
			return n, ctx.Err()
		}
	})

	_, err := cg.Run(context.Background(), 0, WithMiddleware(
		Retry[int](WithBackoff(func(int) time.Duration { return 0 })),
		Timeout[int](50*time.Millisecond),
	))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want wrapping DeadlineExceeded", err)
	}
	// Default classifier skips DeadlineExceeded, so only one attempt.
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (default classifier should skip DeadlineExceeded)", got)
	}
}

func TestDefaultRetryBackoff_Exponential(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 0},
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 800 * time.Millisecond},
	}
	for _, c := range cases {
		if got := DefaultRetryBackoff(c.attempt); got != c.want {
			t.Errorf("DefaultRetryBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

// Assert the package-level verbal contract about ErrNodePanic's format:
// the error text should mention the node name in quotes. If someone
// reorders the format string this test flags it.
func TestRecover_ErrorFormatMentionsNode(t *testing.T) {
	cg := singleNodeGraph(t, func(_ context.Context, _ int) (int, error) {
		panic(fmt.Errorf("wrapped panic value"))
	})

	_, err := cg.Run(context.Background(), 0, WithMiddleware(Recover[int]()))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "wrapped panic value") {
		t.Errorf("err text missing panic value: %v", err)
	}
}
