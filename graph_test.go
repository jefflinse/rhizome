package daggo

import (
	"context"
	"errors"
	"fmt"
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
	must(t, g.AddConditionalEdge("entry", func(s *state) string {
		if s.Route == "left" {
			return "left"
		}
		return "right"
	}))

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
	must(t, g.AddConditionalEdge("inc", func(s *state) string {
		if s.Counter >= 3 {
			return End
		}
		return "inc"
	}))

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

func TestImplicitEnd(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("only", appendValue("only")))
	must(t, g.AddEdge(Start, "only"))

	compiled := mustCompile(t, g)
	result, err := compiled.Run(context.Background(), &state{})
	if err != nil {
		t.Fatal(err)
	}

	assertValues(t, result.Values, []string{"only"})
}

func TestConditionalEntry(t *testing.T) {
	g := New[*state]()
	must(t, g.AddNode("fast", appendValue("fast")))
	must(t, g.AddNode("slow", appendValue("slow")))
	must(t, g.AddConditionalEdge(Start, func(s *state) string {
		if s.Route == "fast" {
			return "fast"
		}
		return "slow"
	}))

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
		must(t, g.AddEdge("nonexistent", "a"))
		_, err := g.Compile()
		assertErrorIs(t, err, ErrNodeNotFound)
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
		err := g.AddConditionalEdge("a", func(s *state) string { return End })
		assertErrorIs(t, err, ErrConflictingEdge)
	})

	t.Run("conflicting conditional then static", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("a", noop))
		must(t, g.AddNode("b", noop))
		must(t, g.AddConditionalEdge("a", func(s *state) string { return End }))
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
		err := g.AddConditionalEdge("x", nil)
		if err == nil {
			t.Fatal("expected error for nil router function")
		}
	})
}

func TestRuntimeErrors(t *testing.T) {
	t.Run("node error halts execution", func(t *testing.T) {
		nodeErr := fmt.Errorf("node failed")
		g := New[*state]()
		must(t, g.AddNode("fail", func(_ context.Context, s *state) (*state, error) {
			s.Values = append(s.Values, "fail")
			return s, nodeErr
		}))
		must(t, g.AddNode("after", appendValue("after")))
		must(t, g.AddEdge(Start, "fail"))
		must(t, g.AddEdge("fail", "after"))

		compiled := mustCompile(t, g)
		result, err := compiled.Run(context.Background(), &state{})
		if !errors.Is(err, nodeErr) {
			t.Fatalf("got %v, want %v", err, nodeErr)
		}
		assertValues(t, result.Values, []string{"fail"})
	})

	t.Run("invalid route from conditional", func(t *testing.T) {
		g := New[*state]()
		must(t, g.AddNode("router", noop))
		must(t, g.AddEdge(Start, "router"))
		must(t, g.AddConditionalEdge("router", func(s *state) string {
			return "nonexistent"
		}))

		compiled := mustCompile(t, g)
		_, err := compiled.Run(context.Background(), &state{})
		assertErrorIs(t, err, ErrInvalidRoute)
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
	must(t, g.AddConditionalEdge("check", func(s *state) string {
		if s.Route == "done" {
			return End
		}
		return "unreached"
	}))

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

	compiled := mustCompile(t, g)
	result, err := compiled.Run(ctx, &state{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
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
