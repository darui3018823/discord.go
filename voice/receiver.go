package voice

import (
	"errors"
	"fmt"
	"sync"

	dgo "github.com/darui3018823/discord.go"
	"github.com/darui3018823/opus"
)

var (
	// ErrLatePacket is returned for a packet older than the next expected RTP
	// sequence number. Decoding it would corrupt the stateful Opus stream.
	ErrLatePacket = errors.New("late or duplicate voice packet")
	// ErrPacketGapTooLarge bounds work caused by a large or malicious RTP
	// sequence jump.
	ErrPacketGapTooLarge = errors.New("voice packet gap is too large")
)

const maxConcealedFrames = 10

// Frame is one decoded 20 ms PCM frame. Concealed is true when PCM was
// generated with Opus packet-loss concealment.
type Frame struct {
	SSRC      uint32
	Sequence  uint16
	PCM       []int16
	Concealed bool
}

type decoderState struct {
	decoder      *opus.Decoder
	nextSequence uint16
	initialized  bool
}

// Receiver decodes Discord Opus packets. It owns one stateful decoder per
// SSRC and is safe for concurrent callers.
type Receiver struct {
	channels int
	mu       sync.Mutex
	streams  map[uint32]*decoderState
}

// NewReceiver creates a Discord Voice Opus receiver. channels must be one or
// two and controls the interleaved PCM output layout.
func NewReceiver(channels int) (*Receiver, error) {
	if channels != 1 && channels != 2 {
		return nil, fmt.Errorf("channels must be one or two: %d", channels)
	}
	return &Receiver{
		channels: channels,
		streams:  make(map[uint32]*decoderState),
	}, nil
}

// Channels returns the interleaved PCM channel count.
func (r *Receiver) Channels() int {
	return r.channels
}

func (r *Receiver) stream(ssrc uint32) (*decoderState, error) {
	if state := r.streams[ssrc]; state != nil {
		return state, nil
	}
	decoder, err := opus.NewDecoder(SampleRate, r.channels)
	if err != nil {
		return nil, err
	}
	state := &decoderState{decoder: decoder}
	r.streams[ssrc] = state
	return state, nil
}

// Decode decodes one Discord packet. A forward sequence gap produces one
// concealed Frame per missing packet before the decoded current Frame.
func (r *Receiver) Decode(packet *dgo.Packet) ([]Frame, error) {
	if packet == nil {
		return nil, errors.New("voice packet must not be nil")
	}
	if len(packet.Opus) == 0 {
		return nil, errors.New("voice packet has no Opus payload")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.stream(packet.SSRC)
	if err != nil {
		return nil, fmt.Errorf("create Opus decoder: %w", err)
	}

	frames := make([]Frame, 0, 1)
	if state.initialized {
		missing := uint16(packet.Sequence - state.nextSequence)
		switch {
		case missing == 0:
		case missing < 1<<15:
			if missing > maxConcealedFrames {
				return nil, fmt.Errorf("%w: missing %d packets", ErrPacketGapTooLarge, missing)
			}
			frames = make([]Frame, 0, int(missing)+1)
			for offset := uint16(0); offset < missing; offset++ {
				pcm := make([]int16, FrameSize*r.channels)
				samples, decodeErr := state.decoder.DecodePLC(pcm, FrameSize)
				if decodeErr != nil {
					delete(r.streams, packet.SSRC)
					return nil, fmt.Errorf("conceal Opus frame: %w", decodeErr)
				}
				frames = append(frames, Frame{
					SSRC:      packet.SSRC,
					Sequence:  state.nextSequence + offset,
					PCM:       pcm[:samples*r.channels],
					Concealed: true,
				})
			}
		case missing >= 1<<15:
			return nil, fmt.Errorf("%w: got %d, expected %d", ErrLatePacket, packet.Sequence, state.nextSequence)
		}
	}

	pcm := make([]int16, opus.MaxFrameSize*r.channels)
	samples, err := state.decoder.Decode(packet.Opus, pcm)
	if err != nil {
		// A preceding PLC operation may already have advanced codec history.
		// Drop the stream rather than retaining a state the caller cannot
		// reconcile with the failed packet.
		delete(r.streams, packet.SSRC)
		return nil, fmt.Errorf("decode Opus frame: %w", err)
	}
	frames = append(frames, Frame{
		SSRC:     packet.SSRC,
		Sequence: packet.Sequence,
		PCM:      pcm[:samples*r.channels],
	})
	state.nextSequence = packet.Sequence + 1
	state.initialized = true
	return frames, nil
}

// Reset drops codec history for one SSRC.
func (r *Receiver) Reset(ssrc uint32) {
	r.mu.Lock()
	delete(r.streams, ssrc)
	r.mu.Unlock()
}

// ResetAll drops codec history for every SSRC.
func (r *Receiver) ResetAll() {
	r.mu.Lock()
	r.streams = make(map[uint32]*decoderState)
	r.mu.Unlock()
}
