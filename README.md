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

Route dynamically based on state:

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
g.AddConditionalEdge("classify", func(s *state) string {
	if s.Status == "high" {
		return "handle-high"
	}
	return "handle-low"
})
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
g.AddConditionalEdge("increment", func(n int) string {
	if n >= 5 {
		return rhizome.End
	}
	return "increment"
})

compiled, _ := g.Compile(rhizome.WithMaxNodeExecs(20)) // raise limit if needed
result, _ := compiled.Run(context.Background(), 0)     // result: 5
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

## API

| Function / Type | Description |
|---|---|
| `New[S]()` | Create a new graph builder |
| `AddNode(name, fn)` | Register a named node |
| `AddEdge(from, to)` | Add a static edge between nodes |
| `AddConditionalEdge(from, router)` | Add dynamic routing based on state |
| `Compile(opts...)` | Validate and freeze the graph |
| `Run(ctx, state, opts...)` | Execute the compiled graph |
| `Start` / `End` | Virtual entry and exit points |
| `WithMaxNodeExecs(n)` | Compile option: set per-node execution limit |
| `WithMiddleware(mw...)` | Run option: add middleware chain |

## License

See [LICENSE](LICENSE).
