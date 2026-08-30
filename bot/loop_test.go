package bot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoopRunsHooksAndCount(t *testing.T) {
	var calls []string
	loop, err := NewLoop(time.Millisecond, func(context.Context) error {
		calls = append(calls, "task")
		return nil
	},
		WithLoopName("refresh"),
		WithLoopCount(3),
		WithBeforeLoop(func(context.Context) error {
			calls = append(calls, "before")
			return nil
		}),
		WithAfterLoop(func(_ context.Context, result error) error {
			if result != nil {
				return fmt.Errorf("unexpected loop result: %w", result)
			}
			calls = append(calls, "after")
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if loop.Name() != "refresh" {
		t.Fatalf("Name = %q", loop.Name())
	}
	if err := loop.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := loop.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"before", "task", "task", "task", "after"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if loop.CurrentLoop() != 3 || loop.IsRunning() || loop.LastError() != nil || !loop.NextRun().IsZero() {
		t.Fatalf("unexpected final status: iterations=%d running=%v error=%v next=%v", loop.CurrentLoop(), loop.IsRunning(), loop.LastError(), loop.NextRun())
	}
}

func TestLoopStopIsGraceful(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var canceled atomic.Bool
	loop, err := NewLoop(time.Hour, func(ctx context.Context) error {
		close(started)
		select {
		case <-ctx.Done():
			canceled.Store(true)
		case <-release:
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	stopped := make(chan error, 1)
	go func() { stopped <- loop.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before current iteration completed: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if canceled.Load() || loop.CurrentLoop() != 1 {
		t.Fatalf("graceful stop canceled=%v iterations=%d", canceled.Load(), loop.CurrentLoop())
	}
}

func TestLoopCancelReturnsCause(t *testing.T) {
	started := make(chan struct{})
	loop, err := NewLoop(time.Hour, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return context.Cause(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	want := errors.New("shutdown")
	if err := loop.Cancel(want); err != nil {
		t.Fatal(err)
	}
	if err := loop.Wait(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Wait error = %v", err)
	}
}

func TestLoopCanContinueAfterErrors(t *testing.T) {
	want := errors.New("temporary")
	var handled int
	loop, err := NewLoop(time.Millisecond, func(context.Context) error { return want },
		WithLoopCount(2),
		WithContinueOnError(true),
		WithLoopErrorHandler(func(_ context.Context, err error) error {
			if !errors.Is(err, want) {
				return fmt.Errorf("unexpected handler error: %w", err)
			}
			handled++
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if handled != 2 || loop.CurrentLoop() != 2 {
		t.Fatalf("handled/iterations = %d/%d", handled, loop.CurrentLoop())
	}
}

func TestLoopChangeIntervalWakesTimer(t *testing.T) {
	ran := make(chan struct{})
	loop, err := NewLoop(time.Hour, func(context.Context) error {
		close(ran)
		return nil
	}, WithImmediate(false), WithLoopCount(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := loop.ChangeInterval(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("changed interval did not wake timer")
	}
	if err := loop.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLoopRecoversPanicAndCanRestart(t *testing.T) {
	var panicOnce atomic.Bool
	loop, err := NewLoop(time.Millisecond, func(context.Context) error {
		if !panicOnce.Swap(true) {
			panic("boom")
		}
		return nil
	}, WithLoopCount(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := loop.Start(context.Background()); !errors.Is(err, ErrLoopRunning) {
		t.Fatalf("second Start error = %v", err)
	}
	var panicErr *LoopPanicError
	if err := loop.Wait(context.Background()); !errors.As(err, &panicErr) {
		t.Fatalf("panic result = %v", err)
	}
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("restart error = %v", err)
	}
}

func TestLoopValidationAndWaitTimeout(t *testing.T) {
	if _, err := NewLoop(0, func(context.Context) error { return nil }); !errors.Is(err, ErrInvalidLoop) {
		t.Fatalf("invalid interval error = %v", err)
	}
	loop, err := NewLoop(time.Hour, func(context.Context) error { return nil }, WithImmediate(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.Wait(context.Background()); !errors.Is(err, ErrLoopNotRunning) {
		t.Fatalf("Wait before Start error = %v", err)
	}
	if err := loop.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := loop.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed Wait error = %v", err)
	}
	if err := loop.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBotManagesLoopLifecycle(t *testing.T) {
	framework := newTestBot(t)
	started := make(chan struct{})
	release := make(chan struct{})
	loop, err := NewLoop(time.Hour, func(context.Context) error {
		close(started)
		<-release
		return nil
	}, WithLoopName("managed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := framework.StartLoop(loop); err != nil {
		t.Fatal(err)
	}
	if err := framework.StartLoop(loop); !errors.Is(err, ErrLoopManaged) {
		t.Fatalf("second StartLoop error = %v", err)
	}
	if got := framework.Loops(); len(got) != 1 || got[0] != loop {
		t.Fatalf("Loops = %v", got)
	}
	<-started
	closed := make(chan error, 1)
	go func() { closed <- framework.CloseContext(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("CloseContext returned before current task completed: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if loop.IsRunning() {
		t.Fatal("managed loop remained active after CloseContext")
	}
	if err := framework.StartLoop(loop); !errors.Is(err, ErrBotClosed) {
		t.Fatalf("StartLoop after close error = %v", err)
	}
}

func TestBotCloseDeadlineCancelsManagedLoop(t *testing.T) {
	framework := newTestBot(t)
	started := make(chan struct{})
	finished := make(chan struct{})
	loop, err := NewLoop(time.Hour, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return context.Cause(ctx)
	}, WithAfterLoop(func(context.Context, error) error {
		close(finished)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := framework.StartLoop(loop); err != nil {
		t.Fatal(err)
	}
	<-started
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := framework.CloseContext(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("loop did not observe forced cancellation")
	}
	if err := loop.LastError(); !errors.Is(err, ErrBotClosed) {
		t.Fatalf("loop cancellation cause = %v", err)
	}
}

func TestBotStopLoopReleasesOwnership(t *testing.T) {
	framework := newTestBot(t)
	loop, err := NewLoop(time.Hour, func(context.Context) error { return nil }, WithImmediate(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := framework.StopLoop(context.Background(), loop); !errors.Is(err, ErrLoopNotManaged) {
		t.Fatalf("unmanaged StopLoop error = %v", err)
	}
	if err := framework.StartLoop(loop); err != nil {
		t.Fatal(err)
	}
	if err := framework.StopLoop(context.Background(), loop); err != nil {
		t.Fatal(err)
	}
	if len(framework.Loops()) != 0 {
		t.Fatalf("Loops after StopLoop = %v", framework.Loops())
	}
}
