package bot

import (
	"errors"
	"testing"

	dgo "github.com/darui3018823/discord.go"
)

func argumentContext(options ...*dgo.ApplicationCommandInteractionDataOption) *Context {
	return &Context{Options: options, Data: dgo.ApplicationCommandInteractionData{}}
}

func TestTypedPrimitiveOptions(t *testing.T) {
	ctx := argumentContext(
		&dgo.ApplicationCommandInteractionDataOption{Name: "text", Type: dgo.ApplicationCommandOptionString, Value: "hello"},
		&dgo.ApplicationCommandInteractionDataOption{Name: "count", Type: dgo.ApplicationCommandOptionInteger, Value: float64(42)},
		&dgo.ApplicationCommandInteractionDataOption{Name: "ratio", Type: dgo.ApplicationCommandOptionNumber, Value: 1.5},
		&dgo.ApplicationCommandInteractionDataOption{Name: "enabled", Type: dgo.ApplicationCommandOptionBoolean, Value: true},
	)
	if got, err := ctx.String("text"); err != nil || got != "hello" {
		t.Fatalf("String = %q, %v", got, err)
	}
	if got, err := ctx.Integer("count"); err != nil || got != 42 {
		t.Fatalf("Integer = %d, %v", got, err)
	}
	if got, err := ctx.Number("ratio"); err != nil || got != 1.5 {
		t.Fatalf("Number = %f, %v", got, err)
	}
	if got, err := ctx.Boolean("enabled"); err != nil || !got {
		t.Fatalf("Boolean = %t, %v", got, err)
	}
}

func TestTypedOptionErrorsDoNotPanic(t *testing.T) {
	ctx := argumentContext(&dgo.ApplicationCommandInteractionDataOption{
		Name:  "count",
		Type:  dgo.ApplicationCommandOptionInteger,
		Value: "not-a-number",
	})
	if _, err := ctx.String("missing"); !errors.Is(err, ErrOptionNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	var typeErr *OptionTypeError
	if _, err := ctx.String("count"); !errors.As(err, &typeErr) {
		t.Fatalf("type error = %v", err)
	}
	if _, err := ctx.Integer("count"); err == nil {
		t.Fatal("expected malformed value error")
	}
}

func TestResolvedOptionsAndContextMenuTargets(t *testing.T) {
	user := &dgo.User{ID: "user", Username: "tester"}
	role := &dgo.Role{ID: "role", Name: "moderator"}
	message := &dgo.Message{ID: "message", Content: "hello"}
	ctx := argumentContext(
		&dgo.ApplicationCommandInteractionDataOption{Name: "user", Type: dgo.ApplicationCommandOptionUser, Value: "user"},
		&dgo.ApplicationCommandInteractionDataOption{Name: "role", Type: dgo.ApplicationCommandOptionRole, Value: "role"},
	)
	ctx.Data = dgo.ApplicationCommandInteractionData{
		CommandType: dgo.MessageApplicationCommand,
		TargetID:    "message",
		Resolved: &dgo.ApplicationCommandInteractionDataResolved{
			Users:    map[string]*dgo.User{"user": user},
			Roles:    map[string]*dgo.Role{"role": role},
			Messages: map[string]*dgo.Message{"message": message},
		},
	}
	if got, err := ctx.UserOption("user"); err != nil || got != user {
		t.Fatalf("UserOption = %#v, %v", got, err)
	}
	if got, err := ctx.Role("role"); err != nil || got != role {
		t.Fatalf("Role = %#v, %v", got, err)
	}
	if got, err := ctx.TargetMessage(); err != nil || got != message {
		t.Fatalf("TargetMessage = %#v, %v", got, err)
	}
}
