package daggo

import "errors"

var (
	ErrDuplicateNode   = errors.New("daggo: duplicate node")
	ErrNodeNotFound    = errors.New("daggo: node not found")
	ErrReservedName    = errors.New("daggo: reserved name")
	ErrDuplicateEdge   = errors.New("daggo: duplicate edge")
	ErrConflictingEdge = errors.New("daggo: conflicting edge")
	ErrNoEntrypoint    = errors.New("daggo: no entrypoint")
	ErrUnreachableNode = errors.New("daggo: unreachable node")
	ErrCycleLimit      = errors.New("daggo: node exceeded max execution count")
	ErrInvalidRoute    = errors.New("daggo: routed to unknown node")
)
