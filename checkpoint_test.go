package rhizome

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
)

// snapState is a checkpoint-capable test state. Persisted form is JSON.
type snapState struct {
	Values []string `json:"values"`
}

func (s *snapState) MarshalBinary() ([]byte, error)    { return json.Marshal(s) }
func (s *snapState) UnmarshalBinary(data []byte) error { return json.Unmarshal(data, s) }

func appendSnap(v string) NodeFunc[*snapState] {
	return func(_ context.Context, s *snapState) (*snapState, error) {
		s.Values = append(s.Values, v)
		return s, nil
	}
}

// flakey runs as a regular node but returns an error the first failUntil
// invocations. Used to simulate a crash between checkpoints.
type flakey struct {
	v         string
	failUntil int32
	attempts  atomic.Int32
}

func (f *flakey) run(_ context.Context, s *snapState) (*snapState, error) {
	if f.attempts.Add(1) <= f.failUntil {
		return s, errors.New("simulated crash")
	}
	s.Values = append(s.Values, f.v)
	return s, nil
}

// threeNodeGraph wires a -> b -> c -> End with the given node fns.
func threeNodeGraph(t *testing.T, a, b, c NodeFunc[*snapState], opts ...CompileOption) *CompiledGraph[*snapState] {
	t.Helper()
	g := New[*snapState]()
	must(t, g.AddNode("a", a))
	must(t, g.AddNode("b", b))
	must(t, g.AddNode("c", c))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", "b"))
	must(t, g.AddEdge("b", "c"))
	must(t, g.AddEdge("c", End))
	compiled, err := g.Compile(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestCheckpointing_RoundTrip(t *testing.T) {
	store := &MemoryStore{}
	cg := threeNodeGraph(t, appendSnap("a"), appendSnap("b"), appendSnap("c"), WithCheckpointing(store))

	result, err := cg.Run(context.Background(), &snapState{}, WithThreadID[*snapState]("t1"))
	if err != nil {
		t.Fatal(err)
	}
	assertValues(t, result.Values, []string{"a", "b", "c"})

	// Final checkpoint should be for "c" with the full state.
	node, data, err := store.Load(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if node != "c" {
		t.Fatalf("last node = %q, want %q", node, "c")
	}
	var decoded snapState
	if err := decoded.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}
	assertValues(t, decoded.Values, []string{"a", "b", "c"})
}

func TestCheckpointing_CompileRejectsNonSnapshotter(t *testing.T) {
	g := New[int]()
	must(t, g.AddNode("a", func(_ context.Context, n int) (int, error) { return n + 1, nil }))
	must(t, g.AddEdge(Start, "a"))
	must(t, g.AddEdge("a", End))

	_, err := g.Compile(WithCheckpointing(&MemoryStore{}))
	if !errors.Is(err, ErrCheckpointRequiresSnapshotter) {
		t.Fatalf("got %v, want ErrCheckpointRequiresSnapshotter", err)
	}
}

func TestCheckpointing_RunRequiresThreadID(t *testing.T) {
	cg := threeNodeGraph(t, appendSnap("a"), appendSnap("b"), appendSnap("c"), WithCheckpointing(&MemoryStore{}))

	_, err := cg.Run(context.Background(), &snapState{})
	if !errors.Is(err, ErrThreadIDRequired) {
		t.Fatalf("got %v, want ErrThreadIDRequired", err)
	}
}

func TestCheckpointing_WithoutStoreIgnoresThreadID(t *testing.T) {
	cg := threeNodeGraph(t, appendSnap("a"), appendSnap("b"), appendSnap("c"))

	result, err := cg.Run(context.Background(), &snapState{}, WithThreadID[*snapState]("anything"))
	if err != nil {
		t.Fatal(err)
	}
	assertValues(t, result.Values, []string{"a", "b", "c"})
}

func TestCheckpointing_ResumeAfterCrash(t *testing.T) {
	store := &MemoryStore{}
	flakeyB := &flakey{v: "b", failUntil: 1}

	cg := threeNodeGraph(t, appendSnap("a"), flakeyB.run, appendSnap("c"), WithCheckpointing(store))

	// First run: a succeeds, b fails, c never runs.
	_, err := cg.Run(context.Background(), &snapState{}, WithThreadID[*snapState]("t1"))
	if err == nil {
		t.Fatal("expected crash error from run 1")
	}

	// Store holds the checkpoint from "a"; "b" never saved.
	node, _, err := store.Load(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if node != "a" {
		t.Fatalf("checkpoint node = %q, want %q", node, "a")
	}

	// Resume: "b" succeeds on retry, "c" runs, final state has all three.
	resumed, err := cg.Resume(context.Background(), "t1", &snapState{})
	if err != nil {
		t.Fatal(err)
	}
	assertValues(t, resumed.Values, []string{"a", "b", "c"})
}

func TestCheckpointing_ResumeUnknownThread(t *testing.T) {
	store := &MemoryStore{}
	cg := threeNodeGraph(t, appendSnap("a"), appendSnap("b"), appendSnap("c"), WithCheckpointing(store))

	_, err := cg.Resume(context.Background(), "never-existed", &snapState{})
	if !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("got %v, want ErrNoCheckpoint", err)
	}
}

func TestCheckpointing_ResumeRequiresCheckpointing(t *testing.T) {
	cg := threeNodeGraph(t, appendSnap("a"), appendSnap("b"), appendSnap("c"))

	_, err := cg.Resume(context.Background(), "t1", &snapState{})
	if !errors.Is(err, ErrCheckpointingDisabled) {
		t.Fatalf("got %v, want ErrCheckpointingDisabled", err)
	}
}

func TestCheckpointing_ResumeRequiresThreadID(t *testing.T) {
	store := &MemoryStore{}
	cg := threeNodeGraph(t, appendSnap("a"), appendSnap("b"), appendSnap("c"), WithCheckpointing(store))

	_, err := cg.Resume(context.Background(), "", &snapState{})
	if !errors.Is(err, ErrThreadIDRequired) {
		t.Fatalf("got %v, want ErrThreadIDRequired", err)
	}
}

type failingStore struct{ saveErr error }

func (f *failingStore) Save(_ context.Context, _, _ string, _ []byte) error {
	return f.saveErr
}
func (f *failingStore) Load(_ context.Context, _ string) (string, []byte, error) {
	return "", nil, ErrNoCheckpoint
}

func TestCheckpointing_SaveErrorFailsRun(t *testing.T) {
	sentinel := errors.New("boom")
	store := &failingStore{saveErr: sentinel}
	cg := threeNodeGraph(t, appendSnap("a"), appendSnap("b"), appendSnap("c"), WithCheckpointing(store))

	_, err := cg.Run(context.Background(), &snapState{}, WithThreadID[*snapState]("t1"))
	if err == nil {
		t.Fatal("expected error from save failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want wrapped %v", err, sentinel)
	}
}

func TestCheckpointing_MiddlewareFiresOnResume(t *testing.T) {
	store := &MemoryStore{}
	flakeyB := &flakey{v: "b", failUntil: 1}
	cg := threeNodeGraph(t, appendSnap("a"), flakeyB.run, appendSnap("c"), WithCheckpointing(store))

	// First run crashes at b.
	_, _ = cg.Run(context.Background(), &snapState{}, WithThreadID[*snapState]("t1"))

	var seen []string
	mw := func(ctx context.Context, node string, s *snapState, next NodeFunc[*snapState]) (*snapState, error) {
		seen = append(seen, node)
		return next(ctx, s)
	}

	_, err := cg.Resume(context.Background(), "t1", &snapState{}, WithMiddleware(mw))
	if err != nil {
		t.Fatal(err)
	}
	assertValues(t, seen, []string{"b", "c"})
}
