package voice

import (
	"testing"

	dgo "github.com/darui3018823/discord.go"
)

func FuzzReceiverDecode(f *testing.F) {
	f.Add(uint16(1), []byte{0xF8, 0xFF, 0xFE})
	f.Add(uint16(65535), []byte{0x00})
	f.Fuzz(func(t *testing.T, sequence uint16, payload []byte) {
		if len(payload) == 0 || len(payload) > 1500 {
			t.Skip()
		}
		receiver, err := NewReceiver(2)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = receiver.Decode(&dgo.Packet{SSRC: 1, Sequence: sequence, Opus: payload})
	})
}
