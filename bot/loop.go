package bot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	// ErrInvalidLoop is returned for a nil task or non-positive interval.
	ErrInvalidLoop = errors.New("invalid task loop")
	// ErrLoopRunning is returned when Start is called on an active loop.
	ErrLoopRunning = errors.New("task loop is already running")
	// ErrLoopNotRunning is returned when an operation requires an active loop.
	ErrLoopNotRunning = errors.New("task loop is not running")
	// ErrLoopManaged is returned when a Bot already owns a loop.
	ErrLoopManaged = errors.New("task loop is already managed")
	// ErrLoopNotManaged is returned when a Bot does not own a loop.
	ErrLoopNotManaged = errors.New("task loop is not managed")
	errLoopStopped    = errors.New("task loop stopped")
)

// LoopFunc is one iteration of a background task.
type LoopFunc func(context.Context) error

// LoopHook runs before the first iteration.
type LoopHook func(context.Context) error

// LoopAfterHook runs once when a loop exits. The result is the error that
// caused the loop to exit, or nil after a graceful stop or completed count.
type LoopAfterHook func(context.Context, error) error

// LoopErrorHandler observes iteration errors before the loop either stops or
// continues according to WithContinueOnError.
type LoopErrorHandler func(context.Context, error) error

// LoopOption configures a Loop.
type LoopOption func(*Loop) error

// LoopPanicError wraps a panic recovered from a task or lifecycle hook.
type LoopPanicError struct {
	Stage string
	Value any
}

func (e *LoopPanicError) Error() string {
	return fmt.Sprintf("task loop %s panicked: %v", e.Stage, e.Value)
}

// Loop executes a task serially at a configurable interval. It is safe for
// concurrent lifecycle and status calls, and can be restarted after exit.
type Loop struct {
	mu sync.Mutex

	name            string
	interval        time.Duration
	count           uint64
	immediate       bool
	continueOnError bool
	task            LoopFunc
	before          LoopHook
	after           LoopAfterHook
	onError         LoopErrorHandler

	running    bool
	hasRun     bool
	stopping   bool
	iterations uint64
	nextRun    time.Time
	lastErr    error
	cancel     context.CancelCauseFunc
	stop       chan struct{}
	wake       chan struct{}
	done       chan struct{}
}

// NewLoop creates a serial task loop. By default, the first iteration runs
// immediately and any iteration error stops the loop.
func NewLoop(interval time.Duration, task LoopFunc, options ...LoopOption) (*Loop, error) {
	if interval <= 0 || task == nil {
		return nil, ErrInvalidLoop
	}
	loop := &Loop{interval: interval, immediate: true, task: task}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(loop); err != nil {
			return nil, err
		}
	}
	return loop, nil
}

// WithLoopName gives a loop a diagnostic name.
func WithLoopName(name string) LoopOption {
	return func(loop *Loop) error {
		loop.name = name
		return nil
	}
}

// WithLoopCount limits the number of attempted iterations. Zero means no
// limit.
func WithLoopCount(count uint64) LoopOption {
	return func(loop *Loop) error {
		loop.count = count
		return nil
	}
}

// WithImmediate controls whether the first iteration runs immediately.
func WithImmediate(immediate bool) LoopOption {
	return func(loop *Loop) error {
		loop.immediate = immediate
		return nil
	}
}

// WithBeforeLoop installs a hook that runs before the first iteration.
func WithBeforeLoop(hook LoopHook) LoopOption {
	return func(loop *Loop) error {
		loop.before = hook
		return nil
	}
}

// WithAfterLoop installs a hook that runs exactly once after loop execution.
func WithAfterLoop(hook LoopAfterHook) LoopOption {
	return func(loop *Loop) error {
		loop.after = hook
		return nil
	}
}

// WithLoopErrorHandler installs an iteration error observer. If the observer
// returns an error, the loop stops with both errors joined.
func WithLoopErrorHandler(handler LoopErrorHandler) LoopOption {
	return func(loop *Loop) error {
		loop.onError = handler
		return nil
	}
}

// WithContinueOnError controls whether handled iteration errors are followed
// by later iterations.
func WithContinueOnError(continueOnError bool) LoopOption {
	return func(loop *Loop) error {
		loop.continueOnError = continueOnError
		return nil
	}
}

// Start begins the loop in a goroutine.
func (l *Loop) Start(parent context.Context) error {
	prepared, err := l.prepareStart(parent)
	if err != nil {
		return err
	}
	prepared.launch()
	return nil
}

type preparedLoopRun struct {
	loop               *Loop
	ctx                context.Context
	stop               <-chan struct{}
	wake               <-chan struct{}
	done               chan struct{}
	previousHasRun     bool
	previousIterations uint64
	previousLastErr    error
	previousNextRun    time.Time
	previousStop       chan struct{}
	previousWake       chan struct{}
	previousDone       chan struct{}
}

func (l *Loop) prepareStart(parent context.Context) (*preparedLoopRun, error) {
	if l == nil || parent == nil {
		return nil, ErrInvalidLoop
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running {
		return nil, ErrLoopRunning
	}
	prepared := &preparedLoopRun{
		loop:               l,
		previousHasRun:     l.hasRun,
		previousIterations: l.iterations,
		previousLastErr:    l.lastErr,
		previousNextRun:    l.nextRun,
		previousStop:       l.stop,
		previousWake:       l.wake,
		previousDone:       l.done,
	}
	runCtx, cancel := context.WithCancelCause(parent)
	l.running = true
	l.hasRun = true
	l.stopping = false
	l.iterations = 0
	l.nextRun = time.Time{}
	l.lastErr = nil
	l.cancel = cancel
	l.stop = make(chan struct{})
	l.wake = make(chan struct{}, 1)
	l.done = make(chan struct{})
	prepared.ctx = runCtx
	prepared.stop = l.stop
	prepared.wake = l.wake
	prepared.done = l.done
	return prepared, nil
}

func (run *preparedLoopRun) launch() {
	go run.loop.execute(run.ctx, run.stop, run.wake, run.done)
}

func (run *preparedLoopRun) abort(cause error) {
	loop := run.loop
	loop.mu.Lock()
	if loop.done != run.done || !loop.running {
		loop.mu.Unlock()
		return
	}
	cancel := loop.cancel
	loop.hasRun = run.previousHasRun
	loop.iterations = run.previousIterations
	loop.lastErr = run.previousLastErr
	loop.running = false
	loop.stopping = false
	loop.nextRun = run.previousNextRun
	loop.cancel = nil
	loop.stop = run.previousStop
	loop.wake = run.previousWake
	loop.done = run.previousDone
	close(run.done)
	loop.mu.Unlock()
	cancel(cause)
}

// Run starts the loop and blocks until it exits or the waiting context ends.
func (l *Loop) Run(ctx context.Context) error {
	if err := l.Start(ctx); err != nil {
		return err
	}
	return l.Wait(ctx)
}

// Stop prevents another iteration and waits for the current iteration to
// finish without cancelling its context.
func (l *Loop) Stop(ctx context.Context) error {
	if l == nil || ctx == nil {
		return ErrInvalidLoop
	}
	l.mu.Lock()
	if !l.running {
		l.mu.Unlock()
		return ErrLoopNotRunning
	}
	if !l.stopping {
		close(l.stop)
		l.stopping = true
	}
	done := l.done
	l.mu.Unlock()
	return waitLoopDone(ctx, done, l)
}

// Cancel immediately cancels the active iteration. The optional cause is
// returned by Wait; context.Canceled is used when omitted or nil.
func (l *Loop) Cancel(cause ...error) error {
	if l == nil {
		return ErrInvalidLoop
	}
	l.mu.Lock()
	if !l.running {
		l.mu.Unlock()
		return ErrLoopNotRunning
	}
	cancel := l.cancel
	l.mu.Unlock()
	var cancellation error
	if len(cause) > 0 {
		cancellation = cause[0]
	}
	if cancellation == nil {
		cancellation = context.Canceled
	}
	cancel(cancellation)
	return nil
}

// Wait waits for the current or most recent run and returns its result.
func (l *Loop) Wait(ctx context.Context) error {
	if l == nil || ctx == nil {
		return ErrInvalidLoop
	}
	l.mu.Lock()
	if !l.hasRun {
		l.mu.Unlock()
		return ErrLoopNotRunning
	}
	done := l.done
	l.mu.Unlock()
	return waitLoopDone(ctx, done, l)
}

func waitLoopDone(ctx context.Context, done <-chan struct{}, loop *Loop) error {
	select {
	case <-done:
		loop.mu.Lock()
		err := loop.lastErr
		loop.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ChangeInterval changes the delay used before the next iteration. An active
// timer is recalculated from the time of this call.
func (l *Loop) ChangeInterval(interval time.Duration) error {
	if l == nil || interval <= 0 {
		return ErrInvalidLoop
	}
	l.mu.Lock()
	l.interval = interval
	wake := l.wake
	running := l.running
	l.mu.Unlock()
	if running {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// Name returns the diagnostic name.
func (l *Loop) Name() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.name
}

// IsRunning reports whether a run is active, including graceful shutdown.
func (l *Loop) IsRunning() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// CurrentLoop returns the number of attempted iterations in the current or
// most recent run.
func (l *Loop) CurrentLoop() uint64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.iterations
}

// NextRun returns the scheduled next iteration, or the zero time when none is
// currently scheduled.
func (l *Loop) NextRun() time.Time {
	if l == nil {
		return time.Time{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nextRun
}

// LastError returns the result from the most recently completed run.
func (l *Loop) LastError() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
}

// StartLoop starts a loop under the Bot lifecycle. Managed loops are stopped
// by CloseContext and cancelled if its shutdown deadline expires.
func (b *Bot) StartLoop(loop *Loop) error {
	if loop == nil {
		return ErrInvalidLoop
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBotClosed
	}
	if _, exists := b.loops[loop]; exists {
		b.mu.Unlock()
		return ErrLoopManaged
	}
	if _, reserved := b.reservedLoops[loop]; reserved {
		b.mu.Unlock()
		return ErrLoopManaged
	}
	if err := loop.Start(b.lifecycleCtx); err != nil {
		b.mu.Unlock()
		return err
	}
	b.loops[loop] = struct{}{}
	b.mu.Unlock()
	go b.watchLoop(loop)
	return nil
}

// StopLoop gracefully stops and releases a managed loop.
func (b *Bot) StopLoop(ctx context.Context, loop *Loop) error {
	if ctx == nil || loop == nil {
		return ErrInvalidLoop
	}
	b.mu.RLock()
	_, exists := b.loops[loop]
	b.mu.RUnlock()
	if !exists {
		return ErrLoopNotManaged
	}
	err := loop.Stop(ctx)
	if !loop.IsRunning() {
		b.mu.Lock()
		delete(b.loops, loop)
		b.mu.Unlock()
	}
	return err
}

// Loops returns the task loops currently managed by the Bot.
func (b *Bot) Loops() []*Loop {
	b.mu.RLock()
	loops := make([]*Loop, 0, len(b.loops))
	for loop := range b.loops {
		loops = append(loops, loop)
	}
	b.mu.RUnlock()
	sort.Slice(loops, func(i, j int) bool { return loops[i].Name() < loops[j].Name() })
	return loops
}

func (b *Bot) watchLoop(loop *Loop) {
	_ = loop.Wait(context.Background())
	b.mu.Lock()
	delete(b.loops, loop)
	b.mu.Unlock()
}

func (l *Loop) execute(ctx context.Context, stop <-chan struct{}, wake <-chan struct{}, done chan struct{}) {
	result := l.run(ctx, stop, wake)
	cleanupCtx := context.WithoutCancel(ctx)
	if l.after != nil {
		if err := invokeLoopAfter(l.after, cleanupCtx, result); err != nil {
			result = errors.Join(result, err)
		}
	}

	l.mu.Lock()
	cancel := l.cancel
	l.lastErr = result
	l.running = false
	l.stopping = false
	l.nextRun = time.Time{}
	l.cancel = nil
	close(done)
	l.mu.Unlock()
	cancel(nil)
}

func (l *Loop) run(ctx context.Context, stop <-chan struct{}, wake <-chan struct{}) error {
	select {
	case <-stop:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
	}
	if l.before != nil {
		if err := invokeLoopHook(l.before, ctx, "before hook"); err != nil {
			return err
		}
	}

	if !l.immediate {
		if err := l.waitInterval(ctx, stop, wake); err != nil {
			if errors.Is(err, errLoopStopped) {
				return nil
			}
			return err
		}
	}
	for {
		err := invokeLoopTask(l.task, ctx)
		l.mu.Lock()
		l.iterations++
		iteration := l.iterations
		count := l.count
		continueOnError := l.continueOnError
		onError := l.onError
		l.mu.Unlock()
		if err != nil {
			if onError != nil {
				if handlerErr := invokeLoopErrorHandler(onError, ctx, err); handlerErr != nil {
					return errors.Join(err, handlerErr)
				}
			}
			if !continueOnError {
				return err
			}
		}
		if count > 0 && iteration >= count {
			return nil
		}
		if err := l.waitInterval(ctx, stop, wake); err != nil {
			if errors.Is(err, errLoopStopped) {
				return nil
			}
			return err
		}
	}
}

func (l *Loop) waitInterval(ctx context.Context, stop <-chan struct{}, wake <-chan struct{}) error {
	for {
		l.mu.Lock()
		interval := l.interval
		next := time.Now().Add(interval)
		l.nextRun = next
		l.mu.Unlock()
		timer := time.NewTimer(time.Until(next))
		select {
		case <-timer.C:
			l.mu.Lock()
			l.nextRun = time.Time{}
			l.mu.Unlock()
			return nil
		case <-wake:
			stopAndDrainTimer(timer)
			continue
		case <-stop:
			stopAndDrainTimer(timer)
			return errLoopStopped
		case <-ctx.Done():
			stopAndDrainTimer(timer)
			return context.Cause(ctx)
		}
	}
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func invokeLoopTask(task LoopFunc, ctx context.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &LoopPanicError{Stage: "task", Value: recovered}
		}
	}()
	return task(ctx)
}

func invokeLoopHook(hook LoopHook, ctx context.Context, stage string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &LoopPanicError{Stage: stage, Value: recovered}
		}
	}()
	return hook(ctx)
}

func invokeLoopAfter(hook LoopAfterHook, ctx context.Context, result error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &LoopPanicError{Stage: "after hook", Value: recovered}
		}
	}()
	return hook(ctx, result)
}

func invokeLoopErrorHandler(handler LoopErrorHandler, ctx context.Context, taskErr error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &LoopPanicError{Stage: "error handler", Value: recovered}
		}
	}()
	return handler(ctx, taskErr)
}
