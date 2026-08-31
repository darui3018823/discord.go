# Getting started

To start off, check out existing pull requests and issues to get a sense of
what problems we’re currently solving and what features you can implement.

## Issues

Our issues are mostly used for bugs, however we welcome refactoring and conceptual issues.

Any other conversation would belong and would be moved into “Discussions”.

## Discussions

We use discussions for ideas, polls, announcements and help questions.

Don’t hesitate to ask; we will always try to help.

## Pull Requests

If you want to help us by improving existing features or adding new ones,
create a pull request (PR). It allows us to review your code, suggest changes,
and merge it.

Here are some tips on how to make a good first PR:

- When creating a PR, please consider a distinctive name and description for it, so the maintainers can understand what your PR changes / adds / removes.
- It’s always a good idea to link documentation when implementing a new feature / endpoint
- If you’re resolving an issue, don’t forget to [link it](https://docs.github.com/en/issues/tracking-your-work-with-issues/linking-a-pull-request-to-an-issue) in the description.
- Enable the checkbox to allow maintainers to edit your PR and make commits in the PR branch when necessary.
- We may ask for changes, usually through suggestions or pull request comments. You can apply suggestions right in the UI. Any other change needs to be done manually.
- Don’t forget to mark PR comments resolved when you’re done applying the changes.
- Be patient and don’t close and reopen your PR when no one responds, sometimes it might be held for a while. There might be a lot of reasons: release preparation, the feature is not significant, maintainers are busy, etc.


When your changes are still incomplete (i.e. in Work In Progress state), you can still create a PR, but consider making it a draft. 
To make a draft PR, you can change the type of PR by clicking to a triangle next to the “Create Pull Request” button.

Once you’re done, you can mark it as “Ready for review”, and we’ll get right on it.


# Code style

To standardize and make things less messy we have a certain code style, that is persistent throughout the codebase.

## Naming

### REST methods

When naming a REST method, while it might seem counterintuitive, we specify the entity before the action verb (for GET endpoints we don’t specify one however). Here’s an example:

> Endpoint name: Get Channel Message
>
> Method name: `ChannelMessage`

> Endpoint name: Edit Channel Message
>
> Method name: `ChannelMessageEdit`

### Parameter structures

When making a complex REST endpoint, sometimes you might need to implement a `Param` structure. This structure contains parameters for certain endpoint/set of endpoints.

- If an endpoint/set of endpoints have mostly same parameters, it’s a good idea to use a single `Param` structure for them. Here’s an example:
    
    > Endpoint: `GuildMemberEdit`
    >
    > `Param` structure: `GuildMemberParams` 
- If an endpoint/set of endpoints have differentiating parameters, `Param` structure can be named after the endpoint’s verb. Here’s an example:
    
    > Endpoint: `ChannelMessageSendComplex`
    >
    > `Param` structure: `MessageSend`
    
    > Endpoint: `ChannelMessageEditComplex`
    >
    > `Param` structure: `MessageEdit` 

### Events

When naming an event, we follow gateway’s internal naming (which often matches with the official event name in the docs). Here’s an example:

> Event name: Interaction Create (`INTERACTION_CREATE`)
>
> Structure name: `InteractionCreate`

## Returns

In our REST functions we usually favor named returns instead of regular anonymous returns. This helps readability.

Additionally, we try to avoid naked return statements for functions with a
long body, since it’s easier to lose track of the return result.

## Documentation

Install the documentation dependencies and build the site in strict mode:

```sh
python -m pip install -r requirements-docs.txt
mkdocs build --clean --strict
```

When changing a public API or example, update the corresponding documentation
and verify the rendered links with this build.

## Testing

Run the complete contributor suite from the repository root:

```sh
go run ./tools/cmd/fulltest
```

No Discord credentials are required for formatting, race tests, vet, coverage,
or documentation validation. The live package skips automatically when its
environment is not configured.

GoLand and other JetBrains IDEs with the Go plugin load two shared Run
Configurations from `.idea/runConfigurations`:

- **Fulltest (Offline)** always removes the live-test variables for that run.
- **Fulltest (Interactive Live)** asks in the Run console whether to enable the
  live phase, then accepts the optional guild and standard Voice channel IDs.

The interactive configuration does not ask for the bot token because Run
console input is echoed. Configure `test_bot_token` in the operating-system or
your private IDE environment, never in the shared XML. If the variable was
added after the IDE started, restart the IDE so the Run process inherits it.
Press Enter at the first prompt to run offline. Existing resource IDs are shown
as defaults; enter `-` to clear one for the current run.

### Live Discord command and Voice test

Use a dedicated test application and test guild. Do not use a production bot
or a Voice channel with users who have not agreed to hear the one-second test
tone. The bot needs these permissions in the configured guild:

- View Channels
- Connect and Speak in the selected standard Voice channel
- the `bot` and `applications.commands` installation scopes

Set a raw bot token and explicit test resource IDs:

```text
test_bot_token
test_guild_id
test_voice_channel_id
```

PowerShell:

```powershell
$env:test_bot_token = "YOUR_RAW_TEST_BOT_TOKEN"
$env:test_guild_id = "YOUR_TEST_GUILD_ID"
$env:test_voice_channel_id = "YOUR_STANDARD_VOICE_CHANNEL_ID"
go run ./tools/cmd/fulltest
```

POSIX shells:

```sh
export test_bot_token=YOUR_RAW_TEST_BOT_TOKEN
export test_guild_id=YOUR_TEST_GUILD_ID
export test_voice_channel_id=YOUR_STANDARD_VOICE_CHANNEL_ID
go run ./tools/cmd/fulltest
```

The live phase performs the following lifecycle:

1. Authenticate through REST and receive Gateway READY.
2. Register one uniquely named temporary guild slash command through the
   high-level command-diff API.
3. Fetch it, resynchronize it as unchanged, delete it, and verify deletion.
4. Validate that the configured channel belongs to the configured guild and is
   a standard Voice channel.
5. Join self-deafened, encode and play a low-volume one-second 440 Hz stereo
   tone through the high-level Opus queue, and drain the queue.
6. Stop speaking, verify DAVE has no consecutive encryption failures,
   disconnect, and verify the Voice transport was removed from the Session.

Cleanup retries command deletion and Voice disconnection when an intermediate
assertion fails. The test never prints the token. Setting only
`test_bot_token` runs the read-only connectivity phase; adding
`test_guild_id` enables command registration; adding all three variables runs
the complete command and Voice lifecycle. See [the live E2E runbook](docs/E2E.md)
for limitations and manual interaction/receive checks.
