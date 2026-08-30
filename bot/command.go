package bot

import (
	"fmt"
	"strings"

	dgo "github.com/darui3018823/discord.go"
)

// Handler handles one application-command interaction.
type Handler func(*Context) error

// Middleware wraps a command handler. Middleware is applied in registration
// order, so the first middleware registered is the outermost wrapper.
type Middleware func(Handler) Handler

// ErrorHandler receives errors returned by command handlers.
type ErrorHandler func(*Context, error)

// Command combines a Discord command definition with local handlers.
type Command struct {
	Definition *dgo.ApplicationCommand
	Handler    Handler
	routes     map[string]Handler
}

// Subcommand describes one chat-input subcommand and its leaf options.
type Subcommand struct {
	Name        string
	Description string
	Options     []*dgo.ApplicationCommandOption
	Handler     Handler
}

// Sub creates a chat-input subcommand.
func Sub(name, description string, handler Handler, options ...*dgo.ApplicationCommandOption) *Subcommand {
	return &Subcommand{Name: name, Description: description, Handler: handler, Options: options}
}

// SubcommandGroup describes a named group of subcommands.
type SubcommandGroup struct {
	Name        string
	Description string
	Commands    []*Subcommand
}

// Group creates a chat-input subcommand group.
func Group(name, description string, commands ...*Subcommand) *SubcommandGroup {
	return &SubcommandGroup{Name: name, Description: description, Commands: commands}
}

// Slash creates a chat-input command. handler may be nil when subcommands are
// added before registration.
func Slash(name, description string, handler Handler) *Command {
	return &Command{
		Definition: &dgo.ApplicationCommand{
			Type:        dgo.ChatApplicationCommand,
			Name:        name,
			Description: description,
		},
		Handler: handler,
		routes:  make(map[string]Handler),
	}
}

// UserCommand creates a user context-menu command.
func UserCommand(name string, handler Handler) *Command {
	return &Command{
		Definition: &dgo.ApplicationCommand{Type: dgo.UserApplicationCommand, Name: name},
		Handler:    handler,
		routes:     make(map[string]Handler),
	}
}

// MessageCommand creates a message context-menu command.
func MessageCommand(name string, handler Handler) *Command {
	return &Command{
		Definition: &dgo.ApplicationCommand{Type: dgo.MessageApplicationCommand, Name: name},
		Handler:    handler,
		routes:     make(map[string]Handler),
	}
}

// AddSubcommands adds top-level subcommands and derives their Discord command
// definitions. A chat-input command cannot mix ordinary top-level options with
// subcommands.
func (c *Command) AddSubcommands(commands ...*Subcommand) error {
	if err := c.validateSubcommandContainer(); err != nil {
		return err
	}
	for _, command := range commands {
		if err := validateSubcommand(command); err != nil {
			return err
		}
		if c.topLevelOptionExists(command.Name) {
			return fmt.Errorf("%w: duplicate subcommand %q", ErrInvalidCommand, command.Name)
		}
	}
	for _, command := range commands {
		c.Definition.Options = append(c.Definition.Options, &dgo.ApplicationCommandOption{
			Type:        dgo.ApplicationCommandOptionSubCommand,
			Name:        command.Name,
			Description: command.Description,
			Options:     cloneOptions(command.Options),
		})
		c.routes[routeKey([]string{command.Name})] = command.Handler
	}
	return nil
}

// AddGroups adds subcommand groups and derives their nested Discord command
// definitions.
func (c *Command) AddGroups(groups ...*SubcommandGroup) error {
	if err := c.validateSubcommandContainer(); err != nil {
		return err
	}
	for _, group := range groups {
		if group == nil || group.Name == "" || group.Description == "" || len(group.Commands) == 0 {
			return fmt.Errorf("%w: invalid subcommand group", ErrInvalidCommand)
		}
		if c.topLevelOptionExists(group.Name) {
			return fmt.Errorf("%w: duplicate subcommand group %q", ErrInvalidCommand, group.Name)
		}
		seen := make(map[string]struct{}, len(group.Commands))
		for _, command := range group.Commands {
			if err := validateSubcommand(command); err != nil {
				return err
			}
			if _, exists := seen[command.Name]; exists {
				return fmt.Errorf("%w: duplicate subcommand %q in group %q", ErrInvalidCommand, command.Name, group.Name)
			}
			seen[command.Name] = struct{}{}
		}
	}
	for _, group := range groups {
		option := &dgo.ApplicationCommandOption{
			Type:        dgo.ApplicationCommandOptionSubCommandGroup,
			Name:        group.Name,
			Description: group.Description,
		}
		for _, command := range group.Commands {
			option.Options = append(option.Options, &dgo.ApplicationCommandOption{
				Type:        dgo.ApplicationCommandOptionSubCommand,
				Name:        command.Name,
				Description: command.Description,
				Options:     cloneOptions(command.Options),
			})
			c.routes[routeKey([]string{group.Name, command.Name})] = command.Handler
		}
		c.Definition.Options = append(c.Definition.Options, option)
	}
	return nil
}

func (c *Command) validateSubcommandContainer() error {
	if c == nil || c.Definition == nil || keyFor(c.Definition).typeID != dgo.ChatApplicationCommand {
		return fmt.Errorf("%w: subcommands require a chat-input command", ErrInvalidCommand)
	}
	if c.routes == nil {
		c.routes = make(map[string]Handler)
	}
	for _, option := range c.Definition.Options {
		if option != nil && option.Type != dgo.ApplicationCommandOptionSubCommand && option.Type != dgo.ApplicationCommandOptionSubCommandGroup {
			return fmt.Errorf("%w: cannot mix top-level options and subcommands", ErrInvalidCommand)
		}
	}
	return nil
}

func validateSubcommand(command *Subcommand) error {
	if command == nil || command.Name == "" || command.Description == "" || command.Handler == nil {
		return fmt.Errorf("%w: invalid subcommand", ErrInvalidCommand)
	}
	for _, option := range command.Options {
		if option == nil || option.Type == dgo.ApplicationCommandOptionSubCommand || option.Type == dgo.ApplicationCommandOptionSubCommandGroup {
			return fmt.Errorf("%w: invalid leaf option in subcommand %q", ErrInvalidCommand, command.Name)
		}
	}
	return nil
}

func (c *Command) topLevelOptionExists(name string) bool {
	for _, option := range c.Definition.Options {
		if option != nil && option.Name == name {
			return true
		}
	}
	return false
}

func (c *Command) hasHandler() bool {
	return c != nil && (c.Handler != nil || len(c.routes) > 0)
}

func (c *Command) clone() *Command {
	copyCommand := &Command{Handler: c.Handler, routes: make(map[string]Handler, len(c.routes))}
	if c.Definition != nil {
		definition := *c.Definition
		definition.Options = cloneOptions(c.Definition.Options)
		copyCommand.Definition = &definition
	}
	for path, handler := range c.routes {
		copyCommand.routes[path] = handler
	}
	return copyCommand
}

func cloneOptions(options []*dgo.ApplicationCommandOption) []*dgo.ApplicationCommandOption {
	cloned := make([]*dgo.ApplicationCommandOption, len(options))
	for index, option := range options {
		if option == nil {
			continue
		}
		copyOption := *option
		copyOption.Options = cloneOptions(option.Options)
		if option.Choices != nil {
			copyOption.Choices = append([]*dgo.ApplicationCommandOptionChoice(nil), option.Choices...)
		}
		cloned[index] = &copyOption
	}
	return cloned
}

func routeKey(path []string) string {
	return strings.Join(path, "\x00")
}

func resolveCommandRoute(options []*dgo.ApplicationCommandInteractionDataOption) ([]string, []*dgo.ApplicationCommandInteractionDataOption, error) {
	if len(options) == 0 || options[0] == nil {
		return nil, options, nil
	}
	first := options[0]
	switch first.Type {
	case dgo.ApplicationCommandOptionSubCommand:
		return []string{first.Name}, first.Options, nil
	case dgo.ApplicationCommandOptionSubCommandGroup:
		if len(first.Options) != 1 || first.Options[0] == nil || first.Options[0].Type != dgo.ApplicationCommandOptionSubCommand {
			return []string{first.Name}, nil, fmt.Errorf("%w: malformed group %q", ErrCommandRouteNotFound, first.Name)
		}
		second := first.Options[0]
		return []string{first.Name, second.Name}, second.Options, nil
	default:
		return nil, options, nil
	}
}
