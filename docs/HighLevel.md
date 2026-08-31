# High-level framework

The `bot` package is the discord.py-style layer. It owns dispatch, command
policy, extensions, background loops, synchronization, and graceful shutdown,
while `Bot.Session()` keeps the complete low-level API available.

## Create and run a bot

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()

client, err := bot.NewWithToken(os.Getenv("DISCORD_TOKEN"))
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

`Run` opens the Gateway and calls the full `Bot.CloseContext` path after
cancellation, using `DefaultShutdownTimeout`. Use `RunWithShutdown` to select a
different bound, or call `CloseContext` directly during manual lifecycle
management. Shutdown unloads
extensions, finishes the current iteration of managed loops, cancels loops if
the deadline expires, detaches dispatch handlers, and closes the Session.

## Slash and context-menu commands

Commands are validated as a batch and cloned into the router. Add Discord
option definitions to a command and use safe typed accessors in the handler:

```go
greet := bot.Slash("greet", "Greet someone", func(ctx *bot.Context) error {
	name, err := ctx.String("name")
	if err != nil {
		return err
	}
	return ctx.Reply("Hello, " + name)
})
greet.Definition.Options = []*dgo.ApplicationCommandOption{{
	Type: dgo.ApplicationCommandOptionString,
	Name: "name",
	Description: "Who to greet",
	Required: true,
}}
```

`Context` also provides integer, number, boolean, user/member, role, channel,
mentionable, and attachment accessors. Missing, malformed, and wrong-type
values return typed errors instead of panicking. `UserCommand` and
`MessageCommand` register context menus.

Use `Sub`, `Group`, `AddSubcommands`, and `AddGroups` for command trees. Checks,
middleware, autocomplete handlers, and error handlers compose from Bot to
command to subcommand. The closest error handler wins.

## Checks, middleware, permissions, and cooldowns

Global policy is registered with `AddChecks` and `Use`; commands and
subcommands have matching local methods. Built-in checks include `GuildOnly`,
`DMOnly`, `HasPermissions`, and `BotHasPermissions`.

```go
cooldown, err := bot.NewCooldown(2, 10*time.Second, bot.CooldownPerUser)
if err != nil {
	return err
}
command.AddChecks(bot.GuildOnly(), cooldown.Check)
```

Checks and handlers are panic-isolated. Errors preserve their cause for
`errors.Is` and `errors.As` and flow through the nearest configured error
scope.

## Prefix commands

Prefix routing supports multiple longest-match prefixes, case-insensitive
aliases, nested commands, quoted/escaped arguments, typed positional
conversion, checks, middleware, permissions, cooldowns, and local error
scopes.

```go
client.Session().Identify.Intents |= dgo.IntentsGuildMessages | dgo.IntentsMessageContent
if err := client.SetPrefixes("!", "?"); err != nil {
	return err
}
if err := client.RegisterPrefixCommands(bot.TextCommand("ping", func(ctx *bot.PrefixContext) error {
	_, err := ctx.Reply("Pong!")
	return err
})); err != nil {
	return err
}
```

Message Content is a privileged intent for many applications and must also be
enabled in the Developer Portal. Bot-authored messages are ignored.

## Components, selects, modals, and autocomplete

Register exact custom IDs or dynamic prefixes. Exact routes win; otherwise the
longest prefix wins and `Suffix` contains the remaining custom ID.

```go
client.RegisterComponentPrefix("ticket:", func(ctx *bot.ComponentContext) error {
	return ctx.ReplyEphemeral("Ticket " + ctx.Suffix)
})
client.RegisterModal("profile-edit", func(ctx *bot.ModalContext) error {
	name, err := ctx.Text("display-name")
	if err != nil {
		return err
	}
	return ctx.ReplyEphemeral("Saved " + name)
})
```

Autocomplete is attached to the command or leaf subcommand option. The router
passes the focused option and supplies a response helper that enforces
Discord's choice limit.

## Transactional extensions (Cogs)

An `Extension` stages all registrations in an `ExtensionRegistrar`. Nothing is
published until Setup succeeds and every command, alias, component, modal,
event, and loop conflict check passes. Unload removes only resources owned by
that extension before calling optional teardown.

```go
extension := bot.NewExtension("admin", func(ctx context.Context, r *bot.ExtensionRegistrar) error {
	if err := r.AddCommands(bot.Slash("health", "Health check", healthHandler)); err != nil {
		return err
	}
	return r.AddEventHandler(func(_ *dgo.Session, ready *dgo.Ready) {
		log.Printf("ready as %s", ready.User.Username)
	})
}, func(context.Context) error {
	return closeExternalResources()
})

if err := client.LoadExtension(ctx, extension); err != nil {
	return err
}
```

`Cog`, `LoadCog`, and `UnloadCog` are aliases. Extension-owned task loops are
started only after the transaction commits and are stopped automatically.

## Managed task loops

`Loop` is a non-overlapping periodic worker with before/after/error hooks,
optional iteration count, dynamic interval changes, panic isolation, graceful
Stop, immediate Cancel, status inspection, and restart support.

```go
refresh, err := bot.NewLoop(time.Minute, refreshCache,
	bot.WithLoopName("cache-refresh"),
	bot.WithContinueOnError(true),
	bot.WithLoopErrorHandler(func(_ context.Context, err error) error {
		log.Printf("refresh failed: %v", err)
		return nil
	}),
)
if err != nil {
	return err
}
if err := client.StartLoop(refresh); err != nil {
	return err
}
```

Use `ExtensionRegistrar.AddLoops` inside a Cog so ownership follows unload.
Do not start an extension-owned loop directly while the extension is loaded.

## Command synchronization

`SyncCommandDiff` first lists Discord's commands, ignores server-only metadata
and equivalent omitted defaults, then creates, edits, or deletes only changed
definitions. This avoids needless command churn.

```go
report, err := client.SyncCommandDiff(ctx, applicationID, guildID,
	bot.WithCommandSyncDryRun(false),
	bot.WithDeleteUnknownCommands(true),
)
```

Use dry-run in deployment checks and inspect `report.Changes`. The older
`SyncCommands` bulk-overwrite method remains available when full replacement
is explicitly desired.
