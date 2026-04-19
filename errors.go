package rhizome

import "errors"

// Construction errors (returned by AddNode, AddEdge, AddConditionalEdge, Compile).
var (
	ErrDuplicateNode   = errors.New("rhizome: duplicate node")
	ErrNodeNotFound    = errors.New("rhizome: node not found")
	ErrReservedName    = errors.New("rhizome: reserved name")
	ErrDuplicateEdge   = errors.New("rhizome: duplicate edge")
	ErrConflictingEdge = errors.New("rhizome: conflicting edge")
	ErrNoEntrypoint    = errors.New("rhizome: no entrypoint")
	ErrUnreachableNode = errors.New("rhizome: unreachable node")
	ErrNoOutgoingEdge  = errors.New("rhizome: node has no outgoing edge")
	ErrNoTargets       = errors.New("rhizome: conditional edge requires at least one target")
)

// Runtime errors (returned by Run).
var (
	ErrCycleLimit       = errors.New("rhizome: node exceeded max execution count")
	ErrInvalidRoute     = errors.New("rhizome: router returned invalid route")
	ErrUndeclaredTarget = errors.New("rhizome: router returned undeclared target")
)

// Checkpointing errors.
var (
	ErrCheckpointRequiresSnapshotter = errors.New("rhizome: checkpointing requires state to implement Snapshotter")
	ErrThreadIDRequired              = errors.New("rhizome: thread ID required when checkpointing is enabled")
	ErrNoCheckpoint                  = errors.New("rhizome: no checkpoint found")
	ErrCheckpointingDisabled         = errors.New("rhizome: checkpointing is not enabled on this graph")
)

// Human-in-the-loop errors.
var (
	ErrNoInterruptHandler = errors.New("rhizome: no interrupt handler configured for this run")
)
