package bot

import (
	"errors"
	"testing"
	"time"
)

func TestPrefixTypedArguments(t *testing.T) {
	ctx := &PrefixContext{Args: []string{
		"text", "42", "1.5", "true", "2m30s",
		"<@!123>", "<@&456>", "<#789>", "tail",
	}}
	if got, err := ctx.String(0); err != nil || got != "text" {
		t.Fatalf("String = %q, %v", got, err)
	}
	if got, err := ctx.Integer(1); err != nil || got != 42 {
		t.Fatalf("Integer = %d, %v", got, err)
	}
	if got, err := ctx.Number(2); err != nil || got != 1.5 {
		t.Fatalf("Number = %f, %v", got, err)
	}
	if got, err := ctx.Boolean(3); err != nil || !got {
		t.Fatalf("Boolean = %t, %v", got, err)
	}
	if got, err := ctx.Duration(4); err != nil || got != 150*time.Second {
		t.Fatalf("Duration = %s, %v", got, err)
	}
	if got, err := ctx.UserID(5); err != nil || got != "123" {
		t.Fatalf("UserID = %q, %v", got, err)
	}
	if got, err := ctx.RoleID(6); err != nil || got != "456" {
		t.Fatalf("RoleID = %q, %v", got, err)
	}
	if got, err := ctx.ChannelID(7); err != nil || got != "789" {
		t.Fatalf("ChannelID = %q, %v", got, err)
	}
	if got, err := ctx.Rest(7); err != nil || got != "<#789> tail" {
		t.Fatalf("Rest = %q, %v", got, err)
	}
}

func TestPrefixArgumentErrors(t *testing.T) {
	ctx := &PrefixContext{Args: []string{"not-an-int", "<@bad>"}}
	for _, call := range []func() error{
		func() error { _, err := ctx.String(3); return err },
		func() error { _, err := ctx.Integer(0); return err },
		func() error { _, err := ctx.UserID(1); return err },
	} {
		if err := call(); !errors.Is(err, ErrPrefixArgument) {
			t.Fatalf("error = %v, want ErrPrefixArgument", err)
		}
	}
}
