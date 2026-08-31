// Package e2e contains opt-in tests against the live Discord service.
package e2e_test

import (
	"context"
	"os"
	"testing"
	"time"

	dgo "github.com/darui3018823/discord.go"
	"github.com/darui3018823/discord.go/bot"
)

const liveTestTimeout = 45 * time.Second

// TestLiveBotConnectivity verifies the read-only REST surface, high-level
// command planning, Gateway READY dispatch, and graceful Bot shutdown. It does
// not create commands, send messages, or otherwise mutate Discord resources.
//
// Set test_bot_token to a raw token for a dedicated test bot to enable it.
func TestLiveBotConnectivity(t *testing.T) {
	if testing.Short() {
		t.Skip("live Discord E2E is disabled by -short")
	}
	token := os.Getenv("test_bot_token")
	if token == "" {
		t.Skip("test_bot_token is not set")
	}

	testCtx, cancelTest := context.WithTimeout(context.Background(), liveTestTimeout)
	defer cancelTest()

	framework, err := bot.NewWithToken(token)
	if err != nil {
		t.Fatalf("create high-level bot: %v", err)
	}
	session := framework.Session()
	session.Identify.Intents = dgo.IntentsGuilds

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

	if err := framework.Register(bot.Slash(
		"discord-go-e2e",
		"Read-only discord.go live connectivity probe",
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
		"live Discord E2E passed for bot %s (application %s, %d visible guilds)",
		currentUser.ID,
		application.ID,
		gatewayReady.guildCount,
	)
}
