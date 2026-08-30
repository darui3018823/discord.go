package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dgo "github.com/darui3018823/discord.go"
)

func TestSubcommandsBuildDefinitionsAndDispatchLeafOptions(t *testing.T) {
	framework := newTestBot(t)
	var gotPath []string
	var gotValue string
	command := Slash("admin", "Administration", nil)
	err := command.AddSubcommands(Sub(
		"ban",
		"Ban a user",
		func(ctx *Context) error {
			gotPath = append([]string(nil), ctx.CommandPath...)
			option := ctx.Option("reason")
			if option != nil {
				gotValue, _ = option.Value.(string)
			}
			return nil
		},
		&dgo.ApplicationCommandOption{
			Type:        dgo.ApplicationCommandOptionString,
			Name:        "reason",
			Description: "Reason",
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := framework.Register(command); err != nil {
		t.Fatal(err)
	}

	definition := framework.CommandDefinitions()[0]
	if len(definition.Options) != 1 || definition.Options[0].Type != dgo.ApplicationCommandOptionSubCommand {
		t.Fatalf("unexpected definition options: %#v", definition.Options)
	}
	event := interaction("admin", dgo.ChatApplicationCommand)
	event.Data = dgo.ApplicationCommandInteractionData{
		Name:        "admin",
		CommandType: dgo.ChatApplicationCommand,
		Options: []*dgo.ApplicationCommandInteractionDataOption{{
			Type: dgo.ApplicationCommandOptionSubCommand,
			Name: "ban",
			Options: []*dgo.ApplicationCommandInteractionDataOption{{
				Type:  dgo.ApplicationCommandOptionString,
				Name:  "reason",
				Value: "spam",
			}},
		}},
	}
	if !framework.Dispatch(context.Background(), event) {
		t.Fatal("expected interaction to be dispatched")
	}
	if !reflect.DeepEqual(gotPath, []string{"ban"}) || gotValue != "spam" {
		t.Fatalf("path/value = %v/%q", gotPath, gotValue)
	}
}

func TestSubcommandGroupDispatch(t *testing.T) {
	framework := newTestBot(t)
	called := false
	command := Slash("config", "Configuration", nil)
	if err := command.AddGroups(Group("role", "Role settings", Sub("set", "Set role", func(ctx *Context) error {
		called = reflect.DeepEqual(ctx.CommandPath, []string{"role", "set"})
		return nil
	}))); err != nil {
		t.Fatal(err)
	}
	if err := framework.Register(command); err != nil {
		t.Fatal(err)
	}
	event := interaction("config", dgo.ChatApplicationCommand)
	event.Data = dgo.ApplicationCommandInteractionData{
		Name:        "config",
		CommandType: dgo.ChatApplicationCommand,
		Options: []*dgo.ApplicationCommandInteractionDataOption{{
			Type: dgo.ApplicationCommandOptionSubCommandGroup,
			Name: "role",
			Options: []*dgo.ApplicationCommandInteractionDataOption{{
				Type: dgo.ApplicationCommandOptionSubCommand,
				Name: "set",
			}},
		}},
	}
	framework.Dispatch(context.Background(), event)
	if !called {
		t.Fatal("grouped subcommand handler was not called")
	}
}

func TestUnknownSubcommandUsesErrorHandler(t *testing.T) {
	framework := newTestBot(t)
	command := Slash("admin", "Administration", nil)
	if err := command.AddSubcommands(Sub("ban", "Ban", func(*Context) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if err := framework.Register(command); err != nil {
		t.Fatal(err)
	}
	var got error
	framework.SetErrorHandler(func(_ *Context, err error) { got = err })
	event := interaction("admin", dgo.ChatApplicationCommand)
	event.Data = dgo.ApplicationCommandInteractionData{
		Name:        "admin",
		CommandType: dgo.ChatApplicationCommand,
		Options: []*dgo.ApplicationCommandInteractionDataOption{{
			Type: dgo.ApplicationCommandOptionSubCommand,
			Name: "missing",
		}},
	}
	framework.Dispatch(context.Background(), event)
	if !errors.Is(got, ErrCommandRouteNotFound) {
		t.Fatalf("error = %v, want ErrCommandRouteNotFound", got)
	}
}

func TestAddSubcommandsRejectsOrdinaryTopLevelOptions(t *testing.T) {
	command := Slash("mixed", "Mixed", nil)
	command.Definition.Options = []*dgo.ApplicationCommandOption{{
		Type: dgo.ApplicationCommandOptionString,
		Name: "value",
	}}
	err := command.AddSubcommands(Sub("sub", "Sub", func(*Context) error { return nil }))
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("error = %v, want ErrInvalidCommand", err)
	}
}
