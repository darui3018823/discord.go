// Package voice connects Discord Voice transport to the Pure Go Opus codec.
package voice

import (
	"context"
	"errors"
	"fmt"
	"sync"

	dgo "github.com/darui3018823/discord.go"
	"github.com/darui3018823/opus"
)

const (
	// SampleRate is Discord Voice's PCM sample rate.
	SampleRate = opus.SampleRate48kHz
	// FrameSize is the number of samples per channel in one 20 ms Discord
	// Voice packet.
	FrameSize = opus.FrameSize20ms
)

var (
	// ErrVoiceNotReady is returned before the Discord Voice transport has
	// created its Opus send channel.
	ErrVoiceNotReady = errors.New("voice connection is not ready")
	// ErrInvalidPCMFrame is returned when a PCM frame is not exactly 20 ms.
	ErrInvalidPCMFrame = errors.New("invalid PCM frame")
)

// Player encodes interleaved PCM into 20 ms Opus packets and queues them on a
// Discord Voice connection. A Player serializes its stateful Opus encoder and
// is safe for concurrent callers.
type Player struct {
	connection *dgo.VoiceConnection
	encoder    *opus.Encoder
	channels   int
	mu         sync.Mutex
}

// NewPlayer creates a PCM player for a ready or connecting Voice connection.
// channels must be one or two. Discord transport timing is fixed at 20 ms, so
// every WriteFrame call must contain exactly FrameSize samples per channel.
func NewPlayer(connection *dgo.VoiceConnection, channels int) (*Player, error) {
	if connection == nil {
		return nil, errors.New("voice connection must not be nil")
	}
	encoder, err := opus.NewEncoderWithProfile(
		SampleRate,
		channels,
		opus.ApplicationAudio,
		opus.EncoderProfileLibopus,
	)
	if err != nil {
		return nil, fmt.Errorf("create Opus encoder: %w", err)
	}
	return &Player{
		connection: connection,
		encoder:    encoder,
		channels:   channels,
	}, nil
}

// Channels returns the interleaved PCM channel count.
func (p *Player) Channels() int {
	return p.channels
}

// SetBitrate changes the Opus target bitrate.
func (p *Player) SetBitrate(bitrate int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.encoder.SetBitrate(bitrate)
}

// Reset clears Opus stream history while preserving encoder controls. Call it
// when starting a logically new audio stream.
func (p *Player) Reset() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.encoder.Reset()
}

// WriteFrame encodes and queues one 20 ms interleaved PCM frame. The input is
// borrowed only until WriteFrame returns.
func (p *Player) WriteFrame(ctx context.Context, pcm []int16) error {
	if ctx == nil {
		return errors.New("context must not be nil")
	}
	if len(pcm) != FrameSize*p.channels {
		return fmt.Errorf("%w: got %d samples, want %d", ErrInvalidPCMFrame, len(pcm), FrameSize*p.channels)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	packet, err := p.encoder.Encode(pcm, FrameSize)
	if err != nil {
		return fmt.Errorf("encode Opus frame: %w", err)
	}

	p.connection.RLock()
	send := p.connection.OpusSend
	ready := p.connection.Ready
	p.connection.RUnlock()
	if send == nil || !ready {
		return ErrVoiceNotReady
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case send <- packet:
		return nil
	}
}
