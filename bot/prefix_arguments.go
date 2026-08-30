package bot

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dgo "github.com/darui3018823/discord.go"
)

// ErrPrefixArgument is wrapped by PrefixArgumentError for missing or invalid
// positional arguments.
var ErrPrefixArgument = errors.New("invalid prefix command argument")

// PrefixArgumentError describes a failed positional conversion.
type PrefixArgumentError struct {
	Index int
	Value string
	Type  string
	Err   error
}

func (e *PrefixArgumentError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%v %d (%s): %v", ErrPrefixArgument, e.Index, e.Type, e.Err)
	}
	return fmt.Sprintf("%v %d (%s)", ErrPrefixArgument, e.Index, e.Type)
}

func (e *PrefixArgumentError) Unwrap() error { return ErrPrefixArgument }

// String returns one positional argument.
func (c *PrefixContext) String(index int) (string, error) {
	if index < 0 || index >= len(c.Args) {
		return "", &PrefixArgumentError{Index: index, Type: "string", Err: errors.New("argument is missing")}
	}
	return c.Args[index], nil
}

// Rest joins the remaining arguments with a single space.
func (c *PrefixContext) Rest(index int) (string, error) {
	if index < 0 || index >= len(c.Args) {
		return "", &PrefixArgumentError{Index: index, Type: "rest", Err: errors.New("argument is missing")}
	}
	return strings.Join(c.Args[index:], " "), nil
}

// Integer parses one signed decimal integer.
func (c *PrefixContext) Integer(index int) (int64, error) {
	value, err := c.String(index)
	if err != nil {
		return 0, err
	}
	parsed, parseErr := strconv.ParseInt(value, 10, 64)
	if parseErr != nil {
		return 0, &PrefixArgumentError{Index: index, Value: value, Type: "integer", Err: parseErr}
	}
	return parsed, nil
}

// Number parses one floating-point number.
func (c *PrefixContext) Number(index int) (float64, error) {
	value, err := c.String(index)
	if err != nil {
		return 0, err
	}
	parsed, parseErr := strconv.ParseFloat(value, 64)
	if parseErr != nil {
		return 0, &PrefixArgumentError{Index: index, Value: value, Type: "number", Err: parseErr}
	}
	return parsed, nil
}

// Boolean parses common true/false spellings accepted by strconv.ParseBool.
func (c *PrefixContext) Boolean(index int) (bool, error) {
	value, err := c.String(index)
	if err != nil {
		return false, err
	}
	parsed, parseErr := strconv.ParseBool(value)
	if parseErr != nil {
		return false, &PrefixArgumentError{Index: index, Value: value, Type: "boolean", Err: parseErr}
	}
	return parsed, nil
}

// Duration parses a Go duration such as "500ms" or "2h45m".
func (c *PrefixContext) Duration(index int) (time.Duration, error) {
	value, err := c.String(index)
	if err != nil {
		return 0, err
	}
	parsed, parseErr := time.ParseDuration(value)
	if parseErr != nil {
		return 0, &PrefixArgumentError{Index: index, Value: value, Type: "duration", Err: parseErr}
	}
	return parsed, nil
}

// UserID parses a raw snowflake or user mention.
func (c *PrefixContext) UserID(index int) (string, error) {
	return c.snowflake(index, "user", "<@", ">")
}

// RoleID parses a raw snowflake or role mention.
func (c *PrefixContext) RoleID(index int) (string, error) {
	return c.snowflake(index, "role", "<@&", ">")
}

// ChannelID parses a raw snowflake or channel mention.
func (c *PrefixContext) ChannelID(index int) (string, error) {
	return c.snowflake(index, "channel", "<#", ">")
}

// User returns an ID-only user parsed from a raw snowflake or mention.
func (c *PrefixContext) UserArgument(index int) (*dgo.User, error) {
	id, err := c.UserID(index)
	if err != nil {
		return nil, err
	}
	return &dgo.User{ID: id}, nil
}

// Role returns an ID-only role parsed from a raw snowflake or mention.
func (c *PrefixContext) Role(index int) (*dgo.Role, error) {
	id, err := c.RoleID(index)
	if err != nil {
		return nil, err
	}
	return &dgo.Role{ID: id}, nil
}

// Channel returns an ID-only channel parsed from a raw snowflake or mention.
func (c *PrefixContext) Channel(index int) (*dgo.Channel, error) {
	id, err := c.ChannelID(index)
	if err != nil {
		return nil, err
	}
	return &dgo.Channel{ID: id}, nil
}

func (c *PrefixContext) snowflake(index int, typeName, prefix, suffix string) (string, error) {
	value, err := c.String(index)
	if err != nil {
		return "", err
	}
	id := value
	if typeName == "user" && strings.HasPrefix(id, "<@!") && strings.HasSuffix(id, suffix) {
		id = strings.TrimSuffix(strings.TrimPrefix(id, "<@!"), suffix)
	} else if strings.HasPrefix(id, prefix) && strings.HasSuffix(id, suffix) {
		id = strings.TrimSuffix(strings.TrimPrefix(id, prefix), suffix)
	}
	if _, parseErr := strconv.ParseUint(id, 10, 64); parseErr != nil || id == "0" {
		if parseErr == nil {
			parseErr = errors.New("snowflake must be positive")
		}
		return "", &PrefixArgumentError{Index: index, Value: value, Type: typeName, Err: parseErr}
	}
	return id, nil
}
