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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func componentInteraction(customID string) *dgo.InteractionCreate {
	return &dgo.InteractionCreate{Interaction: &dgo.Interaction{
		Type: dgo.InteractionMessageComponent,
		Data: dgo.MessageComponentInteractionData{CustomID: customID},
	}}
}

func modalInteraction(customID string, components ...dgo.MessageComponent) *dgo.InteractionCreate {
	return &dgo.InteractionCreate{Interaction: &dgo.Interaction{
		Type: dgo.InteractionModalSubmit,
		Data: dgo.ModalSubmitInteractionData{CustomID: customID, Components: components},
	}}
}

func TestComponentRoutesPreferExactThenLongestPrefix(t *testing.T) {
	framework := newTestBot(t)
	var calls []string
	if err := framework.RegisterComponentPrefix("item:", func(ctx *ComponentContext) error {
		calls = append(calls, "short:"+ctx.Suffix)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := framework.RegisterComponentPrefix("item:admin:", func(ctx *ComponentContext) error {
		calls = append(calls, "long:"+ctx.Suffix)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := framework.RegisterComponent("item:admin:42", func(ctx *ComponentContext) error {
		calls = append(calls, "exact:"+ctx.Suffix)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if !framework.DispatchInteraction(context.Background(), componentInteraction("item:admin:42")) {
		t.Fatal("exact interaction was not dispatched")
	}
	framework.DispatchInteraction(context.Background(), componentInteraction("item:admin:7"))
	framework.DispatchInteraction(context.Background(), componentInteraction("item:user"))
	want := []string{"exact:", "long:7", "short:user"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestInteractionRouteRegistrationValidation(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.RegisterComponent("", func(*ComponentContext) error { return nil }); !errors.Is(err, ErrInvalidInteractionRoute) {
		t.Fatalf("empty route error = %v", err)
	}
	if err := framework.RegisterComponent("button", nil); !errors.Is(err, ErrInvalidInteractionRoute) {
		t.Fatalf("nil handler error = %v", err)
	}
	if err := framework.RegisterComponent("button", func(*ComponentContext) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := framework.RegisterComponent("button", func(*ComponentContext) error { return nil }); !errors.Is(err, ErrDuplicateInteractionRoute) {
		t.Fatalf("duplicate error = %v", err)
	}
	if framework.DispatchInteraction(context.Background(), componentInteraction("unknown")) {
		t.Fatal("unknown route was dispatched")
	}
}

func TestModalPrefixRouteAndTextField(t *testing.T) {
	framework := newTestBot(t)
	var gotSuffix, gotText string
	if err := framework.RegisterModalPrefix("edit:", func(ctx *ModalContext) error {
		gotSuffix = ctx.Suffix
		var err error
		gotText, err = ctx.Text("title")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	event := modalInteraction("edit:123", &dgo.ActionsRow{Components: []dgo.MessageComponent{
		&dgo.TextInput{CustomID: "title", Value: "New title"},
	}})
	if !framework.DispatchInteraction(context.Background(), event) {
		t.Fatal("modal was not dispatched")
	}
	if gotSuffix != "123" || gotText != "New title" {
		t.Fatalf("suffix/text = %q/%q", gotSuffix, gotText)
	}
}

func TestModalTextSupportsLabelComponents(t *testing.T) {
	ctx := &ModalContext{Data: dgo.ModalSubmitInteractionData{Components: []dgo.MessageComponent{
		&dgo.Label{Component: &dgo.TextInput{CustomID: "body", Value: "text"}},
	}}}
	if got, err := ctx.Text("body"); err != nil || got != "text" {
		t.Fatalf("Text = %q, %v", got, err)
	}
	if _, err := ctx.Text("missing"); !errors.Is(err, ErrOptionNotFound) {
		t.Fatalf("missing Text error = %v", err)
	}
}

func TestComponentErrorsAndPanicsUseInteractionErrorHandler(t *testing.T) {
	framework := newTestBot(t)
	var got []error
	framework.SetInteractionErrorHandler(func(_ *InteractionContext, err error) {
		got = append(got, err)
	})
	want := errors.New("failed")
	if err := framework.RegisterComponent("error", func(*ComponentContext) error { return want }); err != nil {
		t.Fatal(err)
	}
	if err := framework.RegisterComponent("panic", func(*ComponentContext) error { panic("boom") }); err != nil {
		t.Fatal(err)
	}
	framework.DispatchInteraction(context.Background(), componentInteraction("error"))
	framework.DispatchInteraction(context.Background(), componentInteraction("panic"))
	if len(got) != 2 || !errors.Is(got[0], want) {
		t.Fatalf("errors = %v", got)
	}
	var panicErr *PanicError
	if !errors.As(got[1], &panicErr) || panicErr.Value != "boom" {
		t.Fatalf("panic error = %#v", got[1])
	}
}

func TestComponentUpdateSendsUpdateResponse(t *testing.T) {
	framework := newTestBot(t)
	var response dgo.InteractionResponse
	framework.Session().Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&response); err != nil {
			t.Fatalf("decode response body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	if err := framework.RegisterComponent("update", func(ctx *ComponentContext) error {
		return ctx.Update(&dgo.InteractionResponseData{Content: "updated"})
	}); err != nil {
		t.Fatal(err)
	}
	event := componentInteraction("update")
	event.ID = "interaction"
	event.Token = "interaction-token"
	framework.DispatchInteraction(context.Background(), event)
	if response.Type != dgo.InteractionResponseUpdateMessage || response.Data == nil || response.Data.Content != "updated" {
		t.Fatalf("response = %#v", response)
	}
}
