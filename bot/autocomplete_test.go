package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	dgo "github.com/darui3018823/discord.go"
)

func autocompleteInteraction(name string, options ...*dgo.ApplicationCommandInteractionDataOption) *dgo.InteractionCreate {
	return &dgo.InteractionCreate{Interaction: &dgo.Interaction{
		ID:    "interaction",
		Token: "interaction-token",
		Type:  dgo.InteractionApplicationCommandAutocomplete,
		Data: dgo.ApplicationCommandInteractionData{
			Name:        name,
			CommandType: dgo.ChatApplicationCommand,
			Options:     options,
		},
	}}
}

func TestAutocompleteDispatchesFocusedOptionAndResponds(t *testing.T) {
	framework := newTestBot(t)
	var response dgo.InteractionResponse
	framework.Session().Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	command := Slash("search", "Search", func(*Context) error { return nil })
	command.Definition.Options = []*dgo.ApplicationCommandOption{{
		Type:        dgo.ApplicationCommandOptionString,
		Name:        "query",
		Description: "Query",
	}}
	if err := command.SetAutocomplete("query", func(ctx *AutocompleteContext) error {
		if ctx.Focused.Name != "query" || ctx.Focused.Value != "go" {
			t.Fatalf("focused = %#v", ctx.Focused)
		}
		return ctx.Respond(StringChoice("Go", "go"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := framework.Register(command); err != nil {
		t.Fatal(err)
	}
	if !framework.DispatchInteraction(context.Background(), autocompleteInteraction("search",
		&dgo.ApplicationCommandInteractionDataOption{
			Type: dgo.ApplicationCommandOptionString, Name: "query", Value: "go", Focused: true,
		},
	)) {
		t.Fatal("autocomplete was not dispatched")
	}
	if response.Type != dgo.InteractionApplicationCommandAutocompleteResult || response.Data == nil || len(response.Data.Choices) != 1 {
		t.Fatalf("response = %#v", response)
	}
	if !framework.CommandDefinitions()[0].Options[0].Autocomplete {
		t.Fatal("command definition did not enable autocomplete")
	}
}

func TestSubcommandAutocompleteUsesLeafRouteAndChecks(t *testing.T) {
	framework := newTestBot(t)
	checked := false
	called := false
	sub := Sub("find", "Find", func(*Context) error { return nil }, &dgo.ApplicationCommandOption{
		Type: dgo.ApplicationCommandOptionString, Name: "name", Description: "Name",
	})
	sub.AddChecks(func(*Context) error { checked = true; return nil })
	if err := sub.SetAutocomplete("name", func(ctx *AutocompleteContext) error {
		called = ctx.Focused.Name == "name" && len(ctx.CommandPath) == 1 && ctx.CommandPath[0] == "find"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	command := Slash("users", "Users", nil)
	if err := command.AddSubcommands(sub); err != nil {
		t.Fatal(err)
	}
	if err := framework.Register(command); err != nil {
		t.Fatal(err)
	}
	event := autocompleteInteraction("users", &dgo.ApplicationCommandInteractionDataOption{
		Type: dgo.ApplicationCommandOptionSubCommand,
		Name: "find",
		Options: []*dgo.ApplicationCommandInteractionDataOption{{
			Type: dgo.ApplicationCommandOptionString, Name: "name", Value: "a", Focused: true,
		}},
	})
	if !framework.DispatchInteraction(context.Background(), event) || !checked || !called {
		t.Fatalf("dispatched/checked/called = %t/%t/%t", true, checked, called)
	}
}

func TestAutocompleteValidationAndChoiceLimit(t *testing.T) {
	command := Slash("invalid", "Invalid", func(*Context) error { return nil })
	command.Definition.Options = []*dgo.ApplicationCommandOption{{
		Type: dgo.ApplicationCommandOptionBoolean, Name: "flag", Description: "Flag",
	}}
	if err := command.SetAutocomplete("flag", func(*AutocompleteContext) error { return nil }); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unsupported option error = %v", err)
	}
	ctx := &AutocompleteContext{Context: &Context{}}
	choices := make([]*dgo.ApplicationCommandOptionChoice, 26)
	if err := ctx.Respond(choices...); err == nil {
		t.Fatal("expected choice limit error")
	}
}

func TestAutocompletePanicUsesCommandErrorScope(t *testing.T) {
	framework := newTestBot(t)
	command := Slash("panic-complete", "Panic", func(*Context) error { return nil })
	command.Definition.Options = []*dgo.ApplicationCommandOption{{
		Type: dgo.ApplicationCommandOptionString, Name: "value", Description: "Value",
	}}
	if err := command.SetAutocomplete("value", func(*AutocompleteContext) error { panic("boom") }); err != nil {
		t.Fatal(err)
	}
	var got error
	command.SetErrorHandler(func(_ *Context, err error) { got = err })
	if err := framework.Register(command); err != nil {
		t.Fatal(err)
	}
	framework.DispatchInteraction(context.Background(), autocompleteInteraction("panic-complete",
		&dgo.ApplicationCommandInteractionDataOption{
			Type: dgo.ApplicationCommandOptionString, Name: "value", Focused: true,
		},
	))
	var panicErr *PanicError
	if !errors.As(got, &panicErr) || panicErr.Value != "boom" {
		t.Fatalf("panic error = %#v", got)
	}
}
