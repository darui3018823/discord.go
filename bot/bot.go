// Package bot provides an opinionated high-level framework on top of the
// low-level Discord session API.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	dgo "github.com/darui3018823/discord.go"
)

var (
	// ErrDuplicateCommand is returned when a command with the same type and
	// name has already been registered.
	ErrDuplicateCommand = errors.New("command is already registered")
	// ErrInvalidCommand is returned when a command has no definition, name, or
	// handler.
	ErrInvalidCommand = errors.New("invalid command")
)

// Handler handles one application-command interaction.
type Handler func(*Context) error

// Middleware wraps a command handler. Middleware is applied in registration
// order, so the first middleware registered is the outermost wrapper.
type Middleware func(Handler) Handler

// ErrorHandler receives errors returned by command handlers.
type ErrorHandler func(*Context, error)

// Command combines the Discord command definition with its local handler.
type Command struct {
	Definition *dgo.ApplicationCommand
	Handler    Handler
}

// Slash creates a chat-input command.
func Slash(name, description string, handler Handler) *Command {
	return &Command{
		Definition: &dgo.ApplicationCommand{
			Type:        dgo.ChatApplicationCommand,
			Name:        name,
			Description: description,
		},
		Handler: handler,
	}
}

type commandKey struct {
	typeID dgo.ApplicationCommandType
	name   string
}

func keyFor(definition *dgo.ApplicationCommand) commandKey {
	typeID := definition.Type
	if typeID == 0 {
		typeID = dgo.ChatApplicationCommand
	}
	return commandKey{typeID: typeID, name: definition.Name}
}

// Bot routes application-command interactions for a Session.
type Bot struct {
	session *dgo.Session

	mu          sync.RWMutex
	commands    map[commandKey]*Command
	middleware  []Middleware
	errorHandle ErrorHandler
	removeEvent func()
}

// New creates a Bot around an existing low-level Session.
func New(session *dgo.Session) (*Bot, error) {
	if session == nil {
		return nil, errors.New("session must not be nil")
	}

	b := &Bot{
		session:  session,
		commands: make(map[commandKey]*Command),
	}
	b.errorHandle = func(ctx *Context, err error) {
		slog.Default().Error("discord command failed",
			"command", ctx.Data.Name,
			"error", err,
		)
	}
	b.removeEvent = session.AddHandler(func(_ *dgo.Session, event *dgo.InteractionCreate) {
		b.Dispatch(context.Background(), event)
	})
	return b, nil
}

// NewWithToken creates both a low-level bot Session and a high-level Bot.
func NewWithToken(token string) (*Bot, error) {
	session, err := dgo.NewBot(token)
	if err != nil {
		return nil, err
	}
	return New(session)
}

// Session returns the underlying low-level Session.
func (b *Bot) Session() *dgo.Session {
	return b.session
}

// Use registers global command middleware.
func (b *Bot) Use(middleware ...Middleware) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, item := range middleware {
		if item != nil {
			b.middleware = append(b.middleware, item)
		}
	}
}

// SetErrorHandler replaces the handler used for command errors. Passing nil
// restores the default structured logger.
func (b *Bot) SetErrorHandler(handler ErrorHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if handler == nil {
		b.errorHandle = func(ctx *Context, err error) {
			slog.Default().Error("discord command failed",
				"command", ctx.Data.Name,
				"error", err,
			)
		}
		return
	}
	b.errorHandle = handler
}

// Register adds commands to the local router.
func (b *Bot) Register(commands ...*Command) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	seen := make(map[commandKey]struct{}, len(commands))
	for _, command := range commands {
		if command == nil || command.Definition == nil || command.Definition.Name == "" || command.Handler == nil {
			return ErrInvalidCommand
		}
		key := keyFor(command.Definition)
		if _, ok := b.commands[key]; ok {
			return fmt.Errorf("%w: %s", ErrDuplicateCommand, command.Definition.Name)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: %s", ErrDuplicateCommand, command.Definition.Name)
		}
		seen[key] = struct{}{}
	}

	for _, command := range commands {
		if command.Definition.Type == 0 {
			command.Definition.Type = dgo.ChatApplicationCommand
		}
		b.commands[keyFor(command.Definition)] = command
	}
	return nil
}

// CommandDefinitions returns registered Discord command definitions in a
// deterministic order suitable for bulk synchronization.
func (b *Bot) CommandDefinitions() []*dgo.ApplicationCommand {
	b.mu.RLock()
	definitions := make([]*dgo.ApplicationCommand, 0, len(b.commands))
	for _, command := range b.commands {
		definition := *command.Definition
		definitions = append(definitions, &definition)
	}
	b.mu.RUnlock()

	sort.Slice(definitions, func(i, j int) bool {
		left := keyFor(definitions[i])
		right := keyFor(definitions[j])
		if left.typeID != right.typeID {
			return left.typeID < right.typeID
		}
		return left.name < right.name
	})
	return definitions
}

// SyncCommands replaces the application's global or guild commands with the
// locally registered definitions. An empty guildID synchronizes globally.
func (b *Bot) SyncCommands(ctx context.Context, applicationID, guildID string) ([]*dgo.ApplicationCommand, error) {
	if applicationID == "" {
		return nil, errors.New("application ID must not be empty")
	}
	if ctx == nil {
		return nil, errors.New("context must not be nil")
	}
	return b.session.ApplicationCommandBulkOverwrite(
		applicationID,
		guildID,
		b.CommandDefinitions(),
		dgo.WithContext(ctx),
	)
}

// Dispatch routes an interaction to a registered command. It returns true
// when a matching command was found. Handler errors are sent to ErrorHandler.
func (b *Bot) Dispatch(ctx context.Context, event *dgo.InteractionCreate) bool {
	if event == nil || event.Interaction == nil || event.Type != dgo.InteractionApplicationCommand {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	data := event.ApplicationCommandData()
	typeID := data.CommandType
	if typeID == 0 {
		typeID = dgo.ChatApplicationCommand
	}

	b.mu.RLock()
	command := b.commands[commandKey{typeID: typeID, name: data.Name}]
	middleware := append([]Middleware(nil), b.middleware...)
	errorHandler := b.errorHandle
	b.mu.RUnlock()
	if command == nil {
		return false
	}

	commandContext := &Context{
		Context:     ctx,
		Bot:         b,
		Session:     b.session,
		Interaction: event.Interaction,
		Data:        data,
	}
	handler := command.Handler
	for index := len(middleware) - 1; index >= 0; index-- {
		handler = middleware[index](handler)
	}
	if err := handler(commandContext); err != nil && errorHandler != nil {
		errorHandler(commandContext, err)
	}
	return true
}

// Run opens the Gateway, waits for cancellation, and closes the Session.
func (b *Bot) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context must not be nil")
	}
	if err := b.session.OpenWithContext(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return b.session.Close()
}

// Close detaches the router and closes the underlying Session.
func (b *Bot) Close() error {
	b.mu.Lock()
	remove := b.removeEvent
	b.removeEvent = nil
	b.mu.Unlock()
	if remove != nil {
		remove()
	}
	return b.session.Close()
}
