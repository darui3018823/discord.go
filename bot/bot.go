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
	// ErrCommandRouteNotFound is returned when Discord sends an unknown or
	// malformed subcommand path for a registered top-level command.
	ErrCommandRouteNotFound = errors.New("command route not found")
)

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

	mu                     sync.RWMutex
	commands               map[commandKey]*Command
	checks                 []Check
	middleware             []Middleware
	errorHandle            ErrorHandler
	interactionErrorHandle InteractionErrorHandler
	components             customRouter[ComponentHandler]
	modals                 customRouter[ModalHandler]
	removeEvent            func()
	prefixes               []string
	prefixCommands         map[string]*PrefixCommand
	prefixChecks           []PrefixCheck
	prefixMiddleware       []PrefixMiddleware
	prefixErrorHandle      PrefixErrorHandler
	removeMessageEvent     func()
	extensions             map[string]*loadedExtension
	closed                 bool
}

// AddChecks registers global checks that run before every command.
func (b *Bot) AddChecks(checks ...Check) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, check := range checks {
		if check != nil {
			b.checks = append(b.checks, check)
		}
	}
}

// New creates a Bot around an existing low-level Session.
func New(session *dgo.Session) (*Bot, error) {
	if session == nil {
		return nil, errors.New("session must not be nil")
	}

	b := &Bot{
		session:        session,
		commands:       make(map[commandKey]*Command),
		components:     newCustomRouter[ComponentHandler](),
		modals:         newCustomRouter[ModalHandler](),
		prefixCommands: make(map[string]*PrefixCommand),
		extensions:     make(map[string]*loadedExtension),
	}
	b.errorHandle = func(ctx *Context, err error) {
		slog.Default().Error("discord command failed",
			"command", ctx.Data.Name,
			"error", err,
		)
	}
	b.interactionErrorHandle = func(ctx *InteractionContext, err error) {
		slog.Default().Error("discord interaction failed",
			"type", ctx.Interaction.Type,
			"custom_id", ctx.CustomID,
			"error", err,
		)
	}
	b.prefixErrorHandle = defaultPrefixErrorHandler
	b.removeEvent = session.AddHandler(func(_ *dgo.Session, event *dgo.InteractionCreate) {
		b.DispatchInteraction(context.Background(), event)
	})
	b.removeMessageEvent = session.AddHandler(func(_ *dgo.Session, event *dgo.MessageCreate) {
		b.DispatchMessage(context.Background(), event)
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
		if command == nil || command.Definition == nil || command.Definition.Name == "" || !command.hasHandler() {
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
		b.commands[keyFor(command.Definition)] = command.clone()
	}
	return nil
}

// CommandDefinitions returns registered Discord command definitions in a
// deterministic order suitable for bulk synchronization.
func (b *Bot) CommandDefinitions() []*dgo.ApplicationCommand {
	b.mu.RLock()
	definitions := make([]*dgo.ApplicationCommand, 0, len(b.commands))
	for _, command := range b.commands {
		definitions = append(definitions, command.clone().Definition)
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
	checks := append([]Check(nil), b.checks...)
	middleware := append([]Middleware(nil), b.middleware...)
	errorHandler := b.errorHandle
	b.mu.RUnlock()
	if command == nil {
		return false
	}

	path, options, routeErr := resolveCommandRoute(data.Options)
	handler := command.Handler
	checks = append(checks, command.Checks...)
	middleware = append(middleware, command.Middleware...)
	if command.OnError != nil {
		errorHandler = command.OnError
	}
	if len(path) > 0 {
		route := command.routes[routeKey(path)]
		if route != nil {
			handler = route.handler
			checks = append(checks, route.checks...)
			middleware = append(middleware, route.middleware...)
			if route.errorHandler != nil {
				errorHandler = route.errorHandler
			}
		} else {
			handler = nil
		}
	}

	commandContext := &Context{
		Context:     ctx,
		Bot:         b,
		Session:     b.session,
		Interaction: event.Interaction,
		Data:        data,
		CommandPath: append([]string(nil), path...),
		Options:     options,
	}
	if routeErr != nil || handler == nil {
		if errorHandler != nil {
			if routeErr == nil {
				routeErr = fmt.Errorf("%w: %s", ErrCommandRouteNotFound, data.Name)
			}
			reportCommandError(errorHandler, commandContext, routeErr)
		}
		return true
	}
	for index, check := range checks {
		if check == nil {
			continue
		}
		if err := invokeCheck(check, commandContext); err != nil {
			if errorHandler != nil {
				reportCommandError(errorHandler, commandContext, &CheckError{Index: index, Err: err})
			}
			return true
		}
	}
	for index := len(middleware) - 1; index >= 0; index-- {
		if middleware[index] != nil {
			var middlewareErr error
			handler, middlewareErr = wrapHandler(middleware[index], handler)
			if middlewareErr != nil || handler == nil {
				if errorHandler != nil {
					if middlewareErr == nil {
						middlewareErr = errors.New("command middleware returned a nil handler")
					}
					reportCommandError(errorHandler, commandContext, middlewareErr)
				}
				return true
			}
		}
	}
	if err := invokeHandler(handler, commandContext); err != nil && errorHandler != nil {
		reportCommandError(errorHandler, commandContext, err)
	}
	return true
}

// PanicError wraps a recovered command-handler panic.
type PanicError struct {
	Value any
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("command handler panicked: %v", e.Value)
}

func invokeHandler(handler Handler, ctx *Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{Value: recovered}
		}
	}()
	return handler(ctx)
}

func invokeCheck(check Check, ctx *Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{Value: recovered}
		}
	}()
	return check(ctx)
}

func wrapHandler(middleware Middleware, next Handler) (handler Handler, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{Value: recovered}
		}
	}()
	return middleware(next), nil
}

func reportCommandError(handler ErrorHandler, ctx *Context, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Default().Error("discord command error handler panicked",
				"command", ctx.Data.Name,
				"panic", recovered,
			)
		}
	}()
	handler(ctx, err)
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
	return b.Close()
}

// Close detaches the router and closes the underlying Session.
func (b *Bot) Close() error {
	b.mu.Lock()
	b.closed = true
	extensionNames := make([]string, 0, len(b.extensions))
	for name := range b.extensions {
		extensionNames = append(extensionNames, name)
	}
	remove := b.removeEvent
	b.removeEvent = nil
	removeMessage := b.removeMessageEvent
	b.removeMessageEvent = nil
	b.mu.Unlock()
	sort.Strings(extensionNames)
	var closeErrors []error
	for _, name := range extensionNames {
		if err := b.UnloadExtension(context.Background(), name); err != nil && !errors.Is(err, ErrExtensionNotLoaded) {
			closeErrors = append(closeErrors, err)
		}
	}
	if remove != nil {
		remove()
	}
	if removeMessage != nil {
		removeMessage()
	}
	if err := b.session.Close(); err != nil {
		closeErrors = append(closeErrors, err)
	}
	return errors.Join(closeErrors...)
}
