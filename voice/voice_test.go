package voice

import (
	"context"
	"errors"
	"testing"

	dgo "github.com/darui3018823/discord.go"
	"github.com/darui3018823/opus"
)

func newEncoder(t *testing.T, channels int) *opus.Encoder {
	t.Helper()
	encoder, err := opus.NewEncoder(SampleRate, channels, opus.ApplicationAudio)
	if err != nil {
		t.Fatal(err)
	}
	return encoder
}

func TestPlayerWritesTwentyMillisecondOpusPacket(t *testing.T) {
	connection := &dgo.VoiceConnection{Ready: true, OpusSend: make(chan []byte, 1)}
	player, err := NewPlayer(connection, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := player.WriteFrame(context.Background(), make([]int16, FrameSize*2)); err != nil {
		t.Fatal(err)
	}

	packet := <-connection.OpusSend
	samples, err := opus.PacketGetNumSamples(packet, SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if samples != FrameSize {
		t.Fatalf("packet has %d samples per channel, want %d", samples, FrameSize)
	}
}

func TestPlayerRejectsWrongFrameSize(t *testing.T) {
	connection := &dgo.VoiceConnection{Ready: true, OpusSend: make(chan []byte, 1)}
	player, err := NewPlayer(connection, 1)
	if err != nil {
		t.Fatal(err)
	}
	err = player.WriteFrame(context.Background(), make([]int16, FrameSize-1))
	if !errors.Is(err, ErrInvalidPCMFrame) {
		t.Fatalf("WriteFrame error = %v, want ErrInvalidPCMFrame", err)
	}
}

func TestReceiverDecodesAndConcealsSequenceGap(t *testing.T) {
	const channels = 1
	encoder := newEncoder(t, channels)
	first, err := encoder.Encode(make([]int16, FrameSize*channels), FrameSize)
	if err != nil {
		t.Fatal(err)
	}
	secondPCM := make([]int16, FrameSize*channels)
	secondPCM[0] = 1000
	second, err := encoder.Encode(secondPCM, FrameSize)
	if err != nil {
		t.Fatal(err)
	}

	receiver, err := NewReceiver(channels)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := receiver.Decode(&dgo.Packet{SSRC: 42, Sequence: 10, Opus: first})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Concealed {
		t.Fatalf("first Decode returned %#v", frames)
	}

	frames, err = receiver.Decode(&dgo.Packet{SSRC: 42, Sequence: 12, Opus: second})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("Decode returned %d frames, want concealed plus current", len(frames))
	}
	if !frames[0].Concealed || frames[0].Sequence != 11 {
		t.Fatalf("concealed frame = %#v", frames[0])
	}
	if frames[1].Concealed || frames[1].Sequence != 12 {
		t.Fatalf("current frame = %#v", frames[1])
	}
}

func TestReceiverRejectsLatePacket(t *testing.T) {
	encoder := newEncoder(t, 1)
	packet, err := encoder.Encode(make([]int16, FrameSize), FrameSize)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Decode(&dgo.Packet{SSRC: 7, Sequence: 2, Opus: packet}); err != nil {
		t.Fatal(err)
	}
	_, err = receiver.Decode(&dgo.Packet{SSRC: 7, Sequence: 2, Opus: packet})
	if !errors.Is(err, ErrLatePacket) {
		t.Fatalf("Decode error = %v, want ErrLatePacket", err)
	}
}
