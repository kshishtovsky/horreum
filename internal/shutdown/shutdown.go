// Package shutdown coordinates graceful shutdown of multiple
// long-lived components in response to OS signals.
//
// Typical usage:
//
//	coord := shutdown.New(30 * time.Second)
//	coord.Register("metrics", metricsSrv.Shutdown)
//	coord.Register("server", apiSrv.Shutdown)
//	go coord.WaitForSignal() // blocks until SIGINT/SIGTERM
//	coord.Run(context.Background())
package shutdown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Coordinator owns a list of named shutdown hooks that run
// sequentially in registration order on Shutdown.
type Coordinator struct {
	mu     sync.Mutex
	hooks  []hook
	closed bool
	done   chan struct{}
}

type hook struct {
	name string
	fn   func(ctx context.Context) error
}

// New returns a fresh Coordinator.
func New() *Coordinator {
	return &Coordinator{done: make(chan struct{})}
}

// Register adds a shutdown hook.  Hooks run in registration order.
func (c *Coordinator) Register(name string, fn func(ctx context.Context) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hooks = append(c.hooks, hook{name: name, fn: fn})
}

// Triggered returns a channel that closes when shutdown has been
// initiated (by signal or by explicit Trigger).  Components can
// listen on this channel and start draining themselves.
func (c *Coordinator) Triggered() <-chan struct{} { return c.done }

// Trigger initiates shutdown programmatically.  Safe to call
// multiple times — only the first call has any effect.
func (c *Coordinator) Trigger() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.done)
}

// WaitForSignal blocks until SIGINT/SIGTERM is received, then calls
// Trigger.
func (c *Coordinator) WaitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	c.Trigger()
}

// Run executes all registered hooks in order.  Each hook is bounded
// by timeout.  Returns the first error encountered.  Safe to call
// from any goroutine.
func (c *Coordinator) Run(timeout time.Duration) error {
	<-c.Triggered()

	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var firstErr error
	for _, h := range c.hooks {
		fmt.Fprintf(os.Stderr, "[shutdown] %s ...\n", h.name)
		if err := h.fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "[shutdown] %s: %v\n", h.name, err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			fmt.Fprintf(os.Stderr, "[shutdown] %s: ok\n", h.name)
		}
	}
	return firstErr
}
