package bot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	dgo "github.com/darui3018823/discord.go"
)

var (
	// ErrExtensionLoaded is returned when an extension name is already active.
	ErrExtensionLoaded = errors.New("extension is already loaded")
	// ErrExtensionNotLoaded is returned when unloading an unknown extension.
	ErrExtensionNotLoaded = errors.New("extension is not loaded")
	// ErrExtensionUnloaded is used as the cancellation cause when an extension
	// loop cannot finish before its unload deadline.
	ErrExtensionUnloaded = errors.New("extension was unloaded")
	// ErrInvalidExtension is returned for nil or unnamed extensions.
	ErrInvalidExtension = errors.New("invalid extension")
	// ErrBotClosed is returned when an extension is loaded after Close.
	ErrBotClosed = errors.New("bot is closed")
)

// Extension groups commands, interaction routes, and event listeners under a
// lifecycle-managed name.
type Extension interface {
	Name() string
	Setup(context.Context, *ExtensionRegistrar) error
}

// Cog is the discord.py-style name for an Extension.
type Cog = Extension

// ExtensionTeardown is implemented by extensions that need asynchronous
// cleanup after their registrations have been removed.
type ExtensionTeardown interface {
	Teardown(context.Context) error
}

type extensionFunc struct {
	name     string
	setup    func(context.Context, *ExtensionRegistrar) error
	teardown func(context.Context) error
}

func (e *extensionFunc) Name() string { return e.name }
func (e *extensionFunc) Setup(ctx context.Context, registrar *ExtensionRegistrar) error {
	return e.setup(ctx, registrar)
}
func (e *extensionFunc) Teardown(ctx context.Context) error {
	if e.teardown == nil {
		return nil
	}
	return e.teardown(ctx)
}

// NewExtension creates an Extension from setup and optional teardown
// functions.
func NewExtension(name string, setup func(context.Context, *ExtensionRegistrar) error, teardown ...func(context.Context) error) Extension {
	var cleanup func(context.Context) error
	if len(teardown) > 0 {
		cleanup = teardown[0]
	}
	return &extensionFunc{name: name, setup: setup, teardown: cleanup}
}

type interactionRegistration[H any] struct {
	pattern string
	prefix  bool
	handler H
}

// ExtensionRegistrar collects registrations without publishing them. The Bot
// commits the complete set only after Setup succeeds and every conflict check
// passes.
type ExtensionRegistrar struct {
	bot        *Bot
	commands   []*Command
	prefix     []*PrefixCommand
	components []interactionRegistration[ComponentHandler]
	modals     []interactionRegistration[ModalHandler]
	events     []any
	loops      []*Loop
}

// Bot returns the host Bot for dependency access during Setup.
func (r *ExtensionRegistrar) Bot() *Bot { return r.bot }

// AddCommands stages application commands.
func (r *ExtensionRegistrar) AddCommands(commands ...*Command) error {
	for _, command := range commands {
		if command == nil || command.Definition == nil || command.Definition.Name == "" || !command.hasHandler() {
			return ErrInvalidCommand
		}
	}
	r.commands = append(r.commands, commands...)
	return nil
}

// AddPrefixCommands stages message commands.
func (r *ExtensionRegistrar) AddPrefixCommands(commands ...*PrefixCommand) error {
	for _, command := range commands {
		if err := validatePrefixCommand(command, make(map[*PrefixCommand]bool)); err != nil {
			return err
		}
	}
	r.prefix = append(r.prefix, commands...)
	return nil
}

// AddComponent stages an exact component custom ID.
func (r *ExtensionRegistrar) AddComponent(customID string, handler ComponentHandler) error {
	return r.addComponent(customID, false, handler)
}

// AddComponentPrefix stages a component custom-ID prefix.
func (r *ExtensionRegistrar) AddComponentPrefix(prefix string, handler ComponentHandler) error {
	return r.addComponent(prefix, true, handler)
}

func (r *ExtensionRegistrar) addComponent(pattern string, prefix bool, handler ComponentHandler) error {
	if pattern == "" || handler == nil {
		return ErrInvalidInteractionRoute
	}
	r.components = append(r.components, interactionRegistration[ComponentHandler]{pattern: pattern, prefix: prefix, handler: handler})
	return nil
}

// AddModal stages an exact modal custom ID.
func (r *ExtensionRegistrar) AddModal(customID string, handler ModalHandler) error {
	return r.addModal(customID, false, handler)
}

// AddModalPrefix stages a modal custom-ID prefix.
func (r *ExtensionRegistrar) AddModalPrefix(prefix string, handler ModalHandler) error {
	return r.addModal(prefix, true, handler)
}

func (r *ExtensionRegistrar) addModal(pattern string, prefix bool, handler ModalHandler) error {
	if pattern == "" || handler == nil {
		return ErrInvalidInteractionRoute
	}
	r.modals = append(r.modals, interactionRegistration[ModalHandler]{pattern: pattern, prefix: prefix, handler: handler})
	return nil
}

// AddEventHandler stages a low-level typed Session event handler. The handler
// is detached automatically during unload.
func (r *ExtensionRegistrar) AddEventHandler(handler any) error {
	if isNilInterface(handler) {
		return errors.New("event handler must not be nil")
	}
	r.events = append(r.events, handler)
	return nil
}

// AddLoops stages task loops that start only after every extension
// registration has passed conflict validation.
func (r *ExtensionRegistrar) AddLoops(loops ...*Loop) error {
	for _, loop := range loops {
		if loop == nil {
			return ErrInvalidLoop
		}
	}
	r.loops = append(r.loops, loops...)
	return nil
}

type loadedExtension struct {
	name             string
	extension        Extension
	commands         []commandKey
	prefixCommands   []*PrefixCommand
	componentRoutes  []interactionRegistration[ComponentHandler]
	modalRoutes      []interactionRegistration[ModalHandler]
	removeEventHooks []func()
	loops            []*Loop
}

// LoadExtension runs Setup and atomically publishes every staged registry
// entry. Setup panics are returned as PanicError.
func (b *Bot) LoadExtension(ctx context.Context, extension Extension) error {
	if ctx == nil {
		return errors.New("context must not be nil")
	}
	if isNilInterface(extension) || strings.TrimSpace(extension.Name()) == "" {
		return ErrInvalidExtension
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := normalizeExtensionName(extension.Name())
	b.mu.RLock()
	_, exists := b.extensions[name]
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return ErrBotClosed
	}
	if exists {
		return fmt.Errorf("%w: %s", ErrExtensionLoaded, extension.Name())
	}

	registrar := &ExtensionRegistrar{bot: b}
	if err := invokeExtensionSetup(ctx, extension, registrar); err != nil {
		return abortExtension(ctx, extension, err)
	}
	if err := ctx.Err(); err != nil {
		return abortExtension(ctx, extension, err)
	}
	if err := b.commitExtension(name, extension, registrar); err != nil {
		return abortExtension(ctx, extension, err)
	}
	return nil
}

// LoadCog is an alias for LoadExtension.
func (b *Bot) LoadCog(ctx context.Context, cog Cog) error {
	return b.LoadExtension(ctx, cog)
}

func (b *Bot) commitExtension(name string, extension Extension, registrar *ExtensionRegistrar) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBotClosed
	}
	if _, exists := b.extensions[name]; exists {
		return fmt.Errorf("%w: %s", ErrExtensionLoaded, extension.Name())
	}

	commands := make(map[commandKey]*Command, len(b.commands)+len(registrar.commands))
	for key, command := range b.commands {
		commands[key] = command
	}
	ownedCommandKeys := make([]commandKey, 0, len(registrar.commands))
	for _, command := range registrar.commands {
		key := keyFor(command.Definition)
		if _, exists := commands[key]; exists {
			return fmt.Errorf("%w: %s", ErrDuplicateCommand, command.Definition.Name)
		}
		cloned := command.clone()
		if cloned.Definition.Type == 0 {
			cloned.Definition.Type = dgo.ChatApplicationCommand
		}
		commands[keyFor(cloned.Definition)] = cloned
		ownedCommandKeys = append(ownedCommandKeys, keyFor(cloned.Definition))
	}

	prefixCommands := make(map[string]*PrefixCommand, len(b.prefixCommands))
	for alias, command := range b.prefixCommands {
		prefixCommands[alias] = command
	}
	ownedPrefix := make([]*PrefixCommand, 0, len(registrar.prefix))
	for _, command := range registrar.prefix {
		cloned := clonePrefixCommand(command)
		for _, alias := range prefixCommandNames(cloned) {
			if _, exists := prefixCommands[alias]; exists {
				return fmt.Errorf("%w: %s", ErrDuplicatePrefixCommand, alias)
			}
			prefixCommands[alias] = cloned
		}
		ownedPrefix = append(ownedPrefix, cloned)
	}

	components := b.components.clone()
	for _, route := range registrar.components {
		if err := components.add(route.pattern, route.prefix, route.handler); err != nil {
			return err
		}
	}
	modals := b.modals.clone()
	for _, route := range registrar.modals {
		if err := modals.add(route.pattern, route.prefix, route.handler); err != nil {
			return err
		}
	}
	preparedLoops := make([]*preparedLoopRun, 0, len(registrar.loops))
	for _, loop := range registrar.loops {
		_, managed := b.loops[loop]
		_, reserved := b.reservedLoops[loop]
		if managed || reserved {
			abortPreparedLoopRuns(preparedLoops, ErrLoopManaged)
			return ErrLoopManaged
		}
		prepared, err := loop.prepareStart(b.lifecycleCtx)
		if err != nil {
			abortPreparedLoopRuns(preparedLoops, err)
			return err
		}
		preparedLoops = append(preparedLoops, prepared)
	}

	removeHooks := make([]func(), 0, len(registrar.events))
	for _, handler := range registrar.events {
		removeHooks = append(removeHooks, b.session.AddHandler(handler))
	}
	b.commands = commands
	b.prefixCommands = prefixCommands
	b.components = components
	b.modals = modals
	for _, loop := range registrar.loops {
		b.loops[loop] = struct{}{}
		b.reservedLoops[loop] = struct{}{}
	}
	b.extensions[name] = &loadedExtension{
		name: name, extension: extension, commands: ownedCommandKeys,
		prefixCommands:   ownedPrefix,
		componentRoutes:  append([]interactionRegistration[ComponentHandler](nil), registrar.components...),
		modalRoutes:      append([]interactionRegistration[ModalHandler](nil), registrar.modals...),
		removeEventHooks: removeHooks,
		loops:            append([]*Loop(nil), registrar.loops...),
	}
	for _, prepared := range preparedLoops {
		prepared.launch()
		go b.watchLoop(prepared.loop)
	}
	return nil
}

// UnloadExtension removes every owned registration before running Teardown.
func (b *Bot) UnloadExtension(ctx context.Context, name string) error {
	if ctx == nil {
		return errors.New("context must not be nil")
	}
	normalized := normalizeExtensionName(name)
	b.mu.Lock()
	loaded := b.extensions[normalized]
	if loaded == nil {
		b.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrExtensionNotLoaded, name)
	}
	delete(b.extensions, normalized)
	for _, key := range loaded.commands {
		delete(b.commands, key)
	}
	for _, command := range loaded.prefixCommands {
		for _, alias := range prefixCommandNames(command) {
			if b.prefixCommands[alias] == command {
				delete(b.prefixCommands, alias)
			}
		}
	}
	for _, route := range loaded.componentRoutes {
		b.components.remove(route.pattern, route.prefix)
	}
	for _, route := range loaded.modalRoutes {
		b.modals.remove(route.pattern, route.prefix)
	}
	for _, loop := range loaded.loops {
		delete(b.loops, loop)
		delete(b.reservedLoops, loop)
	}
	b.mu.Unlock()

	var unloadErrors []error
	for _, remove := range loaded.removeEventHooks {
		if remove != nil {
			remove()
		}
	}
	for _, loop := range loaded.loops {
		if err := loop.Stop(ctx); err != nil && !errors.Is(err, ErrLoopNotRunning) {
			unloadErrors = append(unloadErrors, err)
			if loop.IsRunning() {
				_ = loop.Cancel(ErrExtensionUnloaded)
			}
		}
	}
	if err := invokeExtensionTeardown(ctx, loaded.extension); err != nil {
		unloadErrors = append(unloadErrors, err)
	}
	return errors.Join(unloadErrors...)
}

// UnloadCog is an alias for UnloadExtension.
func (b *Bot) UnloadCog(ctx context.Context, name string) error {
	return b.UnloadExtension(ctx, name)
}

// Extensions returns loaded extension names in deterministic order.
func (b *Bot) Extensions() []string {
	b.mu.RLock()
	names := make([]string, 0, len(b.extensions))
	for _, extension := range b.extensions {
		names = append(names, extension.extension.Name())
	}
	b.mu.RUnlock()
	sort.Strings(names)
	return names
}

func normalizeExtensionName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func invokeExtensionSetup(ctx context.Context, extension Extension, registrar *ExtensionRegistrar) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{Value: recovered}
		}
	}()
	return extension.Setup(ctx, registrar)
}

func invokeExtensionTeardown(ctx context.Context, extension Extension) (err error) {
	teardown, ok := extension.(ExtensionTeardown)
	if !ok {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{Value: recovered}
		}
	}()
	return teardown.Teardown(ctx)
}

func abortExtension(ctx context.Context, extension Extension, cause error) error {
	if cleanupErr := invokeExtensionTeardown(ctx, extension); cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	return cause
}

func abortPreparedLoopRuns(runs []*preparedLoopRun, cause error) {
	for _, run := range runs {
		run.abort(cause)
	}
}
