# Voice framework

The `voice` package connects the low-level `dgo.VoiceConnection` transport to
`github.com/darui3018823/opus`. Encoding and decoding are Pure Go; no system
libopus installation or CGO is required.

## Audio contract

Discord Voice uses 48 kHz audio in 20 ms packets. `PCMSource` therefore emits
exactly `voice.FrameSize` signed 16-bit samples per channel on each call.
Samples are interleaved for stereo. `io.EOF` marks a clean boundary; partial
frames are rejected rather than silently changing timing.

The package provides:

- `SliceSource` for finite PCM already in memory;
- `ReaderSource` for headerless little-endian s16le PCM;
- `SourceFunc` for decoders, network streams, generators, and tests.

The runnable [voice queue example](https://github.com/darui3018823/discord.go/tree/master/examples/voice_queue)
plays a raw stereo s16le file using these APIs.

Compressed-container parsing, URL fetching, process execution, and resampling
stay outside the trusted audio boundary. This keeps the core library small and
lets applications choose their media policy.

## Player and queue

Create a player after joining Voice, then enqueue sources under an explicit
context:

```go
connection, err := session.ChannelVoiceJoin(guildID, channelID, false, true)
if err != nil {
	return err
}
player, err := voice.NewPlayer(connection, 2)
if err != nil {
	return err
}
if err := player.SetVolume(0.8); err != nil {
	return err
}
player.SetInbandFEC(true)
player.SetPacketLossPercent(15)

queue, err := voice.NewQueue(player)
if err != nil {
	return err
}
if err := queue.Enqueue(source); err != nil {
	return err
}
if err := queue.Start(ctx); err != nil {
	return err
}
return queue.Drain(ctx)
```

An open Queue waits for later sources. `Drain` rejects new work and finishes
the current queue; `Stop` cancels current playback and closes pending sources.
`Pause`, `Resume`, `Skip`, `Clear`, `Current`, `Len`, and status/error methods
are safe for concurrent control code. The Queue assumes ownership and calls
`Close` on sources that implement `io.Closer`.

If Voice temporarily becomes unready, the same PCM frame is retained and
retried instead of being dropped or read twice. A fresh UDP encryption session
increments the transport audio generation; Player resets its stateful Opus
encoder before encoding into that generation. A successful Voice resume keeps
the generation and codec history.

## Optional ffmpeg boundary

ffmpeg is not a library dependency. Applications that need arbitrary media can
run it as an explicit subprocess and feed its stdout to `ReaderSource`:

```go
cmd := exec.CommandContext(ctx, "ffmpeg",
	"-nostdin", "-i", inputPath,
	"-f", "s16le", "-ar", "48000", "-ac", "2", "pipe:1",
)
stdout, err := cmd.StdoutPipe()
if err != nil {
	return err
}
source, err := voice.NewReaderSource(stdout, 2)
if err != nil {
	return err
}
if err := cmd.Start(); err != nil {
	return err
}
if err := queue.Enqueue(source); err != nil {
	return err
}
```

Pass arguments directly rather than constructing a shell command. Validate
untrusted paths/URLs, bound downloads and process lifetime, capture stderr
without logging secrets, and wait for the process after playback. Closing a
source backed by an `io.Closer` can interrupt a blocked queue read.

## Receive, PLC, and FEC

`Receiver` owns one decoder per SSRC and enforces RTP sequence order. For a
forward gap it bounds recovery work, uses in-band LBRR/FEC from the arriving
packet for the newest missing frame, and uses Opus packet-loss concealment for
older missing frames. `Frame.Recovered` and `Frame.Concealed` distinguish the
two paths.

```go
receiver, err := voice.NewReceiver(2)
if err != nil {
	return err
}
for packet := range connection.OpusRecv {
	frames, err := receiver.Decode(packet)
	if err != nil {
		continue
	}
	for _, frame := range frames {
		consumePCM(frame.PCM)
	}
}
```

Call `Reset(ssrc)` when an SSRC is reassigned or `ResetAll` after a logically
fresh receive transport. Late/duplicate packets and excessive gaps return
typed errors without corrupting retained decoder state.
