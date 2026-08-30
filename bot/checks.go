package bot

import (
	"errors"
	"fmt"
	"sync"
	"time"

	dgo "github.com/darui3018823/discord.go"
)

var (
	// ErrGuildOnly is returned when a guild-only command is used in a DM.
	ErrGuildOnly = errors.New("command can only be used in a guild")
	// ErrDMOnly is returned when a DM-only command is used in a guild.
	ErrDMOnly = errors.New("command can only be used in a DM")
	// ErrMissingPermissions is returned when an invocation lacks required
	// member permissions.
	ErrMissingPermissions = errors.New("missing command permissions")
	// ErrCooldownActive is wrapped by CooldownError while a rate bucket is
	// exhausted.
	ErrCooldownActive = errors.New("command cooldown is active")
)

// CheckError identifies the check index that rejected an invocation.
type CheckError struct {
	Index int
	Err   error
}

func (e *CheckError) Error() string {
	return fmt.Sprintf("command check %d failed: %v", e.Index, e.Err)
}

// Unwrap exposes the check's original error for errors.Is and errors.As.
func (e *CheckError) Unwrap() error { return e.Err }

// GuildOnly rejects interactions outside a guild.
func GuildOnly() Check {
	return func(ctx *Context) error {
		if ctx.Interaction == nil || ctx.Interaction.GuildID == "" {
			return ErrGuildOnly
		}
		return nil
	}
}

// DMOnly rejects interactions inside a guild.
func DMOnly() Check {
	return func(ctx *Context) error {
		if ctx.Interaction != nil && ctx.Interaction.GuildID != "" {
			return ErrDMOnly
		}
		return nil
	}
}

// HasPermissions requires all member permission bits. Administrator bypasses
// the individual bit check, matching Discord permission semantics.
func HasPermissions(required int64) Check {
	return func(ctx *Context) error {
		if ctx.Interaction == nil || ctx.Interaction.Member == nil {
			return fmt.Errorf("%w: required=%d available=0", ErrMissingPermissions, required)
		}
		available := ctx.Interaction.Member.Permissions
		if available&dgo.PermissionAdministrator != 0 || available&required == required {
			return nil
		}
		return fmt.Errorf("%w: required=%d available=%d", ErrMissingPermissions, required, available)
	}
}

// BotHasPermissions requires all application permission bits in the channel.
func BotHasPermissions(required int64) Check {
	return func(ctx *Context) error {
		if ctx.Interaction == nil {
			return fmt.Errorf("%w: required=%d available=0", ErrMissingPermissions, required)
		}
		available := ctx.Interaction.AppPermissions
		if available&dgo.PermissionAdministrator != 0 || available&required == required {
			return nil
		}
		return fmt.Errorf("%w: required=%d available=%d", ErrMissingPermissions, required, available)
	}
}

// CooldownKey selects an independent cooldown bucket.
type CooldownKey func(*Context) string

// CooldownPerUser keys cooldowns by invoking user.
func CooldownPerUser(ctx *Context) string {
	if user := ctx.User(); user != nil {
		return "user:" + user.ID
	}
	return "user:unknown"
}

// CooldownPerGuild keys cooldowns by guild and falls back to the current DM
// channel.
func CooldownPerGuild(ctx *Context) string {
	if ctx.Interaction == nil {
		return "guild:unknown"
	}
	if ctx.Interaction.GuildID != "" {
		return "guild:" + ctx.Interaction.GuildID
	}
	return "channel:" + ctx.Interaction.ChannelID
}

// CooldownGlobal places every invocation in one bucket.
func CooldownGlobal(*Context) string { return "global" }

type cooldownBucket struct {
	started time.Time
	uses    int
}

// Cooldown is a concurrency-safe fixed-window command cooldown.
type Cooldown struct {
	limit  int
	period time.Duration
	key    CooldownKey

	mu        sync.Mutex
	buckets   map[string]cooldownBucket
	now       func() time.Time
	lastSweep time.Time
}

// NewCooldown permits limit invocations per period for each key. A nil key
// defaults to CooldownPerUser.
func NewCooldown(limit int, period time.Duration, key CooldownKey) (*Cooldown, error) {
	if limit <= 0 || period <= 0 {
		return nil, errors.New("cooldown limit and period must be positive")
	}
	if key == nil {
		key = CooldownPerUser
	}
	return &Cooldown{
		limit:   limit,
		period:  period,
		key:     key,
		buckets: make(map[string]cooldownBucket),
		now:     time.Now,
	}, nil
}

// CooldownError reports how long remains in a rejected bucket.
type CooldownError struct {
	Key        string
	RetryAfter time.Duration
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("%v for %s; retry after %s", ErrCooldownActive, e.Key, e.RetryAfter)
}

// Unwrap supports errors.Is(err, ErrCooldownActive).
func (e *CooldownError) Unwrap() error { return ErrCooldownActive }

// Check consumes one cooldown use or returns CooldownError.
func (c *Cooldown) Check(ctx *Context) error {
	key := c.key(ctx)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastSweep.IsZero() || now.Sub(c.lastSweep) >= c.period {
		for bucketKey, bucket := range c.buckets {
			if now.Sub(bucket.started) >= c.period {
				delete(c.buckets, bucketKey)
			}
		}
		c.lastSweep = now
	}
	bucket, exists := c.buckets[key]
	if !exists || now.Sub(bucket.started) >= c.period {
		c.buckets[key] = cooldownBucket{started: now, uses: 1}
		return nil
	}
	if bucket.uses >= c.limit {
		return &CooldownError{Key: key, RetryAfter: c.period - now.Sub(bucket.started)}
	}
	bucket.uses++
	c.buckets[key] = bucket
	return nil
}

// Reset clears one cooldown bucket.
func (c *Cooldown) Reset(key string) {
	c.mu.Lock()
	delete(c.buckets, key)
	c.mu.Unlock()
}

// ResetAll clears every cooldown bucket.
func (c *Cooldown) ResetAll() {
	c.mu.Lock()
	c.buckets = make(map[string]cooldownBucket)
	c.mu.Unlock()
}
