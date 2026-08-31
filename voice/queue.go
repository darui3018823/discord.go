package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"
)

var (
	// ErrQueueRunning is returned when Start is called twice.
	ErrQueueRunning = errors.New("voice queue is already running")
	// ErrQueueNotRunning is returned when an operation requires a running queue.
	ErrQueueNotRunning = errors.New("voice queue is not running")
	// ErrQueueNotPlaying is returned when pause, resume, or skip has no current source.
	ErrQueueNotPlaying = errors.New("voice queue is not playing")
	// ErrQueueDraining is returned when enqueue races with a requested drain.
	ErrQueueDraining = errors.New("voice queue is draining")
	// ErrSourceChannelMismatch is returned when source and player layouts differ.
	ErrSourceChannelMismatch = errors.New("PCM source channel count does not match player")
	// ErrTrackSkipped is the cancellation cause used by Skip.
	ErrTrackSkipped = errors.New("voice track was skipped")
	// ErrQueueStopped is the cancellation cause used by Stop.
	ErrQueueStopped = errors.New("voice queue was stopped")
)

// TrackResult describes one source after playback and Close have completed.
type TrackResult struct {
	Source  PCMSource
	Err     error
	Skipped bool
}

// TrackEndHandler observes source completion. Returning an error follows the
// queue's continue-on-error policy just like a source failure.
type TrackEndHandler func(TrackResult) error

// QueueOption configures a Queue.
type QueueOption func(*Queue) error

// QueuePanicError wraps a panic recovered from a track-end handler.
type QueuePanicError struct{ Value any }

func (e *QueuePanicError) Error() string {
	return fmt.Sprintf("voice track-end handler panicked: %v", e.Value)
}

// SourcePanicError wraps a panic recovered at a third-party source boundary.
type SourcePanicError struct {
	Stage string
	Value any
}

func (e *SourcePanicError) Error() string {
	return fmt.Sprintf("voice source %s panicked: %v", e.Stage, e.Value)
}

// Queue serially consumes PCM sources into a Player. Enqueued sources are
// owned by the Queue and closed after playback, removal, or shutdown when they
// implement io.Closer.
type Queue struct {
	player *Player

	mu              sync.Mutex
	pending         []*queuedSource
	current         *queuedSource
	trackCancel     context.CancelCauseFunc
	paused          bool
	running         bool
	hasRun          bool
	draining        bool
	continueOnError bool
	reconnectDelay  time.Duration
	onTrackEnd      TrackEndHandler
	cancel          context.CancelCauseFunc
	wake            chan struct{}
	done            chan struct{}
	lastErr         error
	lastTrackErr    error
}

type queuedSource struct {
	source   PCMSource
	once     sync.Once
	closeErr error
}

func (s *queuedSource) close() error {
	s.once.Do(func() {
		if closer, ok := s.source.(io.Closer); ok {
			s.closeErr = invokeSourceClose(closer)
		}
	})
	return s.closeErr
}

// NewQueue creates a persistent source queue. Start begins its worker; Drain
// finishes after currently queued work, while Stop cancels immediately.
func NewQueue(player *Player, options ...QueueOption) (*Queue, error) {
	if player == nil {
		return nil, errors.New("player must not be nil")
	}
	queue := &Queue{player: player, continueOnError: true, reconnectDelay: 100 * time.Millisecond}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(queue); err != nil {
			return nil, err
		}
	}
	return queue, nil
}

// WithTrackEndHandler installs a serial track completion callback.
func WithTrackEndHandler(handler TrackEndHandler) QueueOption {
	return func(queue *Queue) error {
		queue.onTrackEnd = handler
		return nil
	}
}

// WithQueueContinueOnError controls whether source and callback errors skip to
// the next source. It defaults to true.
func WithQueueContinueOnError(continueOnError bool) QueueOption {
	return func(queue *Queue) error {
		queue.continueOnError = continueOnError
		return nil
	}
}

// WithReconnectDelay changes how frequently a queued frame retries while the
// Discord voice transport is reconnecting.
func WithReconnectDelay(delay time.Duration) QueueOption {
	return func(queue *Queue) error {
		if delay <= 0 {
			return errors.New("voice reconnect delay must be positive")
		}
		queue.reconnectDelay = delay
		return nil
	}
}

// Player returns the underlying Opus player.
func (q *Queue) Player() *Player { return q.player }

// Enqueue atomically appends sources after validating their channel layouts.
func (q *Queue) Enqueue(sources ...PCMSource) error {
	if q == nil {
		return ErrInvalidSource
	}
	for _, source := range sources {
		if isNilSource(source) {
			return ErrInvalidSource
		}
		channels, err := sourceChannelCount(source)
		if err != nil {
			return err
		}
		if channels != q.player.Channels() {
			return fmt.Errorf("%w: source=%d player=%d", ErrSourceChannelMismatch, channels, q.player.Channels())
		}
	}
	q.mu.Lock()
	if q.draining {
		q.mu.Unlock()
		return ErrQueueDraining
	}
	for _, source := range sources {
		q.pending = append(q.pending, &queuedSource{source: source})
	}
	wake := q.wake
	running := q.running
	q.mu.Unlock()
	if running {
		notifyQueue(wake)
	}
	return nil
}

// Start starts the queue worker. Sources may be enqueued before or after it.
func (q *Queue) Start(parent context.Context) error {
	if q == nil || parent == nil {
		return ErrQueueNotRunning
	}
	if err := parent.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	if q.running {
		q.mu.Unlock()
		return ErrQueueRunning
	}
	runCtx, cancel := context.WithCancelCause(parent)
	q.running = true
	q.hasRun = true
	q.draining = false
	q.paused = false
	q.lastErr = nil
	q.lastTrackErr = nil
	q.cancel = cancel
	q.wake = make(chan struct{}, 1)
	q.done = make(chan struct{})
	wake := q.wake
	done := q.done
	q.mu.Unlock()
	go q.execute(runCtx, wake, done)
	return nil
}

// Run starts the worker and waits for it. Since an open queue waits for future
// sources, call Drain or cancel ctx to finish it.
func (q *Queue) Run(ctx context.Context) error {
	if err := q.Start(ctx); err != nil {
		return err
	}
	return q.Wait(ctx)
}

// Drain stops accepting new sources and waits for current and queued playback.
func (q *Queue) Drain(ctx context.Context) error {
	if q == nil || ctx == nil {
		return ErrQueueNotRunning
	}
	q.mu.Lock()
	if !q.running {
		if q.hasRun && len(q.pending) == 0 {
			err := q.lastErr
			q.mu.Unlock()
			return err
		}
		q.mu.Unlock()
		return ErrQueueNotRunning
	}
	q.draining = true
	wake := q.wake
	done := q.done
	q.mu.Unlock()
	notifyQueue(wake)
	return q.wait(ctx, done)
}

// Stop cancels current playback, discards and closes pending sources, and
// waits for the worker. A context deadline bounds the wait.
func (q *Queue) Stop(ctx context.Context) error {
	if q == nil || ctx == nil {
		return ErrQueueNotRunning
	}
	q.mu.Lock()
	if !q.running {
		q.mu.Unlock()
		return ErrQueueNotRunning
	}
	q.draining = true
	cancel := q.cancel
	current := q.current
	pending := q.pending
	q.pending = nil
	done := q.done
	q.mu.Unlock()
	cancel(ErrQueueStopped)
	var closeErrors []error
	if current != nil {
		if err := current.close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	for _, source := range pending {
		if err := source.close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(q.wait(ctx, done), errors.Join(closeErrors...))
}

// Wait waits for the current or most recent run.
func (q *Queue) Wait(ctx context.Context) error {
	if q == nil || ctx == nil {
		return ErrQueueNotRunning
	}
	q.mu.Lock()
	if !q.hasRun {
		q.mu.Unlock()
		return ErrQueueNotRunning
	}
	done := q.done
	q.mu.Unlock()
	return q.wait(ctx, done)
}

func (q *Queue) wait(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		q.mu.Lock()
		err := q.lastErr
		q.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Pause suspends reads and sends for the current source.
func (q *Queue) Pause() error {
	if q == nil {
		return ErrQueueNotPlaying
	}
	q.mu.Lock()
	if !q.running || q.current == nil {
		q.mu.Unlock()
		return ErrQueueNotPlaying
	}
	q.paused = true
	q.mu.Unlock()
	return nil
}

// Resume continues a paused source.
func (q *Queue) Resume() error {
	if q == nil {
		return ErrQueueNotPlaying
	}
	q.mu.Lock()
	if !q.running || q.current == nil || !q.paused {
		q.mu.Unlock()
		return ErrQueueNotPlaying
	}
	q.paused = false
	wake := q.wake
	q.mu.Unlock()
	notifyQueue(wake)
	return nil
}

// Skip cancels and closes the current source, then advances to the next one.
func (q *Queue) Skip() error {
	if q == nil {
		return ErrQueueNotPlaying
	}
	q.mu.Lock()
	if !q.running || q.current == nil || q.trackCancel == nil {
		q.mu.Unlock()
		return ErrQueueNotPlaying
	}
	cancel := q.trackCancel
	current := q.current
	q.mu.Unlock()
	cancel(ErrTrackSkipped)
	return current.close()
}

// Clear removes and closes sources that have not started yet.
func (q *Queue) Clear() error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	pending := q.pending
	q.pending = nil
	q.mu.Unlock()
	var closeErrors []error
	for _, source := range pending {
		if err := source.close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

// Len returns the number of sources waiting behind the current source.
func (q *Queue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// Current returns the active source, if any.
func (q *Queue) Current() PCMSource {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == nil {
		return nil
	}
	return q.current.source
}

func (q *Queue) IsRunning() bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.running
}

func (q *Queue) IsPaused() bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.paused
}

func (q *Queue) LastError() error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastErr
}

func (q *Queue) LastTrackError() error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastTrackErr
}

func (q *Queue) execute(ctx context.Context, wake <-chan struct{}, done chan struct{}) {
	result := q.play(ctx, wake)
	q.mu.Lock()
	q.draining = true
	pending := q.pending
	q.pending = nil
	q.mu.Unlock()
	for _, source := range pending {
		if err := source.close(); err != nil {
			result = errors.Join(result, err)
		}
	}
	q.mu.Lock()
	q.lastErr = result
	q.running = false
	q.draining = false
	q.paused = false
	q.current = nil
	q.trackCancel = nil
	cancel := q.cancel
	q.cancel = nil
	close(done)
	q.mu.Unlock()
	cancel(nil)
}

func (q *Queue) play(ctx context.Context, wake <-chan struct{}) error {
	for {
		track, trackCtx, trackCancel, err := q.next(ctx, wake)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, ErrQueueStopped) {
				return nil
			}
			return err
		}
		trackErr := q.playSource(ctx, trackCtx, wake, track.source)
		trackCancel(nil)
		if closeErr := track.close(); closeErr != nil {
			trackErr = errors.Join(trackErr, closeErr)
		}
		skipped := errors.Is(trackErr, ErrTrackSkipped)
		q.mu.Lock()
		if q.current == track {
			q.current = nil
			q.trackCancel = nil
			q.paused = false
		}
		handler := q.onTrackEnd
		continueOnError := q.continueOnError
		q.mu.Unlock()
		if handler != nil {
			if handlerErr := invokeTrackEndHandler(handler, TrackResult{Source: track.source, Err: trackErr, Skipped: skipped}); handlerErr != nil {
				trackErr = errors.Join(trackErr, handlerErr)
			}
		}
		q.mu.Lock()
		q.lastTrackErr = trackErr
		q.mu.Unlock()
		if cause := context.Cause(ctx); cause != nil {
			if errors.Is(cause, ErrQueueStopped) {
				return nil
			}
			return cause
		}
		if trackErr != nil && !skipped && !continueOnError {
			return trackErr
		}
	}
}

func (q *Queue) next(ctx context.Context, wake <-chan struct{}) (*queuedSource, context.Context, context.CancelCauseFunc, error) {
	for {
		q.mu.Lock()
		if len(q.pending) > 0 {
			track := q.pending[0]
			q.pending = q.pending[1:]
			trackCtx, cancel := context.WithCancelCause(ctx)
			q.current = track
			q.trackCancel = cancel
			q.paused = false
			q.mu.Unlock()
			return track, trackCtx, cancel, nil
		}
		draining := q.draining
		q.mu.Unlock()
		if draining {
			return nil, nil, nil, io.EOF
		}
		select {
		case <-ctx.Done():
			return nil, nil, nil, context.Cause(ctx)
		case <-wake:
		}
	}
}

func (q *Queue) playSource(queueCtx, trackCtx context.Context, wake <-chan struct{}, source PCMSource) error {
	frame := make([]int16, FrameSize*q.player.Channels())
	for {
		if err := q.waitUntilResumed(queueCtx, trackCtx, wake); err != nil {
			return err
		}
		err := invokeSourceRead(source, trackCtx, frame)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if cause := context.Cause(trackCtx); cause != nil {
				return cause
			}
			return err
		}
		if err := q.waitUntilResumed(queueCtx, trackCtx, wake); err != nil {
			return err
		}
		for {
			err = q.player.WriteFrame(trackCtx, frame)
			if err == nil {
				break
			}
			if !errors.Is(err, ErrVoiceNotReady) {
				return err
			}
			timer := time.NewTimer(q.reconnectDelay)
			select {
			case <-timer.C:
			case <-trackCtx.Done():
				stopQueueTimer(timer)
				return context.Cause(trackCtx)
			case <-queueCtx.Done():
				stopQueueTimer(timer)
				return context.Cause(queueCtx)
			}
		}
	}
}

func (q *Queue) waitUntilResumed(queueCtx, trackCtx context.Context, wake <-chan struct{}) error {
	for {
		q.mu.Lock()
		paused := q.paused
		q.mu.Unlock()
		if !paused {
			return nil
		}
		select {
		case <-queueCtx.Done():
			return context.Cause(queueCtx)
		case <-trackCtx.Done():
			return context.Cause(trackCtx)
		case <-wake:
		}
	}
}

func invokeTrackEndHandler(handler TrackEndHandler, result TrackResult) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &QueuePanicError{Value: recovered}
		}
	}()
	return handler(result)
}

func sourceChannelCount(source PCMSource) (channels int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &SourcePanicError{Stage: "Channels", Value: recovered}
		}
	}()
	return source.Channels(), nil
}

func invokeSourceRead(source PCMSource, ctx context.Context, frame []int16) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &SourcePanicError{Stage: "ReadFrame", Value: recovered}
		}
	}()
	return source.ReadFrame(ctx, frame)
}

func invokeSourceClose(closer io.Closer) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &SourcePanicError{Stage: "Close", Value: recovered}
		}
	}()
	return closer.Close()
}

func notifyQueue(wake chan<- struct{}) {
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func stopQueueTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func isNilSource(source PCMSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
