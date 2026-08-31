# Getting started

This guide creates either a high-level discord.go bot or a low-level `dgo`
session using the public Discord Bot API.

## Requirements

- Go 1.26.6 or newer
- A Discord application with a bot user
- A bot token stored outside source control

Create applications in the
[Discord Developer Portal](https://discord.com/developers/applications).
Automated user accounts, raw user tokens, and private Discord client routes
are not supported.

## Install

Create or open a Go module, then add dgo:

```sh
go mod init example.com/my-bot
go get github.com/darui3018823/discord.go@latest
```

There is no need to copy the repository into `GOPATH` or run `go install` for
the library.

## High-level bot

For ordinary bot development, start with the `bot` package. This fragment
assumes `ctx` is a cancellable signal context and the IDs come from your
environment:

```go
client, err := bot.NewWithToken(os.Getenv("DISCORD_BOT_TOKEN"))
if err != nil {
	log.Fatal(err)
}
if err := client.Register(bot.Slash("ping", "Replies with pong", func(ctx *bot.Context) error {
	return ctx.Reply("Pong!")
})); err != nil {
	log.Fatal(err)
}
if _, err := client.SyncCommandDiff(ctx, applicationID, guildID); err != nil {
	log.Fatal(err)
}
if err := client.Run(ctx); err != nil {
	log.Fatal(err)
}
```

Use a guild ID while developing so command updates appear quickly. See the
[high-level framework guide](HighLevel.md) for prefix commands, typed options,
components, extensions, task loops, and lifecycle handling.

## Minimal low-level session

```go
package main

import (
	"log"
	"os"
	"os/signal"

	"github.com/darui3018823/discord.go"
)

func main() {
	token := os.Getenv("DISCORD_BOT_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_BOT_TOKEN is required")
	}

	session, err := dgo.NewBot(token)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	// dgo starts with no Gateway intents. Request only what the bot needs.
	session.Identify.Intents = dgo.IntentsGuilds
	session.AddHandler(func(_ *dgo.Session, ready *dgo.Ready) {
		log.Printf("connected as %s", ready.User.Username)
	})

	if err := session.Open(); err != nil {
		log.Fatal(err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
```

Run it with the token in the environment:

```sh
go run .
```

## Safe defaults

- `New`, `NewBot`, and `NewOAuth2` default to `IntentsNone`. Add only the
  intents required by the bot before opening a Gateway connection.
- Messages default to an empty `allowed_mentions.parse` list. Opt in to
  mentions explicitly when needed.
- `NewBot` accepts a raw bot token and adds the required `Bot ` prefix. If you
  use the generic `New` constructor, pass the full `Bot <token>` credential.
- Interaction and webhook tokens may appear in request URLs; dgo redacts them
  from its diagnostic metadata and logs.

Privileged intents must also be enabled for the application in the Developer
Portal. See Discord's
[Gateway intents documentation](https://docs.discord.com/developers/events/gateway#gateway-intents).

## Next steps

- Browse the [examples on GitHub](https://github.com/darui3018823/discord.go/tree/master/examples).
- Build with the [high-level framework](HighLevel.md).
- Add playback or receive pipelines with the [Voice framework](VoiceFramework.md).
- Review the [migration and compatibility guide](Migration.md).
- Check the [public API inventory](API.md).
- Use the [package reference](https://pkg.go.dev/github.com/darui3018823/discord.go)
  for exported types and methods.
