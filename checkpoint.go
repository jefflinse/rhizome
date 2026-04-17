package rhizome

import (
	"context"
	"encoding"
	"sync"
)

// Snapshotter is implemented by state types that can be checkpointed.
// It composes the standard encoding.BinaryMarshaler and BinaryUnmarshaler
// interfaces, so a type that already implements those for other reasons
// satisfies Snapshotter automatically.
//
// Because UnmarshalBinary must mutate its receiver, Snapshotter is
// typically satisfied by a pointer type (e.g., S = *MyState).
type Snapshotter interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

// CheckpointStore persists graph state between node executions so that a
// run can be resumed later, possibly in a different process.
//
// Save records the state produced by nodeName for threadID. Load returns
// the last saved node name and state bytes for threadID, or
// ErrNoCheckpoint if the thread is unknown.
//
// Implementations must be safe for concurrent use.
type CheckpointStore interface {
	Save(ctx context.Context, threadID, nodeName string, data []byte) error
	Load(ctx context.Context, threadID string) (nodeName string, data []byte, err error)
}

// WithCheckpointing enables persistence of state after every node execution.
// The state type S must satisfy Snapshotter; if it does not, Compile returns
// an error wrapping ErrCheckpointRequiresSnapshotter.
func WithCheckpointing(store CheckpointStore) CompileOption {
	return func(c *compileConfig) {
		c.checkpointStore = store
	}
}

// WithThreadID sets the identifier under which a Run or Resume records
// checkpoints. It is required when the compiled graph was built with
// WithCheckpointing and ignored otherwise.
func WithThreadID[S any](id string) RunOption[S] {
	return func(cfg *runConfig[S]) {
		cfg.threadID = id
	}
}

// MemoryStore is an in-memory CheckpointStore suitable for tests and
// ephemeral single-process use. The zero value is ready to use.
type MemoryStore struct {
	mu    sync.Mutex
	items map[string]memoryCheckpoint
}

type memoryCheckpoint struct {
	nodeName string
	data     []byte
}

// Save records the checkpoint for threadID, overwriting any previous entry.
func (m *MemoryStore) Save(_ context.Context, threadID, nodeName string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = make(map[string]memoryCheckpoint)
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	m.items[threadID] = memoryCheckpoint{nodeName: nodeName, data: buf}
	return nil
}

// Load returns the most recent checkpoint for threadID, or ErrNoCheckpoint
// if no checkpoint has been recorded.
func (m *MemoryStore) Load(_ context.Context, threadID string) (string, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp, ok := m.items[threadID]
	if !ok {
		return "", nil, ErrNoCheckpoint
	}
	out := make([]byte, len(cp.data))
	copy(out, cp.data)
	return cp.nodeName, out, nil
}
