package rhizome

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type state struct {
	Values  []string
	Counter int
	Route   string
}

var noop NodeFunc[*state] = func(_ context.Context, s *state) (*state, error) {
	return s, nil
}

func TestLinearGraph(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("a", appendValue("a")))
	must(t, g.AddNode("b", appendValue("b")))
	must(t, g.AddNode("c", appendValue("c")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", "b"))
	must(t, g.AddEdge("b", "c"))
	must(t, g.AddEdge("c", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{})
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, result.Values, []string{"a", "b", "c"})
}

func TestConditionalBranching(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("entry", appendValue("entry")))
	must(t, g.AddNode("left", appendValue("left")))
	must(t, g.AddNode("right", appendValue("right")))
	must(t, g.AddEdge(Start, "entry"))
	must(t, g.AddConditionalEdge("entry", func(_ context.Context, s *state) (string, error) {
		if s.Route == "left" {
			return "left", nil
		}
		return "right", nil
	}, "left", "right"))
	must(t, g.AddEdge("left", End))
	must(t, g.AddEdge("right", End))

	compiled := mustCompile(t, g)

	t.Run("left", func(t *testing.T) {
		result, err := compiled.Run(context.Background(), &state{Route: "left"})
		if err != nil {
			t.Fatal(err)
		}
		assertValues(t, result.Values, []string{"entry", "left"})
	})

	t.Run("right", func(t *testing.T) {
		result, err := compiled.Run(context.Background(), &state{Route: "right"})
		if err != nil {
			t.Fatal(err)
		}
		assertValues(t, result.Values, []string{"entry", "right"})
	})
}

func TestCycleWithTermination(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("inc", func(_ context.Context, s *state) (*state, error) {
		s.Counter++
		s.Values = append(s.Values, fmt.Sprintf("inc:%d", s.Counter))
		return s, nil
	}))
	must(t, g.AddEdge(Start, "inc"))
	must(t, g.AddConditionalEdge("inc", func(_ context.Context, s *state) (string, error) {
		if s.Counter >= 3 {
			return End, nil
		}
		return "inc", nil
	}, "inc", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{})
	if err != nil {
		t.Fatal(err)
	}

	if result.Counter != 3 {
		t.Errorf("counter = %d, want 3", result.Counter)
	}
	assertValues(t, result.Values, []string{"inc:1", "inc:2", "inc:3"})
}

func TestCycleSafety(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("loop", func(_ context.Context, s *state) (*state, error) {
		s.Counter++
		return s, nil
	}))
	must(t, g.AddEdge(Start, "loop"))
	must(t, g.AddEdge("loop", "loop"))

	compiled := mustCompile(t, g, WithMaxNodeExecs(5))

	result, err := compiled.Run(context.Background(), &state{})
	if !errors.Is(err, ErrCycleLimit) {
		t.Fatalf("got %v, want ErrCycleLimit", err)
	}
	if result.Counter != 5 {
		t.Errorf("counter = %d, want 5", result.Counter)
	}
}

func TestConditionalEntry(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("fast", appendValue("fast")))
	must(t, g.AddNode("slow", appendValue("slow")))
	must(t, g.AddConditionalEdge(Start, func(_ context.Context, s *state) (string, error) {
		if s.Route == "fast" {
			return "fast", nil
		}
		return "slow", nil
	}, "fast", "slow"))
	must(t, g.AddEdge("fast", End))
	must(t, g.AddEdge("slow", End))

	compiled := mustCompile(t, g)

	t.Run("fast", func(t *testing.T) {
		result, err := compiled.Run(context.Background(), &state{Route: "fast"})
		if err != nil {
			t.Fatal(err)
		}
		assertValues(t, result.Values, []string{"fast"})
	})

	t.Run("slow", func(t *testing.T) {
		result, err := compiled.Run(context.Background(), &state{Route: "slow"})
		if err != nil {
			t.Fatal(err)
		}
		assertValues(t, result.Values, []string{"slow"})
	})
}

func TestValueTypeState(t *testing.T) {
	g := New[int]()
	must(t, g.AddNode("double", func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	}))
	must(t, g.AddNode("add1", func(_ context.Context, n int) (int, error) {
		return n + 1, nil
	}))
	must(t, g.AddEdge(Start, "double"))
	must(t, g.AddEdge("double", "add1"))
	must(t, g.AddEdge("add1", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if result != 11 {
		t.Errorf("got %d, want 11", result)
	}
}

func TestCompileErrors(t *testing.T) {
	t.Run("no entrypoint", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		_, err := g.Compile()
		assertErrorIs(t, err, ErrNoEntrypoint)
	})

	t.Run("unreachable node", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddNode("orphan", noop))
		must(t, g.AddEdge(Start, "a"))
		must(t, g.AddEdge("a", End))
		must(t, g.AddEdge("orphan", End))
		_, err := g.Compile()
		assertErrorIs(t, err, ErrUnreachableNode)
	})

	t.Run("unreachable node behind conditional", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddNode("orphan", noop))
		must(t, g.AddEdge(Start, "a"))
		must(t, g.AddConditionalEdge("a", func(_ context.Context, s *state) (string, error) {
			return "b", nil
		}, "b"))
		must(t, g.AddEdge("b", End))
		must(t, g.AddEdge("orphan", End))
		_, err := g.Compile()
		assertErrorIs(t, err, ErrUnreachableNode)
	})

	t.Run("edge target not found", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddEdge(Start, "a"))
		must(t, g.AddEdge("a", "nonexistent"))
		_, err := g.Compile()
		assertErrorIs(t, err, ErrNodeNotFound)
	})

	t.Run("edge source not found", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddEdge(Start, "a"))
		must(t, g.AddEdge("a", End))
		must(t, g.AddEdge("nonexistent", "a"))
		_, err := g.Compile()
		assertErrorIs(t, err, ErrNodeNotFound)
	})

	t.Run("conditional edge target not found", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddEdge(Start, "a"))
		must(t, g.AddConditionalEdge("a", func(_ context.Context, s *state) (string, error) {
			return End, nil
		}, "ghost"))
		_, err := g.Compile()
		assertErrorIs(t, err, ErrNodeNotFound)
	})

	t.Run("node with no outgoing edge", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddEdge(Start, "a"))
		must(t, g.AddEdge("a", "b"))
		// b has no outgoing edge
		_, err := g.Compile()
		assertErrorIs(t, err, ErrNoOutgoingEdge)
	})

	t.Run("reserved node name Start", func(t *testing.T) {
		g := New[*state]()
		err := g.AddNode(Start, noop)
		assertErrorIs(t, err, ErrReservedName)
	})

	t.Run("reserved node name End", func(t *testing.T) {
		g := New[*state]()
		err := g.AddNode(End, noop)
		assertErrorIs(t, err, ErrReservedName)
	})

	t.Run("duplicate node", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		err := g.AddNode("a", noop)
		assertErrorIs(t, err, ErrDuplicateNode)
	})

	t.Run("duplicate static edge", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddEdge(Start, "a"))
		err := g.AddEdge(Start, "b")
		assertErrorIs(t, err, ErrDuplicateEdge)
	})

	t.Run("conflicting static then conditional", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddEdge("a", "b"))
		err := g.AddConditionalEdge("a", func(_ context.Context, s *state) (string, error) { return End, nil }, End)
		assertErrorIs(t, err, ErrConflictingEdge)
	})

	t.Run("conflicting conditional then static", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddConditionalEdge("a", func(_ context.Context, s *state) (string, error) { return End, nil }, End))
		err := g.AddEdge("a", "b")
		assertErrorIs(t, err, ErrConflictingEdge)
	})

	t.Run("edge from End", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		err := g.AddEdge(End, "a")
		assertErrorIs(t, err, ErrReservedName)
	})

	t.Run("edge to Start", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		err := g.AddEdge("a", Start)
		assertErrorIs(t, err, ErrReservedName)
	})

	t.Run("nil node function", func(t *testing.T) {
		g := New[*state]()
		err := g.AddNode("x", nil)
		if err == nil {
			t.Fatal("expected error for nil node function")
		}
	})

	t.Run("nil router function", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("x", noop))
		err := g.AddConditionalEdge("x", nil, End)
		if err == nil {
			t.Fatal("expected error for nil router function")
		}
	})

	t.Run("conditional edge with no targets", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("x", noop))
		err := g.AddConditionalEdge("x", func(_ context.Context, s *state) (string, error) { return End, nil })
		assertErrorIs(t, err, ErrNoTargets)
	})

	t.Run("conditional edge target is Start", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("x", noop))
		err := g.AddConditionalEdge("x", func(_ context.Context, s *state) (string, error) { return End, nil }, Start)
		assertErrorIs(t, err, ErrReservedName)
	})
}

func TestRuntimeErrors(t *testing.T) {
	t.Run("node error wrapped with node name", func(t *testing.T) {
		nodeErr := fmt.Errorf("node failed")
		g := New[*state]()
		must(t, g.AddNode("fail", func(_ context.Context, s *state) (*state, error) {
			s.Values = append(s.Values, "fail")
			return s, nodeErr
		}))
		must(t, g.AddNode("after", appendValue("after")))
		must(t, g.AddEdge(Start, "fail"))
		must(t, g.AddEdge("fail", "after"))
		must(t, g.AddEdge("after", End))

		compiled := mustCompile(t, g)
		result, err := compiled.Run(context.Background(), &state{})
		if !errors.Is(err, nodeErr) {
			t.Fatalf("got %v, want %v wrapped", err, nodeErr)
		}
		if !strings.Contains(err.Error(), `"fail"`) {
			t.Fatalf("error message %q does not name the failing node", err.Error())
		}
		assertValues(t, result.Values, []string{"fail"})
	})

	t.Run("router returns undeclared target", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("router", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddEdge(Start, "router"))
		must(t, g.AddEdge("b", End))
		must(t, g.AddConditionalEdge("router", func(_ context.Context, s *state) (string, error) {
			return "rogue", nil
		}, "b"))

		compiled := mustCompile(t, g)
		_, err := compiled.Run(context.Background(), &state{})
		assertErrorIs(t, err, ErrUndeclaredTarget)
		if !strings.Contains(err.Error(), `"router"`) {
			t.Fatalf("error message %q does not name the router node", err.Error())
		}
	})

	t.Run("router returns Start", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("router", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddEdge(Start, "router"))
		must(t, g.AddEdge("b", End))
		must(t, g.AddConditionalEdge("router", func(_ context.Context, s *state) (string, error) {
			return Start, nil
		}, "b"))

		compiled := mustCompile(t, g)
		_, err := compiled.Run(context.Background(), &state{})
		assertErrorIs(t, err, ErrInvalidRoute)
	})

	t.Run("router returns error", func(t *testing.T) {
		routerErr := fmt.Errorf("bad state")
		g := New[*state]()
		must(t, g.AddNode("router", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddEdge(Start, "router"))
		must(t, g.AddEdge("b", End))
		must(t, g.AddConditionalEdge("router", func(_ context.Context, s *state) (string, error) {
			return "", routerErr
		}, "b"))

		compiled := mustCompile(t, g)
		_, err := compiled.Run(context.Background(), &state{})
		if !errors.Is(err, routerErr) {
			t.Fatalf("got %v, want %v wrapped", err, routerErr)
		}
		if !strings.Contains(err.Error(), `"router"`) {
			t.Fatalf("error message %q does not name the router node", err.Error())
		}
	})
}

func TestConditionalEdgeToEnd(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("check", func(_ context.Context, s *state) (*state, error) {
		s.Values = append(s.Values, "check")
		return s, nil
	}))
	must(t, g.AddNode("unreached", appendValue("unreached")))
	must(t, g.AddEdge(Start, "check"))
	must(t, g.AddConditionalEdge("check", func(_ context.Context, s *state) (string, error) {
		if s.Route == "done" {
			return End, nil
		}
		return "unreached", nil
	}, "unreached", End))
	must(t, g.AddEdge("unreached", End))

	compiled := mustCompile(t, g)

	t.Run("routes to End", func(t *testing.T) {
		result, err := compiled.Run(context.Background(), &state{Route: "done"})
		if err != nil {
			t.Fatal(err)
		}
		assertValues(t, result.Values, []string{"check"})
	})

	t.Run("routes to node", func(t *testing.T) {
		result, err := compiled.Run(context.Background(), &state{Route: "continue"})
		if err != nil {
			t.Fatal(err)
		}
		assertValues(t, result.Values, []string{"check", "unreached"})
	})
}

func TestCompiledGraphIsImmutable(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("a", appendValue("a")))
	must(t, g.AddNode("b", appendValue("b")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", "b"))
	must(t, g.AddEdge("b", End))

	compiled := mustCompile(t, g)

	// Mutate the original graph after compilation
	must(t, g.AddNode("c", appendValue("c")))
	g.edges["b"] = "c"

	// Compiled graph should still reflect the original topology
	result, err := compiled.Run(context.Background(), &state{})
	if err != nil {
		t.Fatal(err)
	}
	assertValues(t, result.Values, []string{"a", "b"})
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	g := New[*state]()
	must(t, g.AddNode("a", func(_ context.Context, s *state) (*state, error) {
		s.Values = append(s.Values, "a")
		cancel()
		return s, nil
	}))
	must(t, g.AddNode("b", appendValue("b")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", "b"))
	must(t, g.AddEdge("b", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(ctx, &state{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	assertValues(t, result.Values, []string{"a"})
}

func TestRunLevelMaxNodeExecs(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("loop", func(_ context.Context, s *state) (*state, error) {
		s.Counter++
		return s, nil
	}))
	must(t, g.AddEdge(Start, "loop"))
	must(t, g.AddEdge("loop", "loop"))

	compiled := mustCompile(t, g, WithMaxNodeExecs(100))

	result, err := compiled.Run(context.Background(), &state{}, WithRunMaxNodeExecs[*state](3))
	if !errors.Is(err, ErrCycleLimit) {
		t.Fatalf("got %v, want ErrCycleLimit", err)
	}
	if result.Counter != 3 {
		t.Errorf("counter = %d, want 3 (Run-level override)", result.Counter)
	}
}

// Middleware tests

func TestMiddlewareWrapsExecution(t *testing.T) {
	mw := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		s.Values = append(s.Values, "before:"+node)
		s, err := next(context.Background(), s)
		s.Values = append(s.Values, "after:"+node)
		return s, err
	}

	g := New[*state]()
	must(t, g.AddNode("a", appendValue("a")))
	must(t, g.AddNode("b", appendValue("b")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", "b"))
	must(t, g.AddEdge("b", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{}, WithMiddleware(mw))
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, result.Values, []string{
		"before:a", "a", "after:a",
		"before:b", "b", "after:b",
	})
}

func TestMiddlewareChainOrder(t *testing.T) {
	outer := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		s.Values = append(s.Values, "outer-before")
		s, err := next(context.Background(), s)
		s.Values = append(s.Values, "outer-after")
		return s, err
	}
	inner := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		s.Values = append(s.Values, "inner-before")
		s, err := next(context.Background(), s)
		s.Values = append(s.Values, "inner-after")
		return s, err
	}

	g := New[*state]()
	must(t, g.AddNode("a", appendValue("a")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{}, WithMiddleware(outer, inner))
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, result.Values, []string{
		"outer-before", "inner-before", "a", "inner-after", "outer-after",
	})
}

func TestMiddlewareSeesNodeName(t *testing.T) {
	var names []string
	mw := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		names = append(names, node)
		return next(context.Background(), s)
	}

	g := New[*state]()
	must(t, g.AddNode("a", appendValue("a")))
	must(t, g.AddNode("b", appendValue("b")))
	must(t, g.AddNode("c", appendValue("c")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", "b"))
	must(t, g.AddEdge("b", "c"))
	must(t, g.AddEdge("c", End))

	compiled := mustCompile(t, g)
	_, err := compiled.Run(context.Background(), &state{}, WithMiddleware(mw))
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, names, []string{"a", "b", "c"})
}

func TestMiddlewareModifiesContext(t *testing.T) {
	type ctxKey struct{}

	mw := func(ctx context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		ctx = context.WithValue(ctx, ctxKey{}, "injected")
		return next(ctx, s)
	}

	g := New[*state]()
	must(t, g.AddNode("a", func(ctx context.Context, s *state) (*state, error) {
		v, _ := ctx.Value(ctxKey{}).(string)
		s.Values = append(s.Values, v)
		return s, nil
	}))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{}, WithMiddleware(mw))
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, result.Values, []string{"injected"})
}

func TestMiddlewareSeesNodeError(t *testing.T) {
	nodeErr := fmt.Errorf("node failed")
	var sawError bool

	mw := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		s.Values = append(s.Values, "before")
		s, err := next(context.Background(), s)
		if err != nil {
			sawError = true
		}
		s.Values = append(s.Values, "after")
		return s, err
	}

	g := New[*state]()
	must(t, g.AddNode("a", func(_ context.Context, s *state) (*state, error) {
		s.Values = append(s.Values, "a")
		return s, nodeErr
	}))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{}, WithMiddleware(mw))
	if !errors.Is(err, nodeErr) {
		t.Fatalf("got %v, want %v", err, nodeErr)
	}
	if !sawError {
		t.Fatal("middleware did not see the error")
	}
	assertValues(t, result.Values, []string{"before", "a", "after"})
}

func TestMiddlewareShortCircuit(t *testing.T) {
	mw := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		s.Values = append(s.Values, "skipped:"+node)
		return s, nil // does not call next
	}

	g := New[*state]()
	must(t, g.AddNode("a", appendValue("a")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{}, WithMiddleware(mw))
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, result.Values, []string{"skipped:a"})
}

func TestMiddlewareDoesNotAffectCycleLimit(t *testing.T) {
	mwCalls := 0
	mw := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		mwCalls++
		return next(context.Background(), s)
	}

	g := New[*state]()
	must(t, g.AddNode("loop", func(_ context.Context, s *state) (*state, error) {
		s.Counter++
		return s, nil
	}))
	must(t, g.AddEdge(Start, "loop"))
	must(t, g.AddEdge("loop", "loop"))

	compiled := mustCompile(t, g, WithMaxNodeExecs(3))
	result, err := compiled.Run(context.Background(), &state{}, WithMiddleware(mw))
	if !errors.Is(err, ErrCycleLimit) {
		t.Fatalf("got %v, want ErrCycleLimit", err)
	}
	if result.Counter != 3 {
		t.Errorf("counter = %d, want 3", result.Counter)
	}
	if mwCalls != 3 {
		t.Errorf("middleware calls = %d, want 3", mwCalls)
	}
}

func TestMultipleWithMiddlewareCalls(t *testing.T) {
	mw1 := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		s.Values = append(s.Values, "mw1-before")
		s, err := next(context.Background(), s)
		s.Values = append(s.Values, "mw1-after")
		return s, err
	}
	mw2 := func(_ context.Context, node string, s *state, next NodeFunc[*state]) (*state, error) {
		s.Values = append(s.Values, "mw2-before")
		s, err := next(context.Background(), s)
		s.Values = append(s.Values, "mw2-after")
		return s, err
	}

	g := New[*state]()
	must(t, g.AddNode("a", appendValue("a")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{},
		WithMiddleware(mw1),
		WithMiddleware(mw2),
	)
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, result.Values, []string{
		"mw1-before", "mw2-before", "a", "mw2-after", "mw1-after",
	})
}

func TestRunWithoutMiddleware(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("a", appendValue("a")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{})
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, result.Values, []string{"a"})
}

// Helpers

func appendValue(v string) NodeFunc[*state] {
	return func(_ context.Context, s *state) (*state, error) {
		s.Values = append(s.Values, v)
		return s, nil
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustCompile[S any](t *testing.T, g *Graph[S], opts ...CompileOption) *CompiledGraph[S] {
	t.Helper()
	compiled, err := g.Compile(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func assertValues(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values = %v, want %v", got, want)
		}
	}
}

func assertErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got %v, want %v", err, target)
	}
}
