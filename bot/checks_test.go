package bot

import (
	"errors"
	"testing"
	"time"

	dgo "github.com/darui3018823/discord.go"
)

func TestGuildAndPermissionChecks(t *testing.T) {
	ctx := &Context{Interaction: &dgo.Interaction{}}
	if err := GuildOnly()(ctx); !errors.Is(err, ErrGuildOnly) {
		t.Fatalf("GuildOnly error = %v", err)
	}
	ctx.Interaction.GuildID = "guild"
	ctx.Interaction.Member = &dgo.Member{Permissions: dgo.PermissionManageMessages}
	if err := GuildOnly()(ctx); err != nil {
		t.Fatal(err)
	}
	if err := DMOnly()(ctx); !errors.Is(err, ErrDMOnly) {
		t.Fatalf("DMOnly error = %v", err)
	}
	if err := HasPermissions(dgo.PermissionManageMessages)(ctx); err != nil {
		t.Fatal(err)
	}
	if err := HasPermissions(dgo.PermissionManageRoles)(ctx); !errors.Is(err, ErrMissingPermissions) {
		t.Fatalf("HasPermissions error = %v", err)
	}
	ctx.Interaction.Member.Permissions = dgo.PermissionAdministrator
	if err := HasPermissions(dgo.PermissionManageRoles)(ctx); err != nil {
		t.Fatalf("administrator did not bypass check: %v", err)
	}
}

func TestCooldownUsesIndependentUserBuckets(t *testing.T) {
	cooldown, err := NewCooldown(1, time.Minute, CooldownPerUser)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	cooldown.now = func() time.Time { return now }
	ctx := &Context{Interaction: &dgo.Interaction{User: &dgo.User{ID: "one"}}}
	if err := cooldown.Check(ctx); err != nil {
		t.Fatal(err)
	}
	err = cooldown.Check(ctx)
	if !errors.Is(err, ErrCooldownActive) {
		t.Fatalf("second Check error = %v", err)
	}
	var cooldownErr *CooldownError
	if !errors.As(err, &cooldownErr) || cooldownErr.RetryAfter != time.Minute {
		t.Fatalf("CooldownError = %#v", cooldownErr)
	}

	ctx.Interaction.User.ID = "two"
	if err := cooldown.Check(ctx); err != nil {
		t.Fatalf("independent user bucket failed: %v", err)
	}
	now = now.Add(time.Minute)
	ctx.Interaction.User.ID = "one"
	if err := cooldown.Check(ctx); err != nil {
		t.Fatalf("expired bucket failed: %v", err)
	}
}

func TestCooldownValidatesConfiguration(t *testing.T) {
	if _, err := NewCooldown(0, time.Second, nil); err == nil {
		t.Fatal("expected invalid limit error")
	}
	if _, err := NewCooldown(1, 0, nil); err == nil {
		t.Fatal("expected invalid period error")
	}
}
