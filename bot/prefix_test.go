package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	dgo "github.com/darui3018823/discord.go"
)

func message(content string) *dgo.MessageCreate {
	return &dgo.MessageCreate{Message: &dgo.Message{
		ID: "message", ChannelID: "channel", GuildID: "guild",
		Content: content, Author: &dgo.User{ID: "user", Username: "tester"},
	}}
}

func TestParsePrefixArguments(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"ping", []string{"ping"}},
		{`say "hello world" 'and more'`, []string{"say", "hello world", "and more"}},
		{`say hello\ world`, []string{"say", "hello world"}},
		{`say "" tail`, []string{"say", "", "tail"}},
		{`say pre"mid dle"post`, []string{"say", "premid dlepost"}},
		{"say\t日本語", []string{"say", "日本語"}},
	}
	for _, test := range tests {
		got, err := parsePrefixArguments(test.input)
		if err != nil {
			t.Fatalf("parsePrefixArguments(%q): %v", test.input, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parsePrefixArguments(%q) = %v, want %v", test.input, got, test.want)
		}
	}
	for _, input := range []string{`say "unterminated`, `say dangling\`} {
		if _, err := parsePrefixArguments(input); !errors.Is(err, ErrPrefixParse) {
			t.Fatalf("parsePrefixArguments(%q) error = %v", input, err)
		}
	}
}

func TestPrefixDispatchComposesTreeScopesAndTypedArguments(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.SetPrefixes("!", "!!"); err != nil {
		t.Fatal(err)
	}
	var calls []string
	framework.AddPrefixChecks(func(*PrefixContext) error {
		calls = append(calls, "global-check")
		return nil
	})
	framework.UsePrefix(func(next PrefixHandler) PrefixHandler {
		return func(ctx *PrefixContext) error {
			calls = append(calls, "global-middleware")
			return next(ctx)
		}
	})

	leaf := TextCommand("ban", func(ctx *PrefixContext) error {
		calls = append(calls, "handler")
		if ctx.Prefix != "!!" || ctx.InvokedWith != "Adm" || !reflect.DeepEqual(ctx.CommandPath, []string{"admin", "ban"}) {
			t.Fatalf("context = %#v", ctx)
		}
		reason, err := ctx.String(0)
		if err != nil || reason != "spam links" {
			t.Fatalf("String = %q, %v", reason, err)
		}
		days, err := ctx.Integer(1)
		if err != nil || days != 7 {
			t.Fatalf("Integer = %d, %v", days, err)
		}
		return errors.New("handled failure")
	})
	leaf.AddAliases("b")
	leaf.AddPrefixChecks(func(*PrefixContext) error {
		calls = append(calls, "leaf-check")
		return nil
	})
	leaf.UsePrefix(func(next PrefixHandler) PrefixHandler {
		return func(ctx *PrefixContext) error {
			calls = append(calls, "leaf-middleware")
			return next(ctx)
		}
	})
	leaf.SetPrefixErrorHandler(func(_ *PrefixContext, err error) {
		calls = append(calls, "leaf-error:"+err.Error())
	})

	root := TextCommand("admin", nil)
	root.AddAliases("adm")
	root.AddPrefixChecks(func(*PrefixContext) error {
		calls = append(calls, "root-check")
		return nil
	})
	root.UsePrefix(func(next PrefixHandler) PrefixHandler {
		return func(ctx *PrefixContext) error {
			calls = append(calls, "root-middleware")
			return next(ctx)
		}
	})
	if err := root.AddSubcommands(leaf); err != nil {
		t.Fatal(err)
	}
	if err := framework.RegisterPrefixCommands(root); err != nil {
		t.Fatal(err)
	}
	if !framework.DispatchMessage(context.Background(), message(`!!Adm B "spam links" 7`)) {
		t.Fatal("prefix command was not dispatched")
	}
	want := []string{
		"global-check", "root-check", "leaf-check",
		"global-middleware", "root-middleware", "leaf-middleware",
		"handler", "leaf-error:handled failure",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestPrefixDispatchIgnoresUnknownAndBotMessages(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.SetPrefixes("!"); err != nil {
		t.Fatal(err)
	}
	if framework.DispatchMessage(context.Background(), message("hello")) {
		t.Fatal("unprefixed message was dispatched")
	}
	if framework.DispatchMessage(context.Background(), message("!unknown")) {
		t.Fatal("unknown command was dispatched")
	}
	botMessage := message("!known")
	botMessage.Author.Bot = true
	if framework.DispatchMessage(context.Background(), botMessage) {
		t.Fatal("bot-authored message was dispatched")
	}
}

func TestPrefixParserAndMissingSubcommandUseErrorHandler(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.SetPrefixes("!"); err != nil {
		t.Fatal(err)
	}
	root := TextCommand("group", nil)
	if err := root.AddSubcommands(TextCommand("child", func(*PrefixContext) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if err := framework.RegisterPrefixCommands(root); err != nil {
		t.Fatal(err)
	}
	var got []error
	framework.SetPrefixErrorHandler(func(_ *PrefixContext, err error) { got = append(got, err) })
	framework.DispatchMessage(context.Background(), message(`!group "unterminated`))
	framework.DispatchMessage(context.Background(), message("!group"))
	if len(got) != 2 || !errors.Is(got[0], ErrPrefixParse) || !errors.Is(got[1], ErrPrefixSubcommandRequired) {
		t.Fatalf("errors = %v", got)
	}
}

func TestRegisterPrefixCommandsRejectsAliasCollisionsAtomically(t *testing.T) {
	framework := newTestBot(t)
	first := TextCommand("first", func(*PrefixContext) error { return nil })
	first.AddAliases("shared")
	second := TextCommand("second", func(*PrefixContext) error { return nil })
	second.AddAliases("shared")
	err := framework.RegisterPrefixCommands(first, second)
	if !errors.Is(err, ErrDuplicatePrefixCommand) {
		t.Fatalf("error = %v", err)
	}
	if len(framework.PrefixCommands()) != 0 {
		t.Fatal("failed registration partially mutated router")
	}
}

func TestPrefixHandlerPanicUsesNearestErrorScope(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.SetPrefixes("!"); err != nil {
		t.Fatal(err)
	}
	command := TextCommand("panic", func(*PrefixContext) error { panic("boom") })
	var got error
	command.SetPrefixErrorHandler(func(_ *PrefixContext, err error) { got = err })
	if err := framework.RegisterPrefixCommands(command); err != nil {
		t.Fatal(err)
	}
	framework.DispatchMessage(context.Background(), message("!panic"))
	var panicErr *PanicError
	if !errors.As(got, &panicErr) || panicErr.Value != "boom" {
		t.Fatalf("panic error = %#v", got)
	}
}

func TestPrefixReplyUsesSafeMentions(t *testing.T) {
	framework := newTestBot(t)
	var sent dgo.MessageSend
	framework.Session().Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"id":"response","channel_id":"channel","content":"hello"}`)),
			Request:    request,
		}, nil
	})}
	ctx := &PrefixContext{Context: context.Background(), Session: framework.Session(), Message: message("!reply")}
	if _, err := ctx.Reply("hello"); err != nil {
		t.Fatal(err)
	}
	if sent.Content != "hello" || sent.AllowedMentions == nil || len(sent.AllowedMentions.Parse) != 0 {
		t.Fatalf("sent message = %#v", sent)
	}
}
