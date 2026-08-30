package dgo

import "testing"

func TestVoiceConnectionOpusSendState(t *testing.T) {
	send := make(chan []byte, 1)
	connection := &VoiceConnection{Ready: true, OpusSend: send, audioGeneration: 7}
	gotSend, ready, generation := connection.OpusSendState()
	if gotSend != send || !ready || generation != 7 {
		t.Fatalf("OpusSendState = %v, %v, %d", gotSend, ready, generation)
	}
}
