package rhizome

import "context"

// InterruptRequest is passed from a node to the configured InterruptHandler.
// Node is set by the runtime before the handler is called, so node code may
// leave it blank. Kind and Payload are consumer-defined: Kind is a
// discriminator the handler can switch on, and Payload carries whatever
// data the handler needs to produce a response.
type InterruptRequest struct {
	Node    string
	Kind    string
	Payload any
}

// InterruptResponse is returned by the handler to resume the paused node.
// Value is consumer-defined; the node type-asserts it into whatever shape
// it expects.
type InterruptResponse struct {
	Value any
}

// InterruptHandler is invoked when a node calls Interrupt. It blocks the
// graph's goroutine until it returns, so implementations must honor ctx
// cancellation — any blocking call (channel recv, network IO, stdin read)
// should select on ctx.Done().
type InterruptHandler func(ctx context.Context, req InterruptRequest) (InterruptResponse, error)

// interruptContextKey is the private key under which the runtime stashes
// the binding (handler + current node name) on the context passed to
// each node invocation.
type interruptContextKey struct{}

// interruptBinding couples the handler with the currently-executing node
// name so Interrupt can populate InterruptRequest.Node automatically.
type interruptBinding struct {
	handler InterruptHandler
	node    string
}

// Interrupt pauses the current node by invoking the InterruptHandler
// configured on the Run. It blocks until the handler returns. If no
// handler was configured it returns ErrNoInterruptHandler.
//
// The runtime overwrites InterruptRequest.Node with the actual executing
// node name, so node code may leave that field blank.
func Interrupt(ctx context.Context, req InterruptRequest) (InterruptResponse, error) {
	b, ok := ctx.Value(interruptContextKey{}).(interruptBinding)
	if !ok {
		return InterruptResponse{}, ErrNoInterruptHandler
	}
	req.Node = b.node
	return b.handler(ctx, req)
}

// WithInterruptHandler registers the handler invoked when a node calls
// Interrupt. Without this option, Interrupt returns ErrNoInterruptHandler.
func WithInterruptHandler[S any](h InterruptHandler) RunOption[S] {
	return func(cfg *runConfig[S]) {
		cfg.interruptHandler = h
	}
}
