package shutdown

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestCoordinatorSuccess(t *testing.T) {
	c := New()
	executed := false

	c.Register("test_hook", func(ctx context.Context) error {
		executed = true
		return nil
	})

	// Trigger shutdown
	c.Trigger()
	c.Trigger() // Duplicate trigger should be no-op

	err := c.Run(1 * time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !executed {
		t.Fatal("expected hook to be executed")
	}
}

func TestCoordinatorHookError(t *testing.T) {
	c := New()
	expectedErr := errors.New("hook failed")

	c.Register("failing_hook", func(ctx context.Context) error {
		return expectedErr
	})

	c.Trigger()
	err := c.Run(100 * time.Millisecond)

	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}

func TestCoordinatorDefaultTimeout(t *testing.T) {
	c := New()
	c.Register("ok_hook", func(ctx context.Context) error {
		return nil
	})

	c.Trigger()
	err := c.Run(0) // Default timeout
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestTriggeredChannel: Triggered returns a channel that closes when
// shutdown is initiated.
func TestTriggeredChannel(t *testing.T) {
	c := New()

	ch := c.Triggered()
	select {
	case <-ch:
		t.Fatal("Triggered channel already closed before Trigger")
	default:
	}

	c.Trigger()

	select {
	case <-ch:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Triggered channel did not close after Trigger")
	}
}

// TestRunIgnoresContextCanceled: an error matching context.Canceled is
// not propagated as the firstErr.
func TestRunIgnoresContextCanceled(t *testing.T) {
	c := New()
	c.Register("ctx_cancel_hook", func(ctx context.Context) error {
		return context.Canceled
	})
	c.Register("ok_hook", func(ctx context.Context) error {
		return nil
	})
	c.Trigger()
	if err := c.Run(time.Second); err != nil {
		t.Errorf("Run returned %v, want nil (context.Canceled is ignored)", err)
	}
}

// TestRunNoHooks: Run with no registered hooks returns nil.
func TestRunNoHooks(t *testing.T) {
	c := New()
	c.Trigger()
	if err := c.Run(time.Second); err != nil {
		t.Errorf("Run (no hooks): %v", err)
	}
}

// TestWaitForSignalTriggersShutdown: WaitForSignal must invoke Trigger
// when SIGINT arrives.  Skipped on Windows where signals to the test
// process behave differently (os.Interrupt == os.Kill and CTRL_BREAK
// races).
func TestWaitForSignalTriggersShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping signal test on Windows")
	}
	c := New()

	// Send SIGINT to ourselves after a short delay so WaitForSignal
	// returns and Trigger fires.
	go func() {
		time.Sleep(20 * time.Millisecond)
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
	}()

	c.WaitForSignal()

	// After WaitForSignal returns, Triggered channel must be closed.
	select {
	case <-c.Triggered():
		// expected
	default:
		t.Errorf("Triggered channel not closed after WaitForSignal")
	}
}
