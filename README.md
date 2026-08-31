# discord.go

[![Go Reference](https://pkg.go.dev/badge/github.com/darui3018823/discord.go.svg)](https://pkg.go.dev/github.com/darui3018823/discord.go)

discord.go is a batteries-included Discord framework for Go. It embeds
the proven REST, Gateway, interaction, state, and Voice implementation forked
from [dgo](https://github.com/darui3018823/dgo), then adds an opinionated bot,
command, component, task, and audio layer on top.

The copied low-level API is intentionally kept available while the high-level
API is developed. Its root Go package is still named `dgo` for now, so callers
may import it with a `discord` alias when preferred.

## Highlights

- Slash, context-menu, autocomplete, component, modal, and prefix routing
- Typed arguments, checks, permissions, cooldowns, middleware, and error scopes
- Transactional Cog/extensions and lifecycle-managed task loops
- Semantic command-diff synchronization with dry-run support
- Pure Go PCM/Opus Voice source queue, volume, pause/resume/skip, FEC, and PLC
- Discord API v10 REST and Gateway bindings
- Gateway event handling, state tracking, and voice connections
- DAVE end-to-end voice encryption support
- Context-aware REST rate limiting and bounded retries
- Structured logging with credential redaction
- Safe defaults: no Gateway intents and no parsed mentions unless enabled
- Go 1.26.6 or newer

## Install

discord.go is currently developed as an unpublished fork. After the repository
is published, install it with:

```sh
go get github.com/darui3018823/discord.go@latest
```

The copied root package is currently named `dgo`. The high-level framework is
available from the `bot` package:

```go
import (
	"context"
	"os"
	"os/signal"

	"github.com/darui3018823/discord.go/bot"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := bot.NewWithToken(os.Getenv("DISCORD_TOKEN"))
	if err != nil {
		panic(err)
	}
	if err := client.Register(bot.Slash("ping", "Replies with pong", func(ctx *bot.Context) error {
		return ctx.Reply("Pong!")
	})); err != nil {
		panic(err)
	}
	if _, err := client.SyncCommandDiff(ctx, os.Getenv("DISCORD_APPLICATION_ID"), ""); err != nil {
		panic(err)
	}
	if err := client.Run(ctx); err != nil {
		panic(err)
	}
}
```

The low-level API remains available directly:

```go
import "github.com/darui3018823/discord.go"
```

Create a bot session from a raw bot token. `NewBot` adds the required `Bot `
authorization prefix:

```go
session, err := dgo.NewBot(token)
if err != nil {
	return err
}
session.Identify.Intents = dgo.IntentsGuilds
```

Gateway connections accept bot credentials only. OAuth2 bearer credentials
may be used with applicable REST endpoints. Automated user accounts and
private client routes are not supported.

## Documentation

- [Getting started](docs/GettingStarted.md)
- [High-level framework](docs/HighLevel.md)
- [Voice framework](docs/VoiceFramework.md)
- [Live Discord E2E validation](docs/E2E.md)
- [Migration, compatibility, and deprecation policy](docs/Migration.md)
- [Webhook Events and Application Identity Profiles](docs/WebhookEvents.md)
- [v1.1.0 release notes](docs/releases/v1.1.0.md)
- [Public API inventory](docs/API.md)
- [Package reference](https://pkg.go.dev/github.com/darui3018823/discord.go)
- [Examples](examples)

The exported `VERSION` value and `Version()` function are resolved from Go
build information. Tagged module builds report their tag, pseudo-version
builds report the pseudo-version, and local builds report a `devel` version
with VCS information when available.

## Support and contributing

For dgo bugs and feature requests, use
[GitHub Issues](https://github.com/darui3018823/discord.go/issues). For general Go
discussion, visit the [Discord Gophers](https://discord.gg/golang) community.

Before opening a pull request:

1. Open or reference an issue describing the change.
2. Follow the repository's current naming and compatibility conventions.
3. Run the root and nested-module verification described by CI.
4. Target the `master` branch.

## Attribution

dgo retains the BSD-3-Clause lineage and attribution of the upstream project.
Thanks to the upstream maintainers and contributors, including
[Chris Rhodes](https://github.com/iopred), who created the original upstream
project logo used by the inherited documentation assets.
