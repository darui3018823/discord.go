package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dgo "github.com/darui3018823/discord.go"
)

func TestExtensionLoadDispatchAndUnload(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.SetPrefixes("!"); err != nil {
		t.Fatal(err)
	}
	var calls []string
	teardownCalls := 0
	extension := NewExtension("Admin", func(_ context.Context, registrar *ExtensionRegistrar) error {
		if registrar.Bot() != framework {
			t.Fatal("registrar Bot did not return its host")
		}
		if err := registrar.AddCommands(Slash("inspect", "Inspect a value", func(*Context) error {
			calls = append(calls, "slash")
			return nil
		})); err != nil {
			return err
		}
		if err := registrar.AddPrefixCommands(TextCommand("inspect", func(*PrefixContext) error {
			calls = append(calls, "prefix")
			return nil
		})); err != nil {
			return err
		}
		if err := registrar.AddComponentPrefix("inspect:", func(ctx *ComponentContext) error {
			calls = append(calls, "component:"+ctx.Suffix)
			return nil
		}); err != nil {
			return err
		}
		if err := registrar.AddModal("inspect-modal", func(*ModalContext) error {
			calls = append(calls, "modal")
			return nil
		}); err != nil {
			return err
		}
		return registrar.AddEventHandler(func(*dgo.Session, *dgo.Ready) {})
	}, func(context.Context) error {
		teardownCalls++
		return nil
	})

	if err := framework.LoadExtension(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if got := framework.Extensions(); !reflect.DeepEqual(got, []string{"Admin"}) {
		t.Fatalf("Extensions = %v", got)
	}
	if !framework.Dispatch(context.Background(), interaction("inspect", dgo.ChatApplicationCommand)) {
		t.Fatal("extension slash command was not dispatched")
	}
	if !framework.DispatchMessage(context.Background(), message("!inspect")) {
		t.Fatal("extension prefix command was not dispatched")
	}
	if !framework.DispatchInteraction(context.Background(), componentInteraction("inspect:42")) {
		t.Fatal("extension component was not dispatched")
	}
	if !framework.DispatchInteraction(context.Background(), modalInteraction("inspect-modal")) {
		t.Fatal("extension modal was not dispatched")
	}
	wantCalls := []string{"slash", "prefix", "component:42", "modal"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}

	if err := framework.UnloadCog(context.Background(), " admin "); err != nil {
		t.Fatal(err)
	}
	if teardownCalls != 1 || len(framework.Extensions()) != 0 {
		t.Fatalf("teardown calls/extensions = %d/%v", teardownCalls, framework.Extensions())
	}
	if framework.Dispatch(context.Background(), interaction("inspect", dgo.ChatApplicationCommand)) ||
		framework.DispatchMessage(context.Background(), message("!inspect")) ||
		framework.DispatchInteraction(context.Background(), componentInteraction("inspect:42")) ||
		framework.DispatchInteraction(context.Background(), modalInteraction("inspect-modal")) {
		t.Fatal("an unloaded extension route was still dispatched")
	}
}

func TestExtensionRegistrationIsTransactional(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.SetPrefixes("!"); err != nil {
		t.Fatal(err)
	}
	if err := framework.Register(Slash("taken", "Existing", func(*Context) error { return nil })); err != nil {
		t.Fatal(err)
	}
	teardownCalls := 0
	extension := NewExtension("conflict", func(_ context.Context, registrar *ExtensionRegistrar) error {
		if err := registrar.AddPrefixCommands(TextCommand("temporary", func(*PrefixContext) error { return nil })); err != nil {
			return err
		}
		return registrar.AddCommands(Slash("taken", "Conflict", func(*Context) error { return nil }))
	}, func(context.Context) error {
		teardownCalls++
		return nil
	})

	err := framework.LoadExtension(context.Background(), extension)
	if !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("LoadExtension error = %v", err)
	}
	if teardownCalls != 1 {
		t.Fatalf("teardown calls = %d, want 1", teardownCalls)
	}
	if framework.DispatchMessage(context.Background(), message("!temporary")) {
		t.Fatal("a registration from a rejected transaction was published")
	}
	if len(framework.Extensions()) != 0 {
		t.Fatalf("Extensions = %v", framework.Extensions())
	}
}

func TestExtensionSetupPanicAndDuplicateName(t *testing.T) {
	framework := newTestBot(t)
	teardownCalls := 0
	panicking := NewExtension("panic", func(context.Context, *ExtensionRegistrar) error {
		panic("setup failed")
	}, func(context.Context) error {
		teardownCalls++
		return nil
	})
	var panicErr *PanicError
	if err := framework.LoadCog(context.Background(), panicking); !errors.As(err, &panicErr) {
		t.Fatalf("panic error = %v", err)
	}
	if teardownCalls != 1 {
		t.Fatalf("teardown calls after setup panic = %d, want 1", teardownCalls)
	}

	first := NewExtension("Utilities", func(context.Context, *ExtensionRegistrar) error { return nil })
	if err := framework.LoadExtension(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	duplicate := NewExtension(" utilities ", func(context.Context, *ExtensionRegistrar) error { return nil })
	if err := framework.LoadExtension(context.Background(), duplicate); !errors.Is(err, ErrExtensionLoaded) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestCloseUnloadsExtensionsAndRejectsNewOnes(t *testing.T) {
	framework := newTestBot(t)
	teardownCalls := 0
	extension := NewExtension("worker", func(context.Context, *ExtensionRegistrar) error { return nil }, func(context.Context) error {
		teardownCalls++
		return nil
	})
	if err := framework.LoadExtension(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if err := framework.Close(); err != nil {
		t.Fatal(err)
	}
	if teardownCalls != 1 {
		t.Fatalf("teardown calls = %d, want 1", teardownCalls)
	}
	if err := framework.LoadExtension(context.Background(), extension); !errors.Is(err, ErrBotClosed) {
		t.Fatalf("load after Close error = %v", err)
	}
}

func TestExtensionTeardownErrorDoesNotKeepRegistrations(t *testing.T) {
	framework := newTestBot(t)
	want := errors.New("teardown failed")
	extension := NewExtension("broken-cleanup", func(_ context.Context, registrar *ExtensionRegistrar) error {
		return registrar.AddCommands(Slash("cleanup", "Cleanup", func(*Context) error { return nil }))
	}, func(context.Context) error { return want })
	if err := framework.LoadExtension(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if err := framework.UnloadExtension(context.Background(), "broken-cleanup"); !errors.Is(err, want) {
		t.Fatalf("UnloadExtension error = %v", err)
	}
	if framework.Dispatch(context.Background(), interaction("cleanup", dgo.ChatApplicationCommand)) {
		t.Fatal("route remained registered after a teardown error")
	}
}
