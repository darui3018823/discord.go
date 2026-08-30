package bot

import (
	"errors"
	"fmt"
	"math"

	dgo "github.com/darui3018823/discord.go"
)

// ErrOptionNotFound is returned when a required command option is absent.
var ErrOptionNotFound = errors.New("command option not found")

// OptionTypeError describes a command option used with the wrong typed
// accessor or carrying a malformed value.
type OptionTypeError struct {
	Name string
	Want dgo.ApplicationCommandOptionType
	Got  dgo.ApplicationCommandOptionType
}

func (e *OptionTypeError) Error() string {
	return fmt.Sprintf("command option %q has type %s, want %s", e.Name, e.Got, e.Want)
}

func (c *Context) typedOption(name string, want ...dgo.ApplicationCommandOptionType) (*dgo.ApplicationCommandInteractionDataOption, error) {
	option := c.Option(name)
	if option == nil {
		return nil, fmt.Errorf("%w: %s", ErrOptionNotFound, name)
	}
	for _, typeID := range want {
		if option.Type == typeID {
			return option, nil
		}
	}
	return nil, &OptionTypeError{Name: name, Want: want[0], Got: option.Type}
}

// String returns a string option without using the panic-based low-level
// accessor.
func (c *Context) String(name string) (string, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionString)
	if err != nil {
		return "", err
	}
	value, ok := option.Value.(string)
	if !ok {
		return "", malformedOptionValue(name, option.Type)
	}
	return value, nil
}

// Integer returns an integer option.
func (c *Context) Integer(name string) (int64, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionInteger)
	if err != nil {
		return 0, err
	}
	switch value := option.Value.(type) {
	case float64:
		const maxDiscordInteger = float64(1 << 53)
		if math.Trunc(value) != value || value < -maxDiscordInteger || value > maxDiscordInteger {
			return 0, malformedOptionValue(name, option.Type)
		}
		return int64(value), nil
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	default:
		return 0, malformedOptionValue(name, option.Type)
	}
}

// Number returns a numeric option.
func (c *Context) Number(name string) (float64, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionNumber)
	if err != nil {
		return 0, err
	}
	switch value := option.Value.(type) {
	case float64:
		return value, nil
	case float32:
		return float64(value), nil
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	default:
		return 0, malformedOptionValue(name, option.Type)
	}
}

// Boolean returns a boolean option.
func (c *Context) Boolean(name string) (bool, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionBoolean)
	if err != nil {
		return false, err
	}
	value, ok := option.Value.(bool)
	if !ok {
		return false, malformedOptionValue(name, option.Type)
	}
	return value, nil
}

// User returns a resolved user option, falling back to an ID-only value when
// Discord omitted resolved data.
func (c *Context) UserOption(name string) (*dgo.User, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionUser, dgo.ApplicationCommandOptionMentionable)
	if err != nil {
		return nil, err
	}
	id, ok := option.Value.(string)
	if !ok {
		return nil, malformedOptionValue(name, option.Type)
	}
	if c.Data.Resolved != nil && c.Data.Resolved.Users[id] != nil {
		return c.Data.Resolved.Users[id], nil
	}
	return &dgo.User{ID: id}, nil
}

// MemberOption returns a resolved guild member for a user option. It returns
// ErrOptionNotFound when Discord did not include a guild member.
func (c *Context) MemberOption(name string) (*dgo.Member, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionUser, dgo.ApplicationCommandOptionMentionable)
	if err != nil {
		return nil, err
	}
	id, ok := option.Value.(string)
	if !ok {
		return nil, malformedOptionValue(name, option.Type)
	}
	if c.Data.Resolved == nil || c.Data.Resolved.Members[id] == nil {
		return nil, fmt.Errorf("%w: resolved member %s", ErrOptionNotFound, id)
	}
	member := *c.Data.Resolved.Members[id]
	if member.User == nil {
		member.User = c.Data.Resolved.Users[id]
	}
	return &member, nil
}

// Role returns a resolved role option, falling back to an ID-only value.
func (c *Context) Role(name string) (*dgo.Role, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionRole, dgo.ApplicationCommandOptionMentionable)
	if err != nil {
		return nil, err
	}
	id, ok := option.Value.(string)
	if !ok {
		return nil, malformedOptionValue(name, option.Type)
	}
	if c.Data.Resolved != nil && c.Data.Resolved.Roles[id] != nil {
		return c.Data.Resolved.Roles[id], nil
	}
	return &dgo.Role{ID: id}, nil
}

// Channel returns a resolved channel option, falling back to an ID-only value.
func (c *Context) Channel(name string) (*dgo.Channel, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionChannel)
	if err != nil {
		return nil, err
	}
	id, ok := option.Value.(string)
	if !ok {
		return nil, malformedOptionValue(name, option.Type)
	}
	if c.Data.Resolved != nil && c.Data.Resolved.Channels[id] != nil {
		return c.Data.Resolved.Channels[id], nil
	}
	return &dgo.Channel{ID: id}, nil
}

// Attachment returns a resolved attachment option.
func (c *Context) Attachment(name string) (*dgo.MessageAttachment, error) {
	option, err := c.typedOption(name, dgo.ApplicationCommandOptionAttachment)
	if err != nil {
		return nil, err
	}
	id, ok := option.Value.(string)
	if !ok {
		return nil, malformedOptionValue(name, option.Type)
	}
	if c.Data.Resolved == nil || c.Data.Resolved.Attachments[id] == nil {
		return nil, fmt.Errorf("%w: resolved attachment %s", ErrOptionNotFound, id)
	}
	return c.Data.Resolved.Attachments[id], nil
}

// TargetUser returns the selected user for a user context-menu command.
func (c *Context) TargetUser() (*dgo.User, error) {
	if c.Data.CommandType != dgo.UserApplicationCommand || c.Data.TargetID == "" {
		return nil, fmt.Errorf("%w: target user", ErrOptionNotFound)
	}
	if c.Data.Resolved != nil && c.Data.Resolved.Users[c.Data.TargetID] != nil {
		return c.Data.Resolved.Users[c.Data.TargetID], nil
	}
	return &dgo.User{ID: c.Data.TargetID}, nil
}

// TargetMessage returns the selected message for a message context-menu
// command.
func (c *Context) TargetMessage() (*dgo.Message, error) {
	if c.Data.CommandType != dgo.MessageApplicationCommand || c.Data.TargetID == "" || c.Data.Resolved == nil || c.Data.Resolved.Messages[c.Data.TargetID] == nil {
		return nil, fmt.Errorf("%w: target message", ErrOptionNotFound)
	}
	return c.Data.Resolved.Messages[c.Data.TargetID], nil
}

func malformedOptionValue(name string, typeID dgo.ApplicationCommandOptionType) error {
	return fmt.Errorf("command option %q has malformed %s value", name, typeID)
}
