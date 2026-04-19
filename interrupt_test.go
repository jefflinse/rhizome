package rhizome

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// hitlState carries an interrupt payload in and the resulting answer out.
type hitlState struct {
	Ask    string
	Answer any
}

func askNode(kind string) NodeFunc[*hitlState] {
	return func(ctx context.Context, s *hitlState) (*hitlState, error) {
		resp, err := Interrupt(ctx, InterruptRequest{Kind: kind, Payload: s.Ask})
		if err != nil {
			return s, err
		}
		s.Answer = resp.Value
		return s, nil
	}
}

func TestHITL_MissingHandlerErrors(t *testing.T) {
	g := New[*hitlState]()
	must(t, g.AddNode("a", askNode("ask")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))
	cg := mustCompile(t, g)

	_, err := cg.Run(context.Background(), &hitlState{Ask: "hi"})
	if !errors.Is(err, ErrNoInterruptHandler) {
		t.Fatalf("got %v, want ErrNoInterruptHandler", err)
	}
}

func TestHITL_HandlerReceivesRequestAndResponseFlows(t *testing.T) {
	g := New[*hitlState]()
	must(t, g.AddNode("a", askNode("approve")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))
	cg := mustCompile(t, g)

	var gotReq InterruptRequest
	handler := func(_ context.Context, req InterruptRequest) (InterruptResponse, error) {
		gotReq = req
		return InterruptResponse{Value: "yes"}, nil
	}

	result, err := cg.Run(context.Background(), &hitlState{Ask: "ship it?"},
		WithInterruptHandler[*hitlState](handler))
	if err != nil {
		t.Fatal(err)
	}

	want := InterruptRequest{Node: "a", Kind: "approve", Payload: "ship it?"}
	if !reflect.DeepEqual(gotReq, want) {
		t.Fatalf("handler req = %+v, want %+v", gotReq, want)
	}
	if result.Answer != "yes" {
		t.Fatalf("answer = %v, want %q", result.Answer, "yes")
	}
}

func TestHITL_NodeFieldOverwrittenByRuntime(t *testing.T) {
	// Even if node code sets Node, the runtime overwrites it so the handler
	// always sees the real executing node.
	g := New[*hitlState]()
	must(t, g.AddNode("real", func(ctx context.Context, s *hitlState) (*hitlState, error) {
		_, err := Interrupt(ctx, InterruptRequest{Node: "lying", Kind: "x"})
		return s, err
	}))
	must(t, g.AddEdge(Start, "real"))
	must(t, g.AddEdge("real", End))
	cg := mustCompile(t, g)

	var seen string
	handler := func(_ context.Context, req InterruptRequest) (InterruptResponse, error) {
		seen = req.Node
		return InterruptResponse{}, nil
	}

	_, err := cg.Run(context.Background(), &hitlState{},
		WithInterruptHandler[*hitlState](handler))
	if err != nil {
		t.Fatal(err)
	}
	if seen != "real" {
		t.Fatalf("handler saw Node = %q, want %q", seen, "real")
	}
}

func TestHITL_HandlerErrorPropagates(t *testing.T) {
	sentinel := errors.New("no thanks")
	g := New[*hitlState]()
	must(t, g.AddNode("a", askNode("x")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))
	cg := mustCompile(t, g)

	handler := func(context.Context, InterruptRequest) (InterruptResponse, error) {
		return InterruptResponse{}, sentinel
	}

	_, err := cg.Run(context.Background(), &hitlState{},
		WithInterruptHandler[*hitlState](handler))
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want wrapped %v", err, sentinel)
	}
}

func TestHITL_HandlerHonorsContextCancel(t *testing.T) {
	g := New[*hitlState]()
	must(t, g.AddNode("a", askNode("x")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))
	cg := mustCompile(t, g)

	ctx, cancel := context.WithCancel(context.Background())

	handler := func(hctx context.Context, _ InterruptRequest) (InterruptResponse, error) {
		cancel()
		select {
		case <-hctx.Done():
			return InterruptResponse{}, hctx.Err()
		case <-make(chan struct{}): // never fires
			return InterruptResponse{}, nil
		}
	}

	_, err := cg.Run(ctx, &hitlState{},
		WithInterruptHandler[*hitlState](handler))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestHITL_MultipleInterruptsPerRun(t *testing.T) {
	g := New[*hitlState]()
	must(t, g.AddNode("a", askNode("first")))
	must(t, g.AddNode("b", askNode("second")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", "b"))
	must(t, g.AddEdge("b", End))
	cg := mustCompile(t, g)

	var seenNodes []string
	var seenKinds []string
	handler := func(_ context.Context, req InterruptRequest) (InterruptResponse, error) {
		seenNodes = append(seenNodes, req.Node)
		seenKinds = append(seenKinds, req.Kind)
		return InterruptResponse{Value: req.Node}, nil
	}

	_, err := cg.Run(context.Background(), &hitlState{},
		WithInterruptHandler[*hitlState](handler))
	if err != nil {
		t.Fatal(err)
	}
	assertValues(t, seenNodes, []string{"a", "b"})
	assertValues(t, seenKinds, []string{"first", "second"})
}

func TestHITL_MiddlewareWrapsInterruptingNode(t *testing.T) {
	g := New[*hitlState]()
	must(t, g.AddNode("a", askNode("x")))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))
	cg := mustCompile(t, g)

	var events []string
	mw := func(ctx context.Context, node string, s *hitlState, next NodeFunc[*hitlState]) (*hitlState, error) {
		events = append(events, "before:"+node)
		out, err := next(ctx, s)
		events = append(events, "after:"+node)
		return out, err
	}
	handler := func(context.Context, InterruptRequest) (InterruptResponse, error) {
		events = append(events, "handler")
		return InterruptResponse{Value: true}, nil
	}

	_, err := cg.Run(context.Background(), &hitlState{},
		WithMiddleware(mw),
		WithInterruptHandler[*hitlState](handler))
	if err != nil {
		t.Fatal(err)
	}
	assertValues(t, events, []string{"before:a", "handler", "after:a"})
}

// hitlSnap is a snap-capable state that also carries an interrupt answer.
type hitlSnap struct {
	Answer string
}

func (s *hitlSnap) MarshalBinary() ([]byte, error) {
	return []byte(s.Answer), nil
}
func (s *hitlSnap) UnmarshalBinary(data []byte) error {
	s.Answer = string(data)
	return nil
}

func TestHITL_CheckpointFiresAfterInterruptingNode(t *testing.T) {
	store := &MemoryStore{}
	g := New[*hitlSnap]()
	must(t, g.AddNode("ask", func(ctx context.Context, s *hitlSnap) (*hitlSnap, error) {
		resp, err := Interrupt(ctx, InterruptRequest{Kind: "ask"})
		if err != nil {
			return s, err
		}
		s.Answer = resp.Value.(string)
		return s, nil
	}))
	must(t, g.AddEdge(Start, "ask"))
	must(t, g.AddEdge("ask", End))
	cg, err := g.Compile(WithCheckpointing(store))
	if err != nil {
		t.Fatal(err)
	}

	handler := func(context.Context, InterruptRequest) (InterruptResponse, error) {
		return InterruptResponse{Value: "computed"}, nil
	}

	_, err = cg.Run(context.Background(), &hitlSnap{},
		WithThreadID[*hitlSnap]("t1"),
		WithInterruptHandler[*hitlSnap](handler))
	if err != nil {
		t.Fatal(err)
	}

	node, data, err := store.Load(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if node != "ask" {
		t.Fatalf("checkpoint node = %q, want %q", node, "ask")
	}
	// State at the checkpoint reflects the post-interrupt answer.
	var decoded hitlSnap
	if err := decoded.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}
	if decoded.Answer != "computed" {
		t.Fatalf("checkpoint answer = %q, want %q", decoded.Answer, "computed")
	}
}
