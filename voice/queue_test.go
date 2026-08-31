package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	dgo "github.com/darui3018823/discord.go"
)

func testQueue(t *testing.T, ready bool, options ...QueueOption) (*Queue, *dgo.VoiceConnection) {
	t.Helper()
	connection := &dgo.VoiceConnection{Ready: ready, OpusSend: make(chan []byte, 16)}
	player, err := NewPlayer(connection, 1)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := NewQueue(player, options...)
	if err != nil {
		t.Fatal(err)
	}
	return queue, connection
}

func oneFrameSource(t *testing.T, sample int16) *SliceSource {
	t.Helper()
	pcm := make([]int16, FrameSize)
	pcm[0] = sample
	source, err := NewSliceSource(pcm, 1)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestQueuePlaysSourcesInOrderAndDrains(t *testing.T) {
	var ended []PCMSource
	queue, connection := testQueue(t, true, WithTrackEndHandler(func(result TrackResult) error {
		if result.Err != nil || result.Skipped {
			return errors.New("unexpected track result")
		}
		ended = append(ended, result.Source)
		return nil
	}))
	first := oneFrameSource(t, 100)
	second := oneFrameSource(t, 200)
	if err := queue.Enqueue(first, second); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := queue.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ended, []PCMSource{first, second}) {
		t.Fatalf("track order = %v", ended)
	}
	if len(connection.OpusSend) != 2 || queue.IsRunning() || queue.Len() != 0 {
		t.Fatalf("packets/running/pending = %d/%v/%d", len(connection.OpusSend), queue.IsRunning(), queue.Len())
	}
}

func TestQueuePauseAndResume(t *testing.T) {
	queue, connection := testQueue(t, true)
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	reads := 0
	source, err := NewSourceFunc(1, func(ctx context.Context, dst []int16) error {
		if reads > 0 {
			return io.EOF
		}
		reads++
		close(readStarted)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseRead:
			dst[0] = 300
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(source); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-readStarted
	if err := queue.Pause(); err != nil {
		t.Fatal(err)
	}
	close(releaseRead)
	select {
	case <-connection.OpusSend:
		t.Fatal("paused queue sent a frame")
	case <-time.After(10 * time.Millisecond):
	}
	if !queue.IsPaused() {
		t.Fatal("queue did not report paused")
	}
	if err := queue.Resume(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connection.OpusSend:
	case <-time.After(time.Second):
		t.Fatal("resumed queue did not send a frame")
	}
	if err := queue.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestQueueSkipAdvancesToNextSource(t *testing.T) {
	var mu sync.Mutex
	var results []TrackResult
	queue, connection := testQueue(t, true, WithTrackEndHandler(func(result TrackResult) error {
		mu.Lock()
		results = append(results, result)
		mu.Unlock()
		return nil
	}))
	started := make(chan struct{})
	blocking, err := NewSourceFunc(1, func(ctx context.Context, _ []int16) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(blocking, oneFrameSource(t, 500)); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := queue.Skip(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connection.OpusSend:
	case <-time.After(time.Second):
		t.Fatal("queue did not advance after Skip")
	}
	if err := queue.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(results) != 2 || !results[0].Skipped || !errors.Is(results[0].Err, ErrTrackSkipped) || results[1].Err != nil {
		t.Fatalf("track results = %#v", results)
	}
}

func TestQueueRetriesFrameAfterVoiceReconnect(t *testing.T) {
	queue, connection := testQueue(t, false, WithReconnectDelay(time.Millisecond))
	read := make(chan struct{})
	reads := 0
	source, err := NewSourceFunc(1, func(context.Context, []int16) error {
		if reads > 0 {
			return io.EOF
		}
		reads++
		close(read)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(source); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-read
	connection.Lock()
	connection.Ready = true
	connection.Unlock()
	select {
	case <-connection.OpusSend:
	case <-time.After(time.Second):
		t.Fatal("queued frame was not retried after reconnect")
	}
	if reads != 1 {
		t.Fatalf("source frame was read %d times", reads)
	}
	if err := queue.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestQueueHandlerPanicStopsWhenConfigured(t *testing.T) {
	queue, _ := testQueue(t, true,
		WithQueueContinueOnError(false),
		WithTrackEndHandler(func(TrackResult) error { panic("callback") }),
	)
	if err := queue.Enqueue(oneFrameSource(t, 1), oneFrameSource(t, 2)); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var panicErr *QueuePanicError
	if err := queue.Drain(context.Background()); !errors.As(err, &panicErr) {
		t.Fatalf("Drain error = %v", err)
	}
	if queue.Len() != 0 {
		t.Fatal("unplayed sources were not released after worker failure")
	}
}

func TestQueueRecoversSourcePanic(t *testing.T) {
	queue, _ := testQueue(t, true, WithQueueContinueOnError(false))
	source, err := NewSourceFunc(1, func(context.Context, []int16) error {
		panic("source")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(source); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var panicErr *SourcePanicError
	if err := queue.Drain(context.Background()); !errors.As(err, &panicErr) || panicErr.Stage != "ReadFrame" {
		t.Fatalf("Drain source panic = %v", err)
	}
}

func TestReaderAndSliceSourceValidation(t *testing.T) {
	pcm := make([]int16, FrameSize)
	pcm[0], pcm[1] = 1234, -4321
	encoded := make([]byte, len(pcm)*2)
	for index, sample := range pcm {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	source, err := NewReaderSource(bytes.NewReader(encoded), 1)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int16, FrameSize)
	if err := source.ReadFrame(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, pcm) {
		t.Fatal("ReaderSource changed PCM")
	}
	if err := source.ReadFrame(context.Background(), got); !errors.Is(err, io.EOF) {
		t.Fatalf("end error = %v", err)
	}
	partial, err := NewReaderSource(bytes.NewReader(encoded[:3]), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := partial.ReadFrame(context.Background(), got); !errors.Is(err, ErrPartialPCMFrame) {
		t.Fatalf("partial error = %v", err)
	}
	if _, err := NewSliceSource(make([]int16, FrameSize-1), 1); !errors.Is(err, ErrPartialPCMFrame) {
		t.Fatalf("slice validation error = %v", err)
	}
	var nilSource *SliceSource
	queue, _ := testQueue(t, true)
	if err := queue.Enqueue(nilSource); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("typed nil source error = %v", err)
	}
}
