package daggo

import "errors"

// Construction errors (returned by AddNode, AddEdge, AddConditionalEdge, Compile).
var (
	ErrDuplicateNode   = errors.New("daggo: duplicate node")
	ErrNodeNotFound    = errors.New("daggo: node not found")
	ErrReservedName    = errors.New("daggo: reserved name")
	ErrDuplicateEdge   = errors.New("daggo: duplicate edge")
	ErrConflictingEdge = errors.New("daggo: conflicting edge")
	ErrNoEntrypoint    = errors.New("daggo: no entrypoint")
	ErrUnreachableNode = errors.New("daggo: unreachable node")
)

// Runtime errors (returned by Run).
var (
	ErrCycleLimit   = errors.New("daggo: node exceeded max execution count")
	ErrInvalidRoute = errors.New("daggo: routed to unknown node")
)
