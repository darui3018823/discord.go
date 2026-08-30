package bot

import (
	"errors"
	"testing"
	"time"
)

func TestPrefixGuildDMChecks(t *testing.T) {
	ctx := &PrefixContext{Message: message("!test")}
	if err := PrefixGuildOnly()(ctx); err != nil {
		t.Fatal(err)
	}
	if err := PrefixDMOnly()(ctx); !errors.Is(err, ErrDMOnly) {
		t.Fatalf("PrefixDMOnly error = %v", err)
	}
	ctx.Message.GuildID = ""
	if err := PrefixGuildOnly()(ctx); !errors.Is(err, ErrGuildOnly) {
		t.Fatalf("PrefixGuildOnly error = %v", err)
	}
}

func TestPrefixCooldown(t *testing.T) {
	cooldown, err := NewPrefixCooldown(1, time.Minute, PrefixCooldownPerUser)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	cooldown.now = func() time.Time { return now }
	ctx := &PrefixContext{Message: message("!test")}
	if err := cooldown.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cooldown.Check(ctx); !errors.Is(err, ErrCooldownActive) {
		t.Fatalf("second Check error = %v", err)
	}
	now = now.Add(time.Minute)
	if err := cooldown.Check(ctx); err != nil {
		t.Fatalf("expired Check = %v", err)
	}
}
