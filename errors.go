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
