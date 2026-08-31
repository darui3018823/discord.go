package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"time"

	dgo "github.com/darui3018823/discord.go"
	"github.com/darui3018823/discord.go/voice"
)

func main() {
	token := os.Getenv("DISCORD_TOKEN")
	guildID := os.Getenv("DISCORD_GUILD_ID")
	channelID := os.Getenv("DISCORD_VOICE_CHANNEL_ID")
	pcmPath := os.Getenv("PCM_S16LE_PATH")
	if token == "" || guildID == "" || channelID == "" || pcmPath == "" {
		log.Fatal("DISCORD_TOKEN, DISCORD_GUILD_ID, DISCORD_VOICE_CHANNEL_ID, and PCM_S16LE_PATH are required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	session, err := dgo.NewBot(token)
	if err != nil {
		log.Fatal(err)
	}
	session.Identify.Intents = dgo.IntentsGuilds | dgo.IntentsGuildVoiceStates
	if err := session.OpenWithContext(ctx); err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	connection, err := session.ChannelVoiceJoin(guildID, channelID, false, true)
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Disconnect()
	file, err := os.Open(pcmPath)
	if err != nil {
		log.Fatal(err)
	}
	source, err := voice.NewReaderSource(file, 2)
	if err != nil {
		log.Fatal(err)
	}
	player, err := voice.NewPlayer(connection, 2)
	if err != nil {
		log.Fatal(err)
	}
	queue, err := voice.NewQueue(player)
	if err != nil {
		log.Fatal(err)
	}
	if err := queue.Enqueue(source); err != nil {
		log.Fatal(err)
	}
	if err := queue.Start(ctx); err != nil {
		log.Fatal(err)
	}
	if err := queue.Drain(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Printf("playback failed: %v", err)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = queue.Stop(shutdownCtx)
	}
}
