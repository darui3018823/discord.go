package bot

import (
	"errors"
	"fmt"
	"sync"
	"time"

	dgo "github.com/darui3018823/discord.go"
)

// PrefixGuildOnly rejects message commands outside guilds.
func PrefixGuildOnly() PrefixCheck {
	return func(ctx *PrefixContext) error {
		if ctx.Message == nil || ctx.Message.Message == nil || ctx.Message.GuildID == "" {
			return ErrGuildOnly
		}
		return nil
	}
}

// PrefixDMOnly rejects message commands inside guilds.
func PrefixDMOnly() PrefixCheck {
	return func(ctx *PrefixContext) error {
		if ctx.Message != nil && ctx.Message.Message != nil && ctx.Message.GuildID != "" {
			return ErrDMOnly
		}
		return nil
	}
}

// PrefixHasPermissions resolves the invoking member's effective channel
// permissions and requires every requested bit.
func PrefixHasPermissions(required int64) PrefixCheck {
	return func(ctx *PrefixContext) error {
		if ctx.Session == nil || ctx.Message == nil || ctx.Message.Message == nil || ctx.Message.Author == nil {
			return fmt.Errorf("%w: required=%d available=0", ErrMissingPermissions, required)
		}
		available, err := ctx.Session.UserChannelPermissions(ctx.Message.Author.ID, ctx.Message.ChannelID)
		if err != nil {
			return fmt.Errorf("resolve prefix permissions: %w", err)
		}
		if available&dgo.PermissionAdministrator != 0 || available&required == required {
			return nil
		}
		return fmt.Errorf("%w: required=%d available=%d", ErrMissingPermissions, required, available)
	}
}

// PrefixCooldownKey selects a message-command cooldown bucket.
type PrefixCooldownKey func(*PrefixContext) string

// PrefixCooldownPerUser keys by message author.
func PrefixCooldownPerUser(ctx *PrefixContext) string {
	if user := ctx.User(); user != nil {
		return "user:" + user.ID
	}
	return "user:unknown"
}

// PrefixCooldownPerGuild keys by guild, falling back to DM channel.
func PrefixCooldownPerGuild(ctx *PrefixContext) string {
	if ctx.Message == nil || ctx.Message.Message == nil {
		return "guild:unknown"
	}
	if ctx.Message.GuildID != "" {
		return "guild:" + ctx.Message.GuildID
	}
	return "channel:" + ctx.Message.ChannelID
}

// PrefixCooldownGlobal uses a single bucket.
func PrefixCooldownGlobal(*PrefixContext) string { return "global" }

// PrefixCooldown is a concurrency-safe fixed-window cooldown.
type PrefixCooldown struct {
	limit  int
	period time.Duration
	key    PrefixCooldownKey

	mu        sync.Mutex
	buckets   map[string]cooldownBucket
	now       func() time.Time
	lastSweep time.Time
}

// NewPrefixCooldown permits limit invocations per period for each key.
func NewPrefixCooldown(limit int, period time.Duration, key PrefixCooldownKey) (*PrefixCooldown, error) {
	if limit <= 0 || period <= 0 {
		return nil, errors.New("cooldown limit and period must be positive")
	}
	if key == nil {
		key = PrefixCooldownPerUser
	}
	return &PrefixCooldown{
		limit: limit, period: period, key: key,
		buckets: make(map[string]cooldownBucket), now: time.Now,
	}, nil
}

// Check consumes one cooldown use.
func (c *PrefixCooldown) Check(ctx *PrefixContext) error {
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

// Reset clears one prefix cooldown bucket.
func (c *PrefixCooldown) Reset(key string) {
	c.mu.Lock()
	delete(c.buckets, key)
	c.mu.Unlock()
}

// ResetAll clears all prefix cooldown buckets.
func (c *PrefixCooldown) ResetAll() {
	c.mu.Lock()
	c.buckets = make(map[string]cooldownBucket)
	c.mu.Unlock()
}
