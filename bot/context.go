package bot

import (
	"context"
	"errors"

	dgo "github.com/darui3018823/discord.go"
)

// Context contains the high-level state for one command invocation.
type Context struct {
	context.Context
	Bot         *Bot
	Session     *dgo.Session
	Interaction *dgo.Interaction
	Data        dgo.ApplicationCommandInteractionData
	// CommandPath contains the selected subcommand path, excluding the
	// top-level command name. It is empty for a top-level handler.
	CommandPath []string
	// Options contains the leaf command options. For a subcommand this omits
	// the enclosing subcommand and group wrappers.
	Options []*dgo.ApplicationCommandInteractionDataOption
}

// Option returns a top-level application-command option by name.
func (c *Context) Option(name string) *dgo.ApplicationCommandInteractionDataOption {
	for _, option := range c.Options {
		if option != nil && option.Name == name {
			return option
		}
	}
	return nil
}

// User returns the user that invoked the command.
func (c *Context) User() *dgo.User {
	if c.Interaction == nil {
		return nil
	}
	if c.Interaction.Member != nil {
		return c.Interaction.Member.User
	}
	return c.Interaction.User
}

// Respond sends a complete interaction response.
func (c *Context) Respond(response *dgo.InteractionResponse) error {
	if c.Session == nil || c.Interaction == nil {
		return errors.New("command context has no active interaction")
	}
	options := []dgo.RequestOption(nil)
	if c.Context != nil {
		options = append(options, dgo.WithContext(c.Context))
	}
	return c.Session.InteractionRespond(c.Interaction, response, options...)
}

// Reply sends a text response with safe mention defaults.
func (c *Context) Reply(content string) error {
	return c.Respond(&dgo.InteractionResponse{
		Type: dgo.InteractionResponseChannelMessageWithSource,
		Data: &dgo.InteractionResponseData{
			Content:         content,
			AllowedMentions: &dgo.MessageAllowedMentions{},
		},
	})
}

// ReplyEphemeral sends a text response visible only to the caller.
func (c *Context) ReplyEphemeral(content string) error {
	return c.Respond(&dgo.InteractionResponse{
		Type: dgo.InteractionResponseChannelMessageWithSource,
		Data: &dgo.InteractionResponseData{
			Content:         content,
			Flags:           dgo.MessageFlagsEphemeral,
			AllowedMentions: &dgo.MessageAllowedMentions{},
		},
	})
}

// Defer acknowledges the interaction so the command can respond later.
func (c *Context) Defer(ephemeral bool) error {
	data := &dgo.InteractionResponseData{}
	if ephemeral {
		data.Flags = dgo.MessageFlagsEphemeral
	}
	return c.Respond(&dgo.InteractionResponse{
		Type: dgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: data,
	})
}
