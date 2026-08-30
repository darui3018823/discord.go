package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dgo "github.com/darui3018823/discord.go"
)

func newTestBot(t *testing.T) *Bot {
	t.Helper()
	session, err := dgo.NewBot("test-token")
	if err != nil {
		t.Fatal(err)
	}
	framework, err := New(session)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		framework.mu.Lock()
		remove := framework.removeEvent
		framework.removeEvent = nil
		removeMessage := framework.removeMessageEvent
		framework.removeMessageEvent = nil
		framework.mu.Unlock()
		if remove != nil {
			remove()
		}
		if removeMessage != nil {
			removeMessage()
		}
	})
	return framework
}

func interaction(name string, typeID dgo.ApplicationCommandType) *dgo.InteractionCreate {
	return &dgo.InteractionCreate{Interaction: &dgo.Interaction{
		Type: dgo.InteractionApplicationCommand,
		Data: dgo.ApplicationCommandInteractionData{
			Name:        name,
			CommandType: typeID,
		},
	}}
}

func TestDispatchRunsMiddlewareInRegistrationOrder(t *testing.T) {
	framework := newTestBot(t)
	var calls []string
	framework.Use(
		func(next Handler) Handler {
			return func(ctx *Context) error {
				calls = append(calls, "first-before")
				err := next(ctx)
				calls = append(calls, "first-after")
				return err
			}
		},
		func(next Handler) Handler {
			return func(ctx *Context) error {
				calls = append(calls, "second-before")
				err := next(ctx)
				calls = append(calls, "second-after")
				return err
			}
		},
	)
	err := framework.Register(Slash("ping", "Replies with pong", func(ctx *Context) error {
		calls = append(calls, "handler")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !framework.Dispatch(context.Background(), interaction("ping", dgo.ChatApplicationCommand)) {
		t.Fatal("expected interaction to be dispatched")
	}
	want := []string{"first-before", "second-before", "handler", "second-after", "first-after"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestRegisterRejectsDuplicateCommandAtomically(t *testing.T) {
	framework := newTestBot(t)
	first := Slash("ping", "one", func(*Context) error { return nil })
	second := Slash("ping", "two", func(*Context) error { return nil })
	if err := framework.Register(first, second); !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("Register error = %v, want ErrDuplicateCommand", err)
	}
	if got := len(framework.CommandDefinitions()); got != 0 {
		t.Fatalf("registered %d commands after failed batch, want 0", got)
	}
}

func TestDispatchRoutesErrors(t *testing.T) {
	framework := newTestBot(t)
	want := errors.New("boom")
	var got error
	framework.SetErrorHandler(func(_ *Context, err error) {
		got = err
	})
	if err := framework.Register(Slash("fail", "fails", func(*Context) error {
		return want
	})); err != nil {
		t.Fatal(err)
	}

	if !framework.Dispatch(context.Background(), interaction("fail", dgo.ChatApplicationCommand)) {
		t.Fatal("expected interaction to be dispatched")
	}
	if !errors.Is(got, want) {
		t.Fatalf("error handler got %v, want %v", got, want)
	}
}

func TestCommandDefinitionsAreSortedAndCopied(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.Register(
		Slash("zeta", "z", func(*Context) error { return nil }),
		Slash("alpha", "a", func(*Context) error { return nil }),
	); err != nil {
		t.Fatal(err)
	}

	definitions := framework.CommandDefinitions()
	if definitions[0].Name != "alpha" || definitions[1].Name != "zeta" {
		t.Fatalf("unexpected order: %s, %s", definitions[0].Name, definitions[1].Name)
	}
	definitions[0].Name = "changed"
	if got := framework.CommandDefinitions()[0].Name; got != "alpha" {
		t.Fatalf("stored definition was mutated through returned copy: %q", got)
	}
}
