package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

var (
	// ErrInvalidSource is returned for a nil source, invalid channel layout, or
	// malformed destination frame.
	ErrInvalidSource = errors.New("invalid PCM source")
	// ErrPartialPCMFrame is returned when a source ends between Discord's fixed
	// 20 ms frame boundaries.
	ErrPartialPCMFrame = errors.New("partial PCM frame")
)

// PCMSource produces interleaved signed 16-bit, 48 kHz PCM in Discord's fixed
// 20 ms frame size. ReadFrame must fill dst or return an error; io.EOF means
// clean end of stream. Implementations should honor ctx and may also implement
// io.Closer so queue cancellation can interrupt a blocked read.
type PCMSource interface {
	Channels() int
	ReadFrame(context.Context, []int16) error
}

// SourceFunc adapts functions to PCMSource and optionally io.Closer.
type SourceFunc struct {
	channels int
	read     func(context.Context, []int16) error
	close    func() error
	once     sync.Once
	closeErr error
}

// NewSourceFunc creates a PCM source from callbacks.
func NewSourceFunc(channels int, read func(context.Context, []int16) error, closeFn ...func() error) (*SourceFunc, error) {
	if (channels != 1 && channels != 2) || read == nil {
		return nil, ErrInvalidSource
	}
	var closeCallback func() error
	if len(closeFn) > 0 {
		closeCallback = closeFn[0]
	}
	return &SourceFunc{channels: channels, read: read, close: closeCallback}, nil
}

func (s *SourceFunc) Channels() int { return s.channels }

func (s *SourceFunc) ReadFrame(ctx context.Context, dst []int16) error {
	if s == nil || ctx == nil || len(dst) != FrameSize*s.channels {
		return ErrInvalidSource
	}
	return s.read(ctx, dst)
}

// Close invokes the optional close callback once.
func (s *SourceFunc) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.close != nil {
			s.closeErr = s.close()
		}
	})
	return s.closeErr
}

// ReaderSource adapts headerless little-endian signed 16-bit PCM. The reader
// is expected to already provide 48 kHz audio; use a decoder or ffmpeg process
// outside this boundary for compressed media and resampling.
type ReaderSource struct {
	reader   io.Reader
	closer   io.Closer
	channels int
	readMu   sync.Mutex
	stateMu  sync.Mutex
	closed   bool
	closeErr error
}

// NewReaderSource creates a source from raw s16le PCM. If reader implements
// io.Closer, Close delegates to it and can be used to interrupt reads.
func NewReaderSource(reader io.Reader, channels int) (*ReaderSource, error) {
	if reader == nil || (channels != 1 && channels != 2) {
		return nil, ErrInvalidSource
	}
	source := &ReaderSource{reader: reader, channels: channels}
	if closer, ok := reader.(io.Closer); ok {
		source.closer = closer
	}
	return source, nil
}

func (s *ReaderSource) Channels() int { return s.channels }

func (s *ReaderSource) ReadFrame(ctx context.Context, dst []int16) error {
	if s == nil || ctx == nil || len(dst) != FrameSize*s.channels {
		return ErrInvalidSource
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	s.stateMu.Lock()
	closed := s.closed
	s.stateMu.Unlock()
	if closed {
		return io.ErrClosedPipe
	}

	encoded := make([]byte, len(dst)*2)
	read, err := io.ReadFull(s.reader, encoded)
	if err != nil {
		if errors.Is(err, io.EOF) && read == 0 {
			return io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || read > 0 {
			return fmt.Errorf("%w: got %d bytes, want %d", ErrPartialPCMFrame, read, len(encoded))
		}
		return err
	}
	for index := range dst {
		dst[index] = int16(binary.LittleEndian.Uint16(encoded[index*2:]))
	}
	return nil
}

// Close marks the source closed and closes its underlying reader when
// supported.
func (s *ReaderSource) Close() error {
	if s == nil {
		return nil
	}
	s.stateMu.Lock()
	if s.closed {
		err := s.closeErr
		s.stateMu.Unlock()
		return err
	}
	s.closed = true
	closer := s.closer
	s.stateMu.Unlock()
	if closer != nil {
		err := closer.Close()
		s.stateMu.Lock()
		s.closeErr = err
		s.stateMu.Unlock()
		return err
	}
	return nil
}

// SliceSource reads a finite interleaved PCM slice. The input is copied so it
// may be reused or modified after construction.
type SliceSource struct {
	channels int
	pcm      []int16
	offset   int
	mu       sync.Mutex
}

// NewSliceSource creates an in-memory PCM source. Its length must end on a
// complete 20 ms frame boundary.
func NewSliceSource(pcm []int16, channels int) (*SliceSource, error) {
	if channels != 1 && channels != 2 {
		return nil, ErrInvalidSource
	}
	frameSamples := FrameSize * channels
	if len(pcm)%frameSamples != 0 {
		return nil, ErrPartialPCMFrame
	}
	return &SliceSource{channels: channels, pcm: append([]int16(nil), pcm...)}, nil
}

func (s *SliceSource) Channels() int { return s.channels }

func (s *SliceSource) ReadFrame(ctx context.Context, dst []int16) error {
	if s == nil || ctx == nil || len(dst) != FrameSize*s.channels {
		return ErrInvalidSource
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.offset == len(s.pcm) {
		return io.EOF
	}
	copy(dst, s.pcm[s.offset:s.offset+len(dst)])
	s.offset += len(dst)
	return nil
}
