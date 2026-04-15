# Parallel Execution

This document captures the analysis and implications of adding graph-level parallel
node execution (fan-out/fan-in) to daggo. This is deferred — the current sequential
model is sufficient for most LLM orchestration patterns, and within-node parallelism
(goroutines inside a `NodeFunc`) covers many concurrent use cases without any
graph-level changes.

This document exists so the analysis doesn't need to be repeated when the need arises.

## Current Model

The executor is a `for` loop with a single `current` pointer. One node executes at a
time. State flows linearly from node to node.

```
Start -> A -> B -> C -> End
               ^
           one place, always
```

## What Parallel Execution Means

Fan-out/fan-in means the graph can be in multiple places at once:

```
Start -> A -> B --> D -> End
              +-> C -+
              (B and C run concurrently, D waits for both)
```

This introduces complexity in five areas.

### 1. Graph Model Changes

Today a node has one outgoing edge (static or conditional). Fan-out means one node has
multiple outgoing edges that execute concurrently. The graph model needs to distinguish
between:

- **Conditional edges**: "choose one path based on state" (exists today)
- **Parallel edges**: "take all paths concurrently" (new)

Join semantics are also needed — a downstream node can't run until all upstream
parallel branches complete.

### 2. State Forking and Merging

This is the hardest part. Today state flows linearly — each node gets the output of
the previous one. With parallel branches:

```go
// A produces stateA
// B and C both receive stateA
// B produces stateB, C produces stateC
// D receives... what?
```

A merge function is required:

```go
type MergeFunc[S any] func(results []S) (S, error)
```

With pointer state, B and C mutating the same object is a data race. With value state,
you get two independent copies that need reconciliation. Either way, merge semantics
must be defined explicitly.

LangGraph solves this with reducers — each field on the state declares how concurrent
writes merge (e.g., `operator.add` for lists). That works but spreads merge logic
across the state definition rather than keeping it at the join point.

### 3. The Executor Becomes a Scheduler

Instead of a simple `for` loop, the executor needs to manage goroutines:

```go
if len(edges) == 1 {
    // simple case, same as today
    state, err = edges[0].fn(ctx, state)
} else {
    // fan-out: launch goroutines, collect results, merge
    var wg sync.WaitGroup
    results := make([]S, len(edges))
    errs := make([]error, len(edges))

    for i, edge := range edges {
        wg.Add(1)
        go func(i int, e Edge[S]) {
            defer wg.Done()
            // if this edge leads to a sub-chain (B -> E -> F before
            // rejoining at D), the entire sub-path must execute in
            // the goroutine
            results[i], errs[i] = cg.runSubpath(ctx, state, e.target)
        }(i, edge)
    }
    wg.Wait()

    state, err = merge(results)
}
```

Note that parallel branches may themselves contain multiple nodes (sub-paths), not just
a single node. The executor needs to handle this.

### 4. Error and Cancellation Semantics Multiply

If B fails, what happens to C? Options:

- **Let C finish**: wastes work, but simple
- **Cancel C via context**: requires per-branch derived contexts
- **Collect all errors**: let the caller decide via `errors.Join`

Each is valid, none is obviously right, and the choice probably varies per graph. This
suggests per-fan-out error policy configuration.

### 5. Cycle Detection Gets Harder

Today cycle detection uses a per-node execution counter. With parallel paths, the same
node might execute simultaneously in two independent branches. That's not a cycle, but
a shared counter would flag it as one. Per-path execution tracking would be needed
instead.

## The Alternative: Within-Node Parallelism

Most LLM orchestration parallelism is "call N tools concurrently, then feed all
results to one LLM call." This is a single node, not a graph-level concern:

```go
g.AddNode("gather", func(ctx context.Context, s *State) (*State, error) {
    g, ctx := errgroup.WithContext(ctx)
    var a, b, c Result

    g.Go(func() error { var err error; a, err = toolA(ctx, s.Input); return err })
    g.Go(func() error { var err error; b, err = toolB(ctx, s.Input); return err })
    g.Go(func() error { var err error; c, err = toolC(ctx, s.Input); return err })

    if err := g.Wait(); err != nil {
        return s, err
    }

    s.Gathered = combine(a, b, c)
    return s, nil
})
```

No graph-level changes needed. The complexity stays contained in the one node that
needs it. Error handling uses `errgroup`'s context cancellation. State merging is
explicit in the `combine` call.

## Summary

Adding graph-level parallel execution roughly doubles the complexity of the codebase.
Every component (state model, edge model, executor, error handling, cycle detection)
gains a second mode. The mental model goes from "follow the pointer" to "manage a tree
of concurrent execution."

Defer this until within-node parallelism is demonstrably insufficient for a real use
case. When that time comes, the key design decisions are:

1. How to represent parallel vs. conditional edges in the graph model
2. How and where to define state merge semantics
3. What error/cancellation policy to use (and whether it's configurable per-fan-out)
4. How to track cycles across concurrent branches
