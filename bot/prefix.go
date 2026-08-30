package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode"

	dgo "github.com/darui3018823/discord.go"
)

var (
	// ErrInvalidPrefixCommand is returned for malformed command trees.
	ErrInvalidPrefixCommand = errors.New("invalid prefix command")
	// ErrDuplicatePrefixCommand is returned for colliding names or aliases.
	ErrDuplicatePrefixCommand = errors.New("prefix command is already registered")
	// ErrPrefixParse is returned for malformed quoted command input.
	ErrPrefixParse = errors.New("could not parse prefix command")
	// ErrPrefixSubcommandRequired is returned when a command group has no
	// handler and no matching child was invoked.
	ErrPrefixSubcommandRequired = errors.New("prefix subcommand is required")
)

// PrefixHandler handles one parsed message command.
type PrefixHandler func(*PrefixContext) error

// PrefixCheck authorizes one parsed message command.
type PrefixCheck func(*PrefixContext) error

// PrefixMiddleware wraps a message-command handler.
type PrefixMiddleware func(PrefixHandler) PrefixHandler

// PrefixErrorHandler receives parser, check, middleware, and handler errors.
type PrefixErrorHandler func(*PrefixContext, error)

// PrefixCommand defines a message command, its aliases, and nested commands.
type PrefixCommand struct {
	Name        string
	Aliases     []string
	Description string
	Handler     PrefixHandler
	Checks      []PrefixCheck
	Middleware  []PrefixMiddleware
	OnError     PrefixErrorHandler
	Subcommands []*PrefixCommand

	children map[string]*PrefixCommand
}

// TextCommand creates a message command. A nil handler is allowed when the
// command contains subcommands.
func TextCommand(name string, handler PrefixHandler) *PrefixCommand {
	return &PrefixCommand{Name: name, Handler: handler}
}

// AddAliases adds alternative invocation names.
func (c *PrefixCommand) AddAliases(aliases ...string) {
	c.Aliases = append(c.Aliases, aliases...)
}

// AddChecks adds command-local prefix checks.
func (c *PrefixCommand) AddPrefixChecks(checks ...PrefixCheck) {
	for _, check := range checks {
		if check != nil {
			c.Checks = append(c.Checks, check)
		}
	}
}

// UsePrefix adds command-local prefix middleware.
func (c *PrefixCommand) UsePrefix(middleware ...PrefixMiddleware) {
	for _, item := range middleware {
		if item != nil {
			c.Middleware = append(c.Middleware, item)
		}
	}
}

// SetPrefixErrorHandler sets the closest error handler for this command tree
// node.
func (c *PrefixCommand) SetPrefixErrorHandler(handler PrefixErrorHandler) {
	c.OnError = handler
}

// AddSubcommands atomically adds nested prefix commands.
func (c *PrefixCommand) AddSubcommands(commands ...*PrefixCommand) error {
	if c == nil {
		return ErrInvalidPrefixCommand
	}
	existing := make(map[string]struct{})
	for _, child := range c.Subcommands {
		for _, name := range prefixCommandNames(child) {
			existing[name] = struct{}{}
		}
	}
	batch := make(map[string]struct{})
	for _, command := range commands {
		if err := validatePrefixCommand(command, make(map[*PrefixCommand]bool)); err != nil {
			return err
		}
		for _, name := range prefixCommandNames(command) {
			if _, found := existing[name]; found {
				return fmt.Errorf("%w: %s", ErrDuplicatePrefixCommand, name)
			}
			if _, found := batch[name]; found {
				return fmt.Errorf("%w: %s", ErrDuplicatePrefixCommand, name)
			}
			batch[name] = struct{}{}
		}
	}
	c.Subcommands = append(c.Subcommands, commands...)
	return nil
}

// PrefixContext contains one parsed message-command invocation.
type PrefixContext struct {
	context.Context
	Bot         *Bot
	Session     *dgo.Session
	Message     *dgo.MessageCreate
	Prefix      string
	InvokedWith string
	CommandPath []string
	Args        []string
	Raw         string
}

// User returns the message author.
func (c *PrefixContext) User() *dgo.User {
	if c.Message == nil || c.Message.Message == nil {
		return nil
	}
	return c.Message.Author
}

// Reply sends a message to the invocation channel with safe mention defaults.
func (c *PrefixContext) Reply(content string) (*dgo.Message, error) {
	if c.Session == nil || c.Message == nil || c.Message.Message == nil {
		return nil, errors.New("prefix context has no active message")
	}
	options := []dgo.RequestOption(nil)
	if c.Context != nil {
		options = append(options, dgo.WithContext(c.Context))
	}
	return c.Session.ChannelMessageSendComplex(c.Message.ChannelID, &dgo.MessageSend{
		Content:         content,
		AllowedMentions: &dgo.MessageAllowedMentions{},
	}, options...)
}

// SetPrefixes replaces recognized message prefixes. Prefix matching prefers
// the longest value. Passing no values disables prefix commands.
func (b *Bot) SetPrefixes(prefixes ...string) error {
	seen := make(map[string]struct{}, len(prefixes))
	cloned := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix == "" {
			return errors.New("message prefix must not be empty")
		}
		if _, exists := seen[prefix]; exists {
			continue
		}
		seen[prefix] = struct{}{}
		cloned = append(cloned, prefix)
	}
	sort.Slice(cloned, func(i, j int) bool {
		if len(cloned[i]) != len(cloned[j]) {
			return len(cloned[i]) > len(cloned[j])
		}
		return cloned[i] < cloned[j]
	})
	b.mu.Lock()
	b.prefixes = cloned
	b.mu.Unlock()
	return nil
}

// AddPrefixChecks registers checks that run for every prefix command.
func (b *Bot) AddPrefixChecks(checks ...PrefixCheck) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, check := range checks {
		if check != nil {
			b.prefixChecks = append(b.prefixChecks, check)
		}
	}
}

// UsePrefix registers middleware that wraps every prefix command.
func (b *Bot) UsePrefix(middleware ...PrefixMiddleware) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, item := range middleware {
		if item != nil {
			b.prefixMiddleware = append(b.prefixMiddleware, item)
		}
	}
}

// SetPrefixErrorHandler replaces the global prefix-command error handler.
func (b *Bot) SetPrefixErrorHandler(handler PrefixErrorHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if handler == nil {
		b.prefixErrorHandle = defaultPrefixErrorHandler
		return
	}
	b.prefixErrorHandle = handler
}

func defaultPrefixErrorHandler(ctx *PrefixContext, err error) {
	command := strings.Join(ctx.CommandPath, " ")
	slog.Default().Error("discord prefix command failed", "command", command, "error", err)
}

// RegisterPrefixCommands atomically registers top-level prefix commands and
// all aliases.
func (b *Bot) RegisterPrefixCommands(commands ...*PrefixCommand) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := make(map[string]struct{})
	for _, command := range commands {
		if err := validatePrefixCommand(command, make(map[*PrefixCommand]bool)); err != nil {
			return err
		}
		for _, name := range prefixCommandNames(command) {
			if _, exists := b.prefixCommands[name]; exists {
				return fmt.Errorf("%w: %s", ErrDuplicatePrefixCommand, name)
			}
			if _, exists := batch[name]; exists {
				return fmt.Errorf("%w: %s", ErrDuplicatePrefixCommand, name)
			}
			batch[name] = struct{}{}
		}
	}
	for _, command := range commands {
		cloned := clonePrefixCommand(command)
		for _, name := range prefixCommandNames(cloned) {
			b.prefixCommands[name] = cloned
		}
	}
	return nil
}

// PrefixCommands returns canonical top-level commands in deterministic order.
func (b *Bot) PrefixCommands() []*PrefixCommand {
	b.mu.RLock()
	seen := make(map[*PrefixCommand]struct{})
	commands := make([]*PrefixCommand, 0)
	for _, command := range b.prefixCommands {
		if _, exists := seen[command]; exists {
			continue
		}
		seen[command] = struct{}{}
		commands = append(commands, clonePrefixCommand(command))
	}
	b.mu.RUnlock()
	sort.Slice(commands, func(i, j int) bool {
		return normalizePrefixName(commands[i].Name) < normalizePrefixName(commands[j].Name)
	})
	return commands
}

// DispatchMessage parses and dispatches a MessageCreate event. Bot-authored
// messages are ignored to prevent feedback loops.
func (b *Bot) DispatchMessage(ctx context.Context, event *dgo.MessageCreate) bool {
	if event == nil || event.Message == nil || event.Author == nil || event.Author.Bot {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.RLock()
	prefixes := append([]string(nil), b.prefixes...)
	checks := append([]PrefixCheck(nil), b.prefixChecks...)
	middleware := append([]PrefixMiddleware(nil), b.prefixMiddleware...)
	errorHandler := b.prefixErrorHandle
	commands := make(map[string]*PrefixCommand, len(b.prefixCommands))
	for name, command := range b.prefixCommands {
		commands[name] = command
	}
	b.mu.RUnlock()
	prefix, input, matched := matchMessagePrefix(event.Content, prefixes)
	if !matched {
		return false
	}
	args, parseErr := parsePrefixArguments(input)
	baseContext := &PrefixContext{
		Context: ctx, Bot: b, Session: b.session, Message: event,
		Prefix: prefix, Raw: input,
	}
	if parseErr != nil {
		reportPrefixError(errorHandler, baseContext, parseErr)
		return true
	}
	if len(args) == 0 {
		return false
	}
	command := commands[normalizePrefixName(args[0])]
	if command == nil {
		return false
	}
	baseContext.InvokedWith = args[0]
	args = args[1:]
	path := []*PrefixCommand{command}
	for len(args) > 0 {
		child := command.children[normalizePrefixName(args[0])]
		if child == nil {
			break
		}
		command = child
		path = append(path, child)
		args = args[1:]
	}
	baseContext.Args = append([]string(nil), args...)
	for _, node := range path {
		baseContext.CommandPath = append(baseContext.CommandPath, node.Name)
		checks = append(checks, node.Checks...)
		middleware = append(middleware, node.Middleware...)
		if node.OnError != nil {
			errorHandler = node.OnError
		}
	}
	if command.Handler == nil {
		reportPrefixError(errorHandler, baseContext, ErrPrefixSubcommandRequired)
		return true
	}
	for index, check := range checks {
		if check == nil {
			continue
		}
		if err := invokePrefixCheck(check, baseContext); err != nil {
			reportPrefixError(errorHandler, baseContext, &CheckError{Index: index, Err: err})
			return true
		}
	}
	handler := command.Handler
	for index := len(middleware) - 1; index >= 0; index-- {
		if middleware[index] == nil {
			continue
		}
		var err error
		handler, err = wrapPrefixHandler(middleware[index], handler)
		if err != nil || handler == nil {
			if err == nil {
				err = errors.New("prefix middleware returned a nil handler")
			}
			reportPrefixError(errorHandler, baseContext, err)
			return true
		}
	}
	if err := invokePrefixHandler(handler, baseContext); err != nil {
		reportPrefixError(errorHandler, baseContext, err)
	}
	return true
}

func matchMessagePrefix(content string, prefixes []string) (prefix, input string, ok bool) {
	for _, candidate := range prefixes {
		if strings.HasPrefix(content, candidate) {
			return candidate, strings.TrimSpace(strings.TrimPrefix(content, candidate)), true
		}
	}
	return "", "", false
}

func validatePrefixCommand(command *PrefixCommand, visiting map[*PrefixCommand]bool) error {
	if command == nil || normalizePrefixName(command.Name) == "" || strings.IndexFunc(command.Name, unicode.IsSpace) >= 0 {
		return ErrInvalidPrefixCommand
	}
	if visiting[command] {
		return fmt.Errorf("%w: cyclic command tree at %s", ErrInvalidPrefixCommand, command.Name)
	}
	visiting[command] = true
	defer delete(visiting, command)
	names := make(map[string]struct{})
	for _, name := range prefixCommandNames(command) {
		if name == "" || strings.IndexFunc(name, unicode.IsSpace) >= 0 {
			return fmt.Errorf("%w: invalid name or alias", ErrInvalidPrefixCommand)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("%w: duplicate alias %s", ErrDuplicatePrefixCommand, name)
		}
		names[name] = struct{}{}
	}
	if command.Handler == nil && len(command.Subcommands) == 0 {
		return fmt.Errorf("%w: %s has no handler or subcommands", ErrInvalidPrefixCommand, command.Name)
	}
	children := make(map[string]struct{})
	for _, child := range command.Subcommands {
		if err := validatePrefixCommand(child, visiting); err != nil {
			return err
		}
		for _, name := range prefixCommandNames(child) {
			if _, exists := children[name]; exists {
				return fmt.Errorf("%w: child %s", ErrDuplicatePrefixCommand, name)
			}
			children[name] = struct{}{}
		}
	}
	return nil
}

func prefixCommandNames(command *PrefixCommand) []string {
	if command == nil {
		return nil
	}
	names := make([]string, 0, len(command.Aliases)+1)
	names = append(names, normalizePrefixName(command.Name))
	for _, alias := range command.Aliases {
		names = append(names, normalizePrefixName(alias))
	}
	return names
}

func normalizePrefixName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func clonePrefixCommand(command *PrefixCommand) *PrefixCommand {
	if command == nil {
		return nil
	}
	cloned := &PrefixCommand{
		Name: command.Name, Description: command.Description, Handler: command.Handler,
		Aliases:    append([]string(nil), command.Aliases...),
		Checks:     append([]PrefixCheck(nil), command.Checks...),
		Middleware: append([]PrefixMiddleware(nil), command.Middleware...),
		OnError:    command.OnError,
		children:   make(map[string]*PrefixCommand),
	}
	for _, child := range command.Subcommands {
		childClone := clonePrefixCommand(child)
		cloned.Subcommands = append(cloned.Subcommands, childClone)
		for _, name := range prefixCommandNames(childClone) {
			cloned.children[name] = childClone
		}
	}
	return cloned
}

func parsePrefixArguments(input string) ([]string, error) {
	var args []string
	var token strings.Builder
	var quote rune
	escaped := false
	started := false
	flush := func() {
		if started {
			args = append(args, token.String())
			token.Reset()
			started = false
		}
	}
	for _, current := range input {
		if escaped {
			token.WriteRune(current)
			started = true
			escaped = false
			continue
		}
		if current == '\\' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if current == quote {
				quote = 0
			} else {
				token.WriteRune(current)
			}
			started = true
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			started = true
			continue
		}
		if unicode.IsSpace(current) {
			flush()
			continue
		}
		token.WriteRune(current)
		started = true
	}
	if escaped {
		return nil, fmt.Errorf("%w: dangling escape", ErrPrefixParse)
	}
	if quote != 0 {
		return nil, fmt.Errorf("%w: unterminated quote", ErrPrefixParse)
	}
	flush()
	return args, nil
}

func invokePrefixCheck(check PrefixCheck, ctx *PrefixContext) (err error) {
	defer recoverPrefixPanic(&err)
	return check(ctx)
}

func invokePrefixHandler(handler PrefixHandler, ctx *PrefixContext) (err error) {
	defer recoverPrefixPanic(&err)
	return handler(ctx)
}

func wrapPrefixHandler(middleware PrefixMiddleware, next PrefixHandler) (handler PrefixHandler, err error) {
	defer recoverPrefixPanic(&err)
	return middleware(next), nil
}

func recoverPrefixPanic(err *error) {
	if recovered := recover(); recovered != nil {
		*err = &PanicError{Value: recovered}
	}
}

func reportPrefixError(handler PrefixErrorHandler, ctx *PrefixContext, err error) {
	if handler == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Default().Error("discord prefix error handler panicked", "panic", recovered)
		}
	}()
	handler(ctx, err)
}
