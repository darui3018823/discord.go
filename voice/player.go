// Package voice connects Discord Voice transport to the Pure Go Opus codec.
package voice

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	// ErrInvalidVolume is returned for negative, NaN, or infinite gain.
	ErrInvalidVolume = errors.New("invalid volume")
)

// Player encodes interleaved PCM into 20 ms Opus packets and queues them on a
// Discord Voice connection. A Player serializes its stateful Opus encoder and
// is safe for concurrent callers.
type Player struct {
	connection    *dgo.VoiceConnection
	encoder       *opus.Encoder
	channels      int
	mu            sync.Mutex
	volume        float64
	generation    uint64
	generationSet bool
	needsReset    bool
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
		volume:     1,
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

// SetInbandFEC enables Opus forward error correction for lossy voice links.
func (p *Player) SetInbandFEC(enabled bool) {
	p.mu.Lock()
	p.encoder.SetInbandFEC(enabled)
	p.mu.Unlock()
}

// SetPacketLossPercent sets the expected loss percentage used by the Opus
// encoder when allocating FEC redundancy.
func (p *Player) SetPacketLossPercent(percent int) {
	p.mu.Lock()
	p.encoder.SetPacketLossPerc(percent)
	p.mu.Unlock()
}

// SetVolume sets linear PCM gain. One is unchanged, zero is silent, and
// values above one amplify with int16 saturation.
func (p *Player) SetVolume(volume float64) error {
	if volume < 0 || math.IsNaN(volume) || math.IsInf(volume, 0) {
		return ErrInvalidVolume
	}
	p.mu.Lock()
	p.volume = volume
	p.mu.Unlock()
	return nil
}

// Volume returns the current linear PCM gain.
func (p *Player) Volume() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.volume
}

// Reset clears Opus stream history while preserving encoder controls. Call it
// when starting a logically new audio stream.
func (p *Player) Reset() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.encoder.Reset(); err != nil {
		return err
	}
	p.needsReset = false
	return nil
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

	send, ready, generation := p.connection.OpusSendState()
	if send == nil || !ready {
		return ErrVoiceNotReady
	}

	p.mu.Lock()
	if !p.generationSet || generation != p.generation || p.needsReset {
		if p.generationSet || p.needsReset {
			if err := p.encoder.Reset(); err != nil {
				p.mu.Unlock()
				return fmt.Errorf("reset Opus encoder after voice reconnect: %w", err)
			}
		}
		p.generation = generation
		p.generationSet = true
		p.needsReset = false
	}
	input := pcm
	if p.volume != 1 {
		input = applyVolume(pcm, p.volume)
	}
	packet, err := p.encoder.Encode(input, FrameSize)
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode Opus frame: %w", err)
	}

	currentSend, currentReady, currentGeneration := p.connection.OpusSendState()
	if currentSend == nil || currentSend != send || !currentReady || currentGeneration != generation {
		p.mu.Lock()
		p.needsReset = true
		p.mu.Unlock()
		return ErrVoiceNotReady
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case send <- packet:
		return nil
	}
}

func applyVolume(pcm []int16, volume float64) []int16 {
	adjusted := make([]int16, len(pcm))
	for index, sample := range pcm {
		scaled := math.Round(float64(sample) * volume)
		switch {
		case scaled > math.MaxInt16:
			adjusted[index] = math.MaxInt16
		case scaled < math.MinInt16:
			adjusted[index] = math.MinInt16
		default:
			adjusted[index] = int16(scaled)
		}
	}
	return adjusted
}
