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

func TestAddSubcommandsRejectsDuplicateBatchAtomically(t *testing.T) {
	command := Slash("duplicate", "Duplicate", nil)
	err := command.AddSubcommands(
		Sub("same", "One", func(*Context) error { return nil }),
		Sub("same", "Two", func(*Context) error { return nil }),
	)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("error = %v, want ErrInvalidCommand", err)
	}
	if len(command.Definition.Options) != 0 || len(command.routes) != 0 {
		t.Fatal("failed batch partially mutated command")
	}
}

func TestChecksMiddlewareAndErrorHandlersComposeByScope(t *testing.T) {
	framework := newTestBot(t)
	var calls []string
	framework.AddChecks(func(*Context) error {
		calls = append(calls, "global-check")
		return nil
	})
	framework.Use(func(next Handler) Handler {
		return func(ctx *Context) error {
			calls = append(calls, "global-middleware")
			return next(ctx)
		}
	})

	sub := Sub("leaf", "Leaf", func(*Context) error {
		calls = append(calls, "handler")
		return errors.New("handler failed")
	})
	sub.AddChecks(func(*Context) error {
		calls = append(calls, "sub-check")
		return nil
	})
	sub.Use(func(next Handler) Handler {
		return func(ctx *Context) error {
			calls = append(calls, "sub-middleware")
			return next(ctx)
		}
	})
	sub.SetErrorHandler(func(_ *Context, err error) {
		calls = append(calls, "sub-error:"+err.Error())
	})

	command := Slash("tree", "Tree", nil)
	command.AddChecks(func(*Context) error {
		calls = append(calls, "command-check")
		return nil
	})
	command.Use(func(next Handler) Handler {
		return func(ctx *Context) error {
			calls = append(calls, "command-middleware")
			return next(ctx)
		}
	})
	command.SetErrorHandler(func(_ *Context, _ error) {
		calls = append(calls, "command-error")
	})
	if err := command.AddSubcommands(sub); err != nil {
		t.Fatal(err)
	}
	if err := framework.Register(command); err != nil {
		t.Fatal(err)
	}

	event := interaction("tree", dgo.ChatApplicationCommand)
	event.Data = dgo.ApplicationCommandInteractionData{
		Name:        "tree",
		CommandType: dgo.ChatApplicationCommand,
		Options: []*dgo.ApplicationCommandInteractionDataOption{{
			Type: dgo.ApplicationCommandOptionSubCommand,
			Name: "leaf",
		}},
	}
	framework.Dispatch(context.Background(), event)
	want := []string{
		"global-check", "command-check", "sub-check",
		"global-middleware", "command-middleware", "sub-middleware",
		"handler", "sub-error:handler failed",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestFailedCheckStopsHandlerAndPreservesCause(t *testing.T) {
	framework := newTestBot(t)
	want := errors.New("denied")
	called := false
	command := Slash("secure", "Secure", func(*Context) error {
		called = true
		return nil
	})
	command.AddChecks(func(*Context) error { return want })
	if err := framework.Register(command); err != nil {
		t.Fatal(err)
	}
	var got error
	commandErrors := 0
	framework.SetErrorHandler(func(_ *Context, err error) {
		commandErrors++
		got = err
	})
	framework.Dispatch(context.Background(), interaction("secure", dgo.ChatApplicationCommand))
	if called {
		t.Fatal("handler ran after failed check")
	}
	if commandErrors != 1 || !errors.Is(got, want) {
		t.Fatalf("error handler got %v (%d calls)", got, commandErrors)
	}
	var checkErr *CheckError
	if !errors.As(got, &checkErr) || checkErr.Index != 0 {
		t.Fatalf("CheckError = %#v", checkErr)
	}
}

func TestHandlerPanicBecomesPanicError(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.Register(Slash("panic", "Panic", func(*Context) error {
		panic("boom")
	})); err != nil {
		t.Fatal(err)
	}
	var got error
	framework.SetErrorHandler(func(_ *Context, err error) { got = err })
	framework.Dispatch(context.Background(), interaction("panic", dgo.ChatApplicationCommand))
	var panicErr *PanicError
	if !errors.As(got, &panicErr) || panicErr.Value != "boom" {
		t.Fatalf("PanicError = %#v", panicErr)
	}
}
