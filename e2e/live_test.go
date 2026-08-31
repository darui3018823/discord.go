// Package e2e contains opt-in tests against the live Discord service.
package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	dgo "github.com/darui3018823/discord.go"
	"github.com/darui3018823/discord.go/bot"
	"github.com/darui3018823/discord.go/voice"
)

const (
	liveTestTimeout      = 90 * time.Second
	liveTokenEnvironment = "test_bot_token"
	liveGuildEnvironment = "test_guild_id"
	liveVoiceEnvironment = "test_voice_channel_id"
	testToneFrames       = 50
)

// TestLiveBotConnectivity verifies the read-only REST surface, high-level
// command planning, Gateway READY dispatch, and graceful Bot shutdown. When a
// test guild is configured, it also exercises a temporary guild command and
// optional Voice playback before cleaning up both resources.
//
// Set test_bot_token to a raw token for a dedicated test bot to enable it.
// test_guild_id additionally enables command registration, and
// test_voice_channel_id additionally enables Voice playback.
func TestLiveBotConnectivity(t *testing.T) {
	if testing.Short() {
		t.Skip("live Discord E2E is disabled by -short")
	}
	token := os.Getenv(liveTokenEnvironment)
	if token == "" {
		t.Skip(liveTokenEnvironment + " is not set")
	}
	guildID := os.Getenv(liveGuildEnvironment)
	voiceChannelID := os.Getenv(liveVoiceEnvironment)
	if voiceChannelID != "" && guildID == "" {
		t.Fatalf("%s requires %s", liveVoiceEnvironment, liveGuildEnvironment)
	}

	testCtx, cancelTest := context.WithTimeout(context.Background(), liveTestTimeout)
	defer cancelTest()

	framework, err := bot.NewWithToken(token)
	if err != nil {
		t.Fatalf("create high-level bot: %v", err)
	}
	session := framework.Session()
	session.Identify.Intents = dgo.IntentsGuilds | dgo.IntentsGuildVoiceStates

	currentUser, err := session.User("@me", dgo.WithContext(testCtx))
	if err != nil {
		t.Fatalf("authenticate with Discord REST: %v", err)
	}
	if currentUser == nil || currentUser.ID == "" || !currentUser.Bot {
		t.Fatalf("current user is not a Discord bot: %#v", currentUser)
	}

	application, err := session.CurrentApplication(dgo.WithContext(testCtx))
	if err != nil {
		t.Fatalf("fetch current Discord application: %v", err)
	}
	if application == nil || application.ID == "" {
		t.Fatal("current Discord application has no ID")
	}

	gateway, err := session.GatewayBot(dgo.WithContext(testCtx))
	if err != nil {
		t.Fatalf("discover Discord Gateway: %v", err)
	}
	if gateway == nil || gateway.URL == "" {
		t.Fatal("Discord Gateway discovery returned no URL")
	}

	commandName := "discord-go-e2e-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := framework.Register(bot.Slash(
		commandName,
		"Temporary discord.go live command lifecycle probe",
		func(*bot.Context) error { return nil },
	)); err != nil {
		t.Fatalf("register high-level command: %v", err)
	}
	report, err := framework.SyncCommandDiff(
		testCtx,
		application.ID,
		"",
		bot.WithCommandSyncDryRun(true),
		bot.WithDeleteUnknownCommands(false),
	)
	if err != nil {
		t.Fatalf("plan high-level command sync: %v", err)
	}
	if report == nil || !report.DryRun {
		t.Fatal("command sync did not return a dry-run report")
	}

	type readyResult struct {
		userID        string
		applicationID string
		guildCount    int
	}
	ready := make(chan readyResult, 1)
	removeReady := session.AddHandlerOnce(func(_ *dgo.Session, event *dgo.Ready) {
		result := readyResult{guildCount: len(event.Guilds)}
		if event.User != nil {
			result.userID = event.User.ID
		}
		if event.Application != nil {
			result.applicationID = event.Application.ID
		}
		ready <- result
	})
	defer removeReady()

	runCtx, cancelRun := context.WithCancel(testCtx)
	runDone := make(chan error, 1)
	go func() {
		runDone <- framework.RunWithShutdown(runCtx, 5*time.Second)
	}()

	var gatewayReady readyResult
	select {
	case gatewayReady = <-ready:
	case err = <-runDone:
		t.Fatalf("high-level bot stopped before Gateway READY: %v", err)
	case <-testCtx.Done():
		t.Fatalf("wait for Discord Gateway READY: %v", testCtx.Err())
	}

	if gatewayReady.userID != currentUser.ID {
		t.Errorf("Gateway user ID = %q, REST user ID = %q", gatewayReady.userID, currentUser.ID)
	}
	if gatewayReady.applicationID != "" && gatewayReady.applicationID != application.ID {
		t.Errorf("Gateway application ID = %q, REST application ID = %q", gatewayReady.applicationID, application.ID)
	}

	commandTested := false
	if guildID != "" {
		if err := testGuildCommandLifecycle(testCtx, framework, application.ID, guildID, commandName); err != nil {
			t.Errorf("guild command lifecycle: %v", err)
		} else {
			commandTested = true
		}
	} else {
		t.Logf("guild command lifecycle skipped; %s is not set", liveGuildEnvironment)
	}

	voiceTested := false
	if voiceChannelID != "" {
		if err := testVoiceLifecycle(testCtx, session, guildID, voiceChannelID); err != nil {
			t.Errorf("Voice lifecycle: %v", err)
		} else {
			voiceTested = true
		}
	} else {
		t.Logf("Voice lifecycle skipped; %s is not set", liveVoiceEnvironment)
	}

	cancelRun()
	select {
	case err = <-runDone:
		if err != nil {
			t.Fatalf("gracefully stop high-level bot: %v", err)
		}
	case <-testCtx.Done():
		t.Fatalf("wait for graceful high-level bot shutdown: %v", testCtx.Err())
	}

	t.Logf(
		"live Discord E2E passed for bot %s (application %s, %d visible guilds, command=%t, voice=%t)",
		currentUser.ID,
		application.ID,
		gatewayReady.guildCount,
		commandTested,
		voiceTested,
	)
}

func testGuildCommandLifecycle(ctx context.Context, framework *bot.Bot, applicationID, guildID, commandName string) (resultErr error) {
	report, err := framework.SyncCommandDiff(
		ctx,
		applicationID,
		guildID,
		bot.WithDeleteUnknownCommands(false),
	)
	if err != nil {
		return fmt.Errorf("register temporary command: %w", err)
	}
	var commandID string
	if len(report.Created) > 0 && report.Created[0] != nil {
		commandID = report.Created[0].ID
	}
	defer func() {
		if commandID == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := framework.Session().ApplicationCommandDelete(
			applicationID,
			guildID,
			commandID,
			dgo.WithContext(cleanupCtx),
		); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("cleanup temporary command: %w", err))
		}
	}()
	if len(report.Created) != 1 || commandID == "" || report.Created[0].Name != commandName {
		return fmt.Errorf("register temporary command: created=%d, want one %q command", len(report.Created), commandName)
	}

	remote, err := framework.Session().ApplicationCommand(applicationID, guildID, commandID, dgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("read registered command: %w", err)
	}
	if remote == nil || remote.ID != commandID || remote.Name != commandName {
		return fmt.Errorf("registered command mismatch: %#v", remote)
	}

	secondReport, err := framework.SyncCommandDiff(
		ctx,
		applicationID,
		guildID,
		bot.WithDeleteUnknownCommands(false),
	)
	if err != nil {
		return fmt.Errorf("resync temporary command: %w", err)
	}
	if len(secondReport.Unchanged) != 1 || len(secondReport.Created) != 0 || len(secondReport.Updated) != 0 {
		return fmt.Errorf(
			"resync result: unchanged=%d created=%d updated=%d",
			len(secondReport.Unchanged),
			len(secondReport.Created),
			len(secondReport.Updated),
		)
	}

	if err := framework.Session().ApplicationCommandDelete(applicationID, guildID, commandID, dgo.WithContext(ctx)); err != nil {
		return fmt.Errorf("delete temporary command: %w", err)
	}
	commands, err := framework.Session().ApplicationCommands(applicationID, guildID, dgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("verify temporary command deletion: %w", err)
	}
	for _, command := range commands {
		if command != nil && command.Name == commandName {
			return fmt.Errorf("temporary command %q still exists after deletion", commandName)
		}
	}
	commandID = ""
	return nil
}

func testVoiceLifecycle(ctx context.Context, session *dgo.Session, guildID, channelID string) (resultErr error) {
	channel, err := session.Channel(channelID, dgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("fetch configured Voice channel: %w", err)
	}
	if channel == nil || channel.GuildID != guildID || channel.Type != dgo.ChannelTypeGuildVoice {
		return fmt.Errorf("configured channel must be a standard Voice channel in guild %s", guildID)
	}

	connection, err := session.ChannelVoiceJoin(guildID, channelID, false, true)
	if err != nil {
		return fmt.Errorf("join Voice channel: %w", err)
	}
	defer func() {
		if connection == nil {
			return
		}
		if err := connection.Disconnect(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("cleanup Voice disconnect: %w", err))
		}
	}()
	if send, ready, _ := connection.OpusSendState(); send == nil || !ready {
		return errors.New("Voice transport was not ready after join")
	}

	player, err := voice.NewPlayer(connection, 2)
	if err != nil {
		return fmt.Errorf("create Opus player: %w", err)
	}
	if err := player.SetVolume(0.2); err != nil {
		return fmt.Errorf("set test playback volume: %w", err)
	}
	source, err := voice.NewSliceSource(testTonePCM(testToneFrames), 2)
	if err != nil {
		return fmt.Errorf("create test tone source: %w", err)
	}
	queue, err := voice.NewQueue(player, voice.WithQueueContinueOnError(false))
	if err != nil {
		return fmt.Errorf("create Voice queue: %w", err)
	}
	if err := queue.Enqueue(source); err != nil {
		return fmt.Errorf("enqueue test tone: %w", err)
	}
	if err := connection.Speaking(true); err != nil {
		return fmt.Errorf("start Voice speaking state: %w", err)
	}
	speaking := true
	defer func() {
		if speaking {
			if err := connection.Speaking(false); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("cleanup Voice speaking state: %w", err))
			}
		}
	}()
	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start Voice queue: %w", err)
	}
	defer func() {
		if !queue.IsRunning() {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.Stop(cleanupCtx); err != nil && !errors.Is(err, voice.ErrQueueNotRunning) {
			resultErr = errors.Join(resultErr, fmt.Errorf("cleanup Voice queue: %w", err))
		}
	}()
	if err := queue.Drain(ctx); err != nil {
		return fmt.Errorf("play and drain Voice queue: %w", err)
	}
	if err := connection.Speaking(false); err != nil {
		return fmt.Errorf("stop Voice speaking state: %w", err)
	}
	speaking = false

	metrics := connection.Metrics()
	if metrics.ConsecutiveDAVEEncryptFailures != 0 {
		return fmt.Errorf("DAVE encryption failures after playback: %d", metrics.ConsecutiveDAVEEncryptFailures)
	}
	disconnectErr := connection.Disconnect()
	_, ready, _ := connection.OpusSendState()
	connection = nil
	if disconnectErr != nil {
		return fmt.Errorf("disconnect Voice channel: %w", disconnectErr)
	}
	if ready {
		return errors.New("Voice transport remained ready after disconnect")
	}
	session.RLock()
	_, retained := session.VoiceConnections[guildID]
	session.RUnlock()
	if retained {
		return errors.New("Voice connection remained registered after disconnect")
	}
	return nil
}

func testTonePCM(frames int) []int16 {
	const (
		channels  = 2
		frequency = 440.0
		amplitude = 6000.0
	)
	samplesPerChannel := frames * voice.FrameSize
	pcm := make([]int16, samplesPerChannel*channels)
	for sample := range samplesPerChannel {
		value := int16(math.Round(amplitude * math.Sin(2*math.Pi*frequency*float64(sample)/float64(voice.SampleRate))))
		pcm[sample*channels] = value
		pcm[sample*channels+1] = value
	}
	return pcm
}

func TestTestTonePCMIsFrameAlignedStereo(t *testing.T) {
	pcm := testTonePCM(2)
	if len(pcm) != 2*voice.FrameSize*2 {
		t.Fatalf("test tone samples = %d, want %d", len(pcm), 2*voice.FrameSize*2)
	}
	nonzero := false
	for index := 0; index < len(pcm); index += 2 {
		if pcm[index] != pcm[index+1] {
			t.Fatalf("stereo samples differ at frame sample %d", index/2)
		}
		if pcm[index] != 0 {
			nonzero = true
		}
	}
	if !nonzero {
		t.Fatal("test tone contains only silence")
	}
}
