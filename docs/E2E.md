# Live Discord E2E validation

Unit, race, fuzz, fixture, and documentation tests do not replace a live
Discord validation. This page defines a repeatable test that uses a dedicated
application and guild without placing credentials in source control.

## Current validation status

The 2026-08-31 local release-baseline run had no `DISCORD_TOKEN`,
`DISCORD_BOT_TOKEN`, `DISCORD_APPLICATION_ID`, `DISCORD_GUILD_ID`, or
`DISCORD_VOICE_CHANNEL_ID` environment variables. Live Gateway, Interaction,
Voice UDP, and DAVE negotiation were therefore **not executed** in that run.
All non-credentialed release checks passed; this is the remaining external
validation boundary.

## Test application setup

Use a disposable test application and guild, not a production bot.

1. Create a bot in the Discord Developer Portal.
2. Enable Message Content only if testing the prefix command.
3. Install it in the test guild with the `bot` and `applications.commands`
   scopes and only the permissions required by the cases below.
4. Create a Voice channel where every participant has explicitly agreed to the
   test. Do not enable recording.
5. Export credentials and IDs in the shell. Never paste them into commands that
   will be committed, logs, screenshots, or issue reports.

The examples read these variables:

```text
DISCORD_TOKEN
DISCORD_APPLICATION_ID
DISCORD_GUILD_ID
DISCORD_VOICE_CHANNEL_ID
```

## Gateway, commands, and lifecycle

Run the high-level example from the repository root:

```sh
go run ./examples/high_level
```

Verify all of the following:

- the Gateway reaches Ready without reconnect churn;
- `/ping` appears in the development guild and responds `Pong!`;
- `!ping` responds only when Message Content is enabled;
- the managed background loop ticks without overlapping executions;
- Ctrl+C unloads managed resources and closes the Gateway without a hang;
- a second run produces an unchanged command diff rather than recreating the
  slash command.

For component, modal, autocomplete, permission, cooldown, and error-scope
checks, register the corresponding routes from [High-Level Framework](HighLevel.md)
in the same disposable bot, invoke each from the Discord client, and confirm
the nearest handler receives both normal errors and a deliberately panicking
test handler without terminating the process.

## Voice send, reconnect, and DAVE

Generate a short stereo 48 kHz raw PCM fixture locally if ffmpeg is available:

```sh
ffmpeg -f lavfi -i "sine=frequency=440:duration=3" -ac 2 -ar 48000 -f s16le test-tone.pcm
```

Set `PCM_S16LE_PATH` to that file and run:

```sh
go run ./examples/voice_queue
```

Verify that the tone plays once, queue drain returns, and the bot leaves cleanly.
Repeat while briefly disconnecting the test machine after playback starts. The
current PCM frame should retry after Voice reconnect, the fresh audio generation
should reset the encoder, and playback must not panic, duplicate source reads,
or leak a goroutine.

DAVE is negotiated automatically when Discord selects it. To exercise receive
and decrypt handling without writing recordings, run the nested example:

```sh
cd examples/voice_receive
go run . -t YOUR_BOT_TOKEN -g YOUR_GUILD_ID -c YOUR_VOICE_CHANNEL_ID
```

Have a consenting participant speak briefly. Confirm packets are received for
the expected SSRC, DAVE transitions complete without repeated decrypt failures,
and disconnect/rejoin creates fresh receive codec state. Leave `-record`
disabled; the example's README documents the additional consent and storage
requirements if recording is ever tested.

Delete `test-tone.pcm` after validation. Revoke and rotate any credential that
may have been exposed during the run.
