package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/darui3018823/discord.go/bot"
)

func main() {
	token := os.Getenv("DISCORD_TOKEN")
	applicationID := os.Getenv("DISCORD_APPLICATION_ID")
	if token == "" || applicationID == "" {
		log.Fatal("DISCORD_TOKEN and DISCORD_APPLICATION_ID are required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := bot.NewWithToken(token)
	if err != nil {
		log.Fatal(err)
	}
	if err := client.Register(bot.Slash("ping", "Replies with pong", func(ctx *bot.Context) error {
		return ctx.Reply("Pong!")
	})); err != nil {
		log.Fatal(err)
	}

	// Set DISCORD_GUILD_ID during development for near-immediate guild command
	// updates. Leave it empty to synchronize global commands.
	if _, err := client.SyncCommands(ctx, applicationID, os.Getenv("DISCORD_GUILD_ID")); err != nil {
		log.Fatal(err)
	}
	if err := client.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
