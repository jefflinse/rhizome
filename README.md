# rhizome

A lightweight, generic graph execution engine in Go. Define nodes and edges, compile the graph, and run it.

```
go get github.com/jefflinse/rhizome
```

## Quick Start

### Linear Pipeline

```go
package main

import (
	"context"
	"fmt"

	"github.com/jefflinse/rhizome"
)

func main() {
	g := rhizome.New[int]()

	g.AddNode("double", func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	})
	g.AddNode("add-ten", func(_ context.Context, n int) (int, error) {
		return n + 10, nil
	})

	g.AddEdge(rhizome.Start, "double")
	g.AddEdge("double", "add-ten")
	g.AddEdge("add-ten", rhizome.End)

	compiled, _ := g.Compile()
	result, _ := compiled.Run(context.Background(), 5)

	fmt.Println(result) // 20
}
```

### Conditional Branching

Route dynamically based on state. Declare the set of targets the router
may return so Compile can verify reachability:

```go
type state struct {
	Value  int
	Status string
}

g := rhizome.New[*state]()

g.AddNode("classify", func(_ context.Context, s *state) (*state, error) {
	if s.Value >= 100 {
		s.Status = "high"
	} else {
		s.Status = "low"
	}
	return s, nil
})
g.AddNode("handle-high", handleHigh)
g.AddNode("handle-low", handleLow)

g.AddEdge(rhizome.Start, "classify")
g.AddConditionalEdge("classify", func(_ context.Context, s *state) (string, error) {
	if s.Status == "high" {
		return "handle-high", nil
	}
	return "handle-low", nil
}, "handle-high", "handle-low")
g.AddEdge("handle-high", rhizome.End)
g.AddEdge("handle-low", rhizome.End)

compiled, _ := g.Compile()
result, _ := compiled.Run(ctx, &state{Value: 150})
```

### Loops

Nodes can loop back to themselves or earlier nodes. Built-in cycle protection prevents infinite loops (default: 10 executions per node).

```go
g := rhizome.New[int]()

g.AddNode("increment", func(_ context.Context, n int) (int, error) {
	return n + 1, nil
})

g.AddEdge(rhizome.Start, "increment")
g.AddConditionalEdge("increment", func(_ context.Context, n int) (string, error) {
	if n >= 5 {
		return rhizome.End, nil
	}
	return "increment", nil
}, "increment", rhizome.End)

compiled, _ := g.Compile(rhizome.WithMaxNodeExecs(20)) // compile-time default
result, _ := compiled.Run(context.Background(), 0)     // result: 5

// Per-Run override:
result, _ = compiled.Run(context.Background(), 0, rhizome.WithRunMaxNodeExecs[int](50))
```

### Middleware

Wrap node execution for logging, timing, or anything else:

```go
logger := func(ctx context.Context, node string, state int, next rhizome.NodeFunc[int]) (int, error) {
	fmt.Printf("entering %s\n", node)
	result, err := next(ctx, state)
	fmt.Printf("leaving %s\n", node)
	return result, err
}

result, _ := compiled.Run(ctx, 0, rhizome.WithMiddleware(logger))
```

#### Built-in middleware

Three resilience primitives are included:

```go
result, err := compiled.Run(ctx, initial, rhizome.WithMiddleware(
    rhizome.Recover[*State](),               // trap panics, return ErrNodePanic
    rhizome.Retry[*State](                   // retry transient failures
        rhizome.WithMaxAttempts(3),
    ),
    rhizome.Timeout[*State](30*time.Second), // per-attempt deadline
))
```

- **`Recover`** converts panics into an error wrapping `ErrNodePanic`, with
  the panic value and stack trace included. The input state is returned
  unchanged since the node produced no valid output.
- **`Timeout(d)`** threads a `context.WithTimeout` into the node. Node code
  must honor `ctx.Done()` for it to take effect.
- **`Retry(opts...)`** re-invokes the node on error. Defaults: 3 attempts,
  exponential backoff starting at 100ms, and a classifier that retries
  everything *except* `context.Canceled` and `context.DeadlineExceeded`
  (so cancelling a run unwinds promptly instead of retrying). Override
  with `WithMaxAttempts`, `WithBackoff`, and `WithRetryIf`.

When combining `Retry` with `Timeout`, place `Retry` *before* `Timeout` in
the middleware list as shown above. That way each retry attempt gets its
own deadline; reversing the order makes one deadline span all attempts.

### Checkpointing

Opt in to persisted state and the graph saves a snapshot after every node.
A crashed run can be resumed later — possibly in a different process —
from the last successful node.

State must implement `Snapshotter` (composed from the stdlib
`encoding.BinaryMarshaler`/`BinaryUnmarshaler` pair). The type check runs
at `Compile` time, so misconfiguration fails early.

```go
type MyState struct {
	Step int
	Logs []string
}

func (s *MyState) MarshalBinary() ([]byte, error)    { return json.Marshal(s) }
func (s *MyState) UnmarshalBinary(data []byte) error { return json.Unmarshal(data, s) }

store := &rhizome.MemoryStore{} // or any CheckpointStore implementation

g := rhizome.New[*MyState]()
// ... AddNode/AddEdge ...

compiled, err := g.Compile(rhizome.WithCheckpointing(store))
if err != nil {
	// Returns ErrCheckpointRequiresSnapshotter if *MyState does not satisfy Snapshotter.
	panic(err)
}

// Thread IDs correlate runs with checkpoints; required when checkpointing is enabled.
final, err := compiled.Run(ctx, &MyState{}, rhizome.WithThreadID[*MyState]("conversation-123"))

// If the run was interrupted, resume from the last checkpoint in a fresh state instance:
resumed, err := compiled.Resume(ctx, "conversation-123", &MyState{})
```

`CheckpointStore` is a two-method interface; `MemoryStore` ships for tests
and single-process use. Persistent backends (SQLite, Postgres, etc.) are
intentionally left to separate modules so the core stays dependency-free.

### Interrupts

A node can pause execution and delegate to a consumer-provided handler by
calling `Interrupt`. The graph's goroutine blocks inside the handler until
it returns. Common uses include human-in-the-loop approvals (CLI prompt,
dialog, web request awaiting a response), waiting on asynchronous external
events (webhook callbacks, queued work results), policy gates, and
debugging breakpoints.

```go
g.AddNode("confirm", func(ctx context.Context, s *State) (*State, error) {
    resp, err := rhizome.Interrupt(ctx, rhizome.InterruptRequest{
        Kind:    "approve",
        Payload: s.Proposal,
    })
    if err != nil {
        return s, err
    }
    s.Approved = resp.Value.(bool)
    return s, nil
})

handler := func(ctx context.Context, req rhizome.InterruptRequest) (rhizome.InterruptResponse, error) {
    // Blocking is expected here — the graph waits.
    // Any blocking call should select on ctx.Done() to honor cancellation.
    approved := promptUser(req.Payload)
    return rhizome.InterruptResponse{Value: approved}, nil
}

final, err := compiled.Run(ctx, &State{},
    rhizome.WithInterruptHandler[*State](handler))
```

`InterruptRequest.Node` is populated by the runtime, so node code doesn't
need to know its own name. `Kind` and `Payload` are consumer-defined —
use `Kind` as a discriminator if a single graph raises multiple kinds of
interrupts. Calling `Interrupt` on a run without a handler returns
`ErrNoInterruptHandler`.

This is an in-process primitive: the graph's goroutine parks inside the
handler and resumes when it returns. Durable pause-and-resume (where the
responder answers minutes or days later, possibly in a different process)
is a separate feature that layers on top of `Snapshotter`.

## API

| Function / Type | Description |
|---|---|
| `New[S]()` | Create a new graph builder |
| `AddNode(name, fn)` | Register a named node |
| `AddEdge(from, to)` | Add a static edge between nodes |
| `AddConditionalEdge(from, router, targets...)` | Add dynamic routing; router may only return declared targets |
| `Compile(opts...)` | Validate and freeze the graph |
| `Run(ctx, state, opts...)` | Execute the compiled graph |
| `Resume(ctx, threadID, empty, opts...)` | Continue a checkpointed run from its last saved node |
| `Start` / `End` | Virtual entry and exit points |
| `WithMaxNodeExecs(n)` | Compile option: per-node execution limit (default) |
| `WithCheckpointing(store)` | Compile option: persist state after each node; requires `S` to satisfy `Snapshotter` |
| `WithRunMaxNodeExecs[S](n)` | Run option: override the per-node execution limit |
| `WithMiddleware(mw...)` | Run option: add middleware chain |
| `WithThreadID[S](id)` | Run option: required when checkpointing is enabled |
| `WithInterruptHandler[S](h)` | Run option: handler invoked when a node calls `Interrupt` |
| `Interrupt(ctx, req)` | Called inside a node to pause and request input from the handler |
| `Snapshotter` | Interface state must satisfy for checkpointing (stdlib binary marshal/unmarshal) |
| `CheckpointStore` | Interface for persisting snapshots |
| `MemoryStore` | In-memory `CheckpointStore` for tests and single-process use (zero value is ready to use) |
| `Recover[S]()` | Built-in middleware: trap panics as `ErrNodePanic` |
| `Timeout[S](d)` | Built-in middleware: bound each node call with a deadline |
| `Retry[S](opts...)` | Built-in middleware: retry failed nodes with backoff |
| `WithMaxAttempts(n)` / `WithBackoff(fn)` / `WithRetryIf(fn)` | `Retry` options |

## License

See [LICENSE](LICENSE.md).
