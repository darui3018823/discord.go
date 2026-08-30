package voice

import (
	"context"
	"errors"
	"math"
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

func TestPlayerVolumeAndFECControls(t *testing.T) {
	connection := &dgo.VoiceConnection{Ready: true, OpusSend: make(chan []byte, 1)}
	player, err := NewPlayer(connection, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, volume := range []float64{-1, math.NaN(), math.Inf(1)} {
		if err := player.SetVolume(volume); !errors.Is(err, ErrInvalidVolume) {
			t.Fatalf("SetVolume(%v) error = %v", volume, err)
		}
	}
	if err := player.SetVolume(0.5); err != nil {
		t.Fatal(err)
	}
	if player.Volume() != 0.5 {
		t.Fatalf("Volume = %v", player.Volume())
	}
	player.SetInbandFEC(true)
	player.SetPacketLossPercent(20)
	if !player.encoder.InbandFEC() || player.encoder.PacketLossPerc() != 20 {
		t.Fatal("Opus FEC controls were not applied")
	}
	pcm := make([]int16, FrameSize)
	pcm[0] = 1200
	if err := player.WriteFrame(context.Background(), pcm); err != nil {
		t.Fatal(err)
	}
	if pcm[0] != 1200 {
		t.Fatal("WriteFrame mutated caller PCM while applying volume")
	}
}

func TestApplyVolumeSaturates(t *testing.T) {
	pcm := []int16{20000, -20000, 3, -3}
	got := applyVolume(pcm, 2)
	want := []int16{math.MaxInt16, math.MinInt16, 6, -6}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("applyVolume[%d] = %d, want %d", index, got[index], want[index])
		}
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

func TestReceiverUsesInbandFECForNewestMissingFrame(t *testing.T) {
	encoder, err := opus.NewEncoder(SampleRate, 1, opus.ApplicationVOIP)
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.SetBitrate(18000); err != nil {
		t.Fatal(err)
	}
	encoder.SetSignalType(opus.SignalVoice)
	encoder.SetInbandFEC(true)
	encoder.SetPacketLossPerc(20)
	packets := make([][]byte, 6)
	for packetIndex := range packets {
		pcm := make([]float64, FrameSize)
		for sample := range pcm {
			position := float64(packetIndex*FrameSize+sample) / SampleRate
			pcm[sample] = 0.3 * math.Sin(2*math.Pi*190*position)
		}
		packets[packetIndex], err = encoder.EncodeFloat(pcm, FrameSize)
		if err != nil {
			t.Fatal(err)
		}
	}
	if hasFEC, err := opus.PacketHasLBRR(packets[5]); err != nil || !hasFEC {
		t.Fatalf("test packet FEC = %v, %v", hasFEC, err)
	}

	receiver, err := NewReceiver(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Decode(&dgo.Packet{SSRC: 9, Sequence: 20, Opus: packets[3]}); err != nil {
		t.Fatal(err)
	}
	frames, err := receiver.Decode(&dgo.Packet{SSRC: 9, Sequence: 22, Opus: packets[5]})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || !frames[0].Recovered || frames[0].Concealed || frames[0].Sequence != 21 {
		t.Fatalf("FEC frames = %#v", frames)
	}
}
