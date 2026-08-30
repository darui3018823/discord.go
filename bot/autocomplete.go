package bot

import (
	"context"
	"errors"
	"fmt"

	dgo "github.com/darui3018823/discord.go"
)

// AutocompleteHandler handles one focused application-command option.
type AutocompleteHandler func(*AutocompleteContext) error

// AutocompleteContext contains the command context and focused option.
type AutocompleteContext struct {
	*Context
	Focused *dgo.ApplicationCommandInteractionDataOption
}

// Respond sends at most 25 autocomplete choices.
func (c *AutocompleteContext) Respond(choices ...*dgo.ApplicationCommandOptionChoice) error {
	if len(choices) > 25 {
		return errors.New("autocomplete responses are limited to 25 choices")
	}
	return c.Context.Respond(&dgo.InteractionResponse{
		Type: dgo.InteractionApplicationCommandAutocompleteResult,
		Data: &dgo.InteractionResponseData{Choices: choices},
	})
}

// StringChoice creates a string autocomplete choice.
func StringChoice(name, value string) *dgo.ApplicationCommandOptionChoice {
	return &dgo.ApplicationCommandOptionChoice{Name: name, Value: value}
}

// IntegerChoice creates an integer autocomplete choice.
func IntegerChoice(name string, value int64) *dgo.ApplicationCommandOptionChoice {
	return &dgo.ApplicationCommandOptionChoice{Name: name, Value: value}
}

// NumberChoice creates a numeric autocomplete choice.
func NumberChoice(name string, value float64) *dgo.ApplicationCommandOptionChoice {
	return &dgo.ApplicationCommandOptionChoice{Name: name, Value: value}
}

func (b *Bot) dispatchAutocomplete(ctx context.Context, event *dgo.InteractionCreate) bool {
	if event == nil || event.Interaction == nil || event.Type != dgo.InteractionApplicationCommandAutocomplete {
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
	globalChecks := append([]Check(nil), b.checks...)
	botErrorHandler := b.errorHandle
	b.mu.RUnlock()
	if command == nil {
		return false
	}

	path, options, routeErr := resolveCommandRoute(data.Options)
	checks := append(globalChecks, command.Checks...)
	errorHandler := botErrorHandler
	autocomplete := command.Autocomplete
	if command.OnError != nil {
		errorHandler = command.OnError
	}
	if len(path) > 0 {
		route := command.routes[routeKey(path)]
		if route == nil {
			routeErr = fmt.Errorf("%w: %s", ErrCommandRouteNotFound, routeKey(path))
		} else {
			checks = append(checks, route.checks...)
			autocomplete = route.autocomplete
			if route.errorHandler != nil {
				errorHandler = route.errorHandler
			}
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
	if routeErr != nil {
		if errorHandler != nil {
			reportCommandError(errorHandler, commandContext, routeErr)
		}
		return true
	}
	focused := focusedOption(options)
	if focused == nil || autocomplete[focused.Name] == nil {
		return false
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
	autocompleteContext := &AutocompleteContext{Context: commandContext, Focused: focused}
	if err := invokeAutocompleteHandler(autocomplete[focused.Name], autocompleteContext); err != nil && errorHandler != nil {
		reportCommandError(errorHandler, commandContext, err)
	}
	return true
}

func focusedOption(options []*dgo.ApplicationCommandInteractionDataOption) *dgo.ApplicationCommandInteractionDataOption {
	for _, option := range options {
		if option != nil && option.Focused {
			return option
		}
	}
	return nil
}

func invokeAutocompleteHandler(handler AutocompleteHandler, ctx *AutocompleteContext) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{Value: recovered}
		}
	}()
	return handler(ctx)
}
