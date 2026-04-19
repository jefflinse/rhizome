package rhizome

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// Recover returns a Middleware that converts panics in the wrapped node into
// an error wrapping ErrNodePanic. The panic value is included in the error
// text and the stack trace at the point of panic is captured.
//
// Because a panicking node produces no valid output, the middleware returns
// the state as it was when the node was entered.
func Recover[S any]() Middleware[S] {
	return func(ctx context.Context, node string, state S, next NodeFunc[S]) (result S, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%w in %q: %v\n%s", ErrNodePanic, node, r, debug.Stack())
				result = state
			}
		}()
		return next(ctx, state)
	}
}

// Timeout returns a Middleware that bounds the execution of the wrapped node
// by d. A child context with deadline d is threaded into next; if the node
// fails to respect ctx.Done() the deadline has no effect.
//
// A non-positive duration disables the timeout, making the middleware a no-op.
func Timeout[S any](d time.Duration) Middleware[S] {
	return func(ctx context.Context, node string, state S, next NodeFunc[S]) (S, error) {
		if d <= 0 {
			return next(ctx, state)
		}
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return next(ctx, state)
	}
}

// RetryOption configures a Retry middleware.
type RetryOption func(*retryConfig)

type retryConfig struct {
	maxAttempts int
	backoff     func(attempt int) time.Duration
	retryIf     func(error) bool
}

// DefaultRetryMaxAttempts is the default number of times Retry will attempt
// a node before giving up. One attempt is the initial call; attempts above
// that are retries.
const DefaultRetryMaxAttempts = 3

// DefaultRetryBackoff returns an exponential backoff of 100ms * 2^(attempt-1):
// 100ms before the second attempt, 200ms before the third, and so on.
func DefaultRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	return 100 * time.Millisecond * (1 << (attempt - 1))
}

// DefaultRetryClassifier retries every error except context.Canceled and
// context.DeadlineExceeded. Retrying on context errors would defeat the
// caller's ability to cancel a running graph.
func DefaultRetryClassifier(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// WithMaxAttempts sets the maximum number of attempts, including the initial
// call. Values less than 1 are treated as 1 (no retries).
func WithMaxAttempts(n int) RetryOption {
	return func(c *retryConfig) {
		if n < 1 {
			n = 1
		}
		c.maxAttempts = n
	}
}

// WithBackoff sets the backoff function used between attempts. The argument
// is the 1-based index of the attempt that just failed (so the sleep before
// attempt 2 receives attempt=1).
func WithBackoff(fn func(attempt int) time.Duration) RetryOption {
	return func(c *retryConfig) {
		c.backoff = fn
	}
}

// WithRetryIf sets the classifier that decides whether an error is retryable.
// Returning false on any error causes Retry to surface it immediately.
func WithRetryIf(fn func(error) bool) RetryOption {
	return func(c *retryConfig) {
		c.retryIf = fn
	}
}

// Retry returns a Middleware that re-invokes the wrapped node on error.
//
// By default it attempts DefaultRetryMaxAttempts times total, sleeps
// DefaultRetryBackoff between attempts, and retries every error except
// context.Canceled and context.DeadlineExceeded. Use WithMaxAttempts,
// WithBackoff, and WithRetryIf to override.
//
// The sleep between attempts aborts immediately when ctx is cancelled or
// hits its deadline; the context error is returned in that case.
//
// When composed with Timeout, place Timeout *inside* Retry (i.e. Retry is
// added before Timeout in WithMiddleware) so that each attempt gets its
// own deadline rather than sharing one across attempts.
func Retry[S any](opts ...RetryOption) Middleware[S] {
	cfg := retryConfig{
		maxAttempts: DefaultRetryMaxAttempts,
		backoff:     DefaultRetryBackoff,
		retryIf:     DefaultRetryClassifier,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(ctx context.Context, node string, state S, next NodeFunc[S]) (S, error) {
		var (
			result  S
			lastErr error
		)
		for attempt := 1; attempt <= cfg.maxAttempts; attempt++ {
			result, lastErr = next(ctx, state)
			if lastErr == nil {
				return result, nil
			}
			if !cfg.retryIf(lastErr) {
				return result, lastErr
			}
			if attempt == cfg.maxAttempts {
				break
			}

			wait := cfg.backoff(attempt)
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return result, ctx.Err()
				}
			}
		}
		return result, lastErr
	}
}
