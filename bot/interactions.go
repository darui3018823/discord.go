package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	dgo "github.com/darui3018823/discord.go"
)

var (
	// ErrDuplicateInteractionRoute is returned when the same exact or prefix
	// route is registered twice for one interaction kind.
	ErrDuplicateInteractionRoute = errors.New("interaction route is already registered")
	// ErrInvalidInteractionRoute is returned for an empty pattern or nil
	// handler.
	ErrInvalidInteractionRoute = errors.New("invalid interaction route")
)

// InteractionContext contains state shared by component and modal handlers.
type InteractionContext struct {
	context.Context
	Bot         *Bot
	Session     *dgo.Session
	Interaction *dgo.Interaction
	CustomID    string
	Pattern     string
	// Suffix is the portion of CustomID after a matched prefix. It is empty
	// for exact routes.
	Suffix string
}

// InteractionErrorHandler handles component and modal handler errors.
type InteractionErrorHandler func(*InteractionContext, error)

// ComponentContext contains a button or select-menu interaction.
type ComponentContext struct {
	*InteractionContext
	Data dgo.MessageComponentInteractionData
}

// ModalContext contains a modal-submit interaction.
type ModalContext struct {
	*InteractionContext
	Data dgo.ModalSubmitInteractionData
}

// ComponentHandler handles a button or select-menu interaction.
type ComponentHandler func(*ComponentContext) error

// ModalHandler handles a modal-submit interaction.
type ModalHandler func(*ModalContext) error

type customRoute[H any] struct {
	pattern string
	handler H
}

type customRouter[H any] struct {
	exact    map[string]H
	prefixes []customRoute[H]
}

func newCustomRouter[H any]() customRouter[H] {
	return customRouter[H]{exact: make(map[string]H)}
}

func (r *customRouter[H]) add(pattern string, prefix bool, handler H) error {
	if pattern == "" {
		return ErrInvalidInteractionRoute
	}
	if r.exact == nil {
		r.exact = make(map[string]H)
	}
	if prefix {
		for _, route := range r.prefixes {
			if route.pattern == pattern {
				return fmt.Errorf("%w: prefix %q", ErrDuplicateInteractionRoute, pattern)
			}
		}
		r.prefixes = append(r.prefixes, customRoute[H]{pattern: pattern, handler: handler})
		sort.SliceStable(r.prefixes, func(i, j int) bool {
			if len(r.prefixes[i].pattern) != len(r.prefixes[j].pattern) {
				return len(r.prefixes[i].pattern) > len(r.prefixes[j].pattern)
			}
			return r.prefixes[i].pattern < r.prefixes[j].pattern
		})
		return nil
	}
	if _, exists := r.exact[pattern]; exists {
		return fmt.Errorf("%w: exact %q", ErrDuplicateInteractionRoute, pattern)
	}
	r.exact[pattern] = handler
	return nil
}

func (r *customRouter[H]) match(customID string) (handler H, pattern, suffix string, ok bool) {
	if exact, exists := r.exact[customID]; exists {
		return exact, customID, "", true
	}
	for _, route := range r.prefixes {
		if strings.HasPrefix(customID, route.pattern) {
			return route.handler, route.pattern, strings.TrimPrefix(customID, route.pattern), true
		}
	}
	return handler, "", "", false
}

// SetInteractionErrorHandler replaces the component/modal error handler.
// Passing nil restores structured logging.
func (b *Bot) SetInteractionErrorHandler(handler InteractionErrorHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if handler == nil {
		b.interactionErrorHandle = func(ctx *InteractionContext, err error) {
			slog.Default().Error("discord interaction failed",
				"type", ctx.Interaction.Type,
				"custom_id", ctx.CustomID,
				"error", err,
			)
		}
		return
	}
	b.interactionErrorHandle = handler
}

// RegisterComponent registers an exact component custom ID.
func (b *Bot) RegisterComponent(customID string, handler ComponentHandler) error {
	if handler == nil {
		return ErrInvalidInteractionRoute
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.components.add(customID, false, handler)
}

// RegisterComponentPrefix registers a dynamic component custom-ID prefix.
// Exact routes win first; otherwise the longest matching prefix wins.
func (b *Bot) RegisterComponentPrefix(prefix string, handler ComponentHandler) error {
	if handler == nil {
		return ErrInvalidInteractionRoute
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.components.add(prefix, true, handler)
}

// RegisterModal registers an exact modal custom ID.
func (b *Bot) RegisterModal(customID string, handler ModalHandler) error {
	if handler == nil {
		return ErrInvalidInteractionRoute
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modals.add(customID, false, handler)
}

// RegisterModalPrefix registers a dynamic modal custom-ID prefix.
func (b *Bot) RegisterModalPrefix(prefix string, handler ModalHandler) error {
	if handler == nil {
		return ErrInvalidInteractionRoute
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modals.add(prefix, true, handler)
}

// DispatchInteraction routes every supported interaction kind. It returns
// false when no route matches.
func (b *Bot) DispatchInteraction(ctx context.Context, event *dgo.InteractionCreate) bool {
	if event == nil || event.Interaction == nil {
		return false
	}
	switch event.Type {
	case dgo.InteractionApplicationCommand:
		return b.Dispatch(ctx, event)
	case dgo.InteractionApplicationCommandAutocomplete:
		return b.dispatchAutocomplete(ctx, event)
	case dgo.InteractionMessageComponent:
		return b.dispatchComponent(ctx, event)
	case dgo.InteractionModalSubmit:
		return b.dispatchModal(ctx, event)
	default:
		return false
	}
}

func (b *Bot) dispatchComponent(ctx context.Context, event *dgo.InteractionCreate) bool {
	data := event.MessageComponentData()
	b.mu.RLock()
	handler, pattern, suffix, ok := b.components.match(data.CustomID)
	errorHandler := b.interactionErrorHandle
	b.mu.RUnlock()
	if !ok {
		return false
	}
	base := b.newInteractionContext(ctx, event.Interaction, data.CustomID, pattern, suffix)
	componentContext := &ComponentContext{InteractionContext: base, Data: data}
	if err := invokeComponentHandler(handler, componentContext); err != nil && errorHandler != nil {
		reportInteractionError(errorHandler, base, err)
	}
	return true
}

func (b *Bot) dispatchModal(ctx context.Context, event *dgo.InteractionCreate) bool {
	data := event.ModalSubmitData()
	b.mu.RLock()
	handler, pattern, suffix, ok := b.modals.match(data.CustomID)
	errorHandler := b.interactionErrorHandle
	b.mu.RUnlock()
	if !ok {
		return false
	}
	base := b.newInteractionContext(ctx, event.Interaction, data.CustomID, pattern, suffix)
	modalContext := &ModalContext{InteractionContext: base, Data: data}
	if err := invokeModalHandler(handler, modalContext); err != nil && errorHandler != nil {
		reportInteractionError(errorHandler, base, err)
	}
	return true
}

func (b *Bot) newInteractionContext(ctx context.Context, interaction *dgo.Interaction, customID, pattern, suffix string) *InteractionContext {
	if ctx == nil {
		ctx = context.Background()
	}
	return &InteractionContext{
		Context:     ctx,
		Bot:         b,
		Session:     b.session,
		Interaction: interaction,
		CustomID:    customID,
		Pattern:     pattern,
		Suffix:      suffix,
	}
}

func invokeComponentHandler(handler ComponentHandler, ctx *ComponentContext) (err error) {
	defer recoverInteractionPanic(&err)
	return handler(ctx)
}

func invokeModalHandler(handler ModalHandler, ctx *ModalContext) (err error) {
	defer recoverInteractionPanic(&err)
	return handler(ctx)
}

func recoverInteractionPanic(err *error) {
	if recovered := recover(); recovered != nil {
		*err = &PanicError{Value: recovered}
	}
}

func reportInteractionError(handler InteractionErrorHandler, ctx *InteractionContext, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Default().Error("discord interaction error handler panicked",
				"custom_id", ctx.CustomID,
				"panic", recovered,
			)
		}
	}()
	handler(ctx, err)
}

// Respond sends a complete interaction response.
func (c *InteractionContext) Respond(response *dgo.InteractionResponse) error {
	if c.Session == nil || c.Interaction == nil {
		return errors.New("interaction context has no active interaction")
	}
	options := []dgo.RequestOption(nil)
	if c.Context != nil {
		options = append(options, dgo.WithContext(c.Context))
	}
	return c.Session.InteractionRespond(c.Interaction, response, options...)
}

// Reply sends a safe text response.
func (c *InteractionContext) Reply(content string) error {
	return c.Respond(&dgo.InteractionResponse{
		Type: dgo.InteractionResponseChannelMessageWithSource,
		Data: &dgo.InteractionResponseData{Content: content, AllowedMentions: &dgo.MessageAllowedMentions{}},
	})
}

// ReplyEphemeral sends a safe text response visible only to the caller.
func (c *InteractionContext) ReplyEphemeral(content string) error {
	return c.Respond(&dgo.InteractionResponse{
		Type: dgo.InteractionResponseChannelMessageWithSource,
		Data: &dgo.InteractionResponseData{
			Content: content, Flags: dgo.MessageFlagsEphemeral,
			AllowedMentions: &dgo.MessageAllowedMentions{},
		},
	})
}

// Update replaces the message that contains the invoked component.
func (c *ComponentContext) Update(data *dgo.InteractionResponseData) error {
	return c.Respond(&dgo.InteractionResponse{Type: dgo.InteractionResponseUpdateMessage, Data: data})
}

// DeferUpdate acknowledges a component without immediately changing it.
func (c *ComponentContext) DeferUpdate() error {
	return c.Respond(&dgo.InteractionResponse{Type: dgo.InteractionResponseDeferredMessageUpdate})
}

// ShowModal responds to a component with a modal.
func (c *ComponentContext) ShowModal(customID, title string, components ...dgo.MessageComponent) error {
	return c.Respond(&dgo.InteractionResponse{
		Type: dgo.InteractionResponseModal,
		Data: &dgo.InteractionResponseData{CustomID: customID, Title: title, Components: components},
	})
}

// Text returns a submitted text-input value by custom ID.
func (c *ModalContext) Text(customID string) (string, error) {
	for _, component := range c.Data.Components {
		if value, ok := findModalText(component, customID); ok {
			return value, nil
		}
	}
	return "", fmt.Errorf("%w: modal field %s", ErrOptionNotFound, customID)
}

func findModalText(component dgo.MessageComponent, customID string) (string, bool) {
	switch value := component.(type) {
	case *dgo.TextInput:
		return value.Value, value.CustomID == customID
	case dgo.TextInput:
		return value.Value, value.CustomID == customID
	case *dgo.ActionsRow:
		for _, child := range value.Components {
			if text, ok := findModalText(child, customID); ok {
				return text, true
			}
		}
	case dgo.ActionsRow:
		for _, child := range value.Components {
			if text, ok := findModalText(child, customID); ok {
				return text, true
			}
		}
	case *dgo.Label:
		return findModalText(value.Component, customID)
	case dgo.Label:
		return findModalText(value.Component, customID)
	}
	return "", false
}
