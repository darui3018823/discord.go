package bot

import (
	"bytes"
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

func TestPlanCommandSyncIgnoresServerMetadataAndDefaults(t *testing.T) {
	defaultPermission := true
	desired := []*dgo.ApplicationCommand{{
		Type: dgo.ChatApplicationCommand, Name: "ping", Description: "Ping",
	}}
	remote := []*dgo.ApplicationCommand{{
		ID: "1", ApplicationID: "app", GuildID: "guild", Version: "9",
		Type: dgo.ChatApplicationCommand, Name: "ping", Description: "Ping",
		DefaultPermission: &defaultPermission, Options: []*dgo.ApplicationCommandOption{},
	}, {
		ID: "2", Type: dgo.ChatApplicationCommand, Name: "stale", Description: "Stale",
	}}
	changes, err := PlanCommandSync(desired, remote, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].Action != CommandSyncUnchanged || changes[1].Action != CommandSyncDelete {
		t.Fatalf("changes = %#v", changes)
	}
	if remote[0].Options == nil || remote[0].ID != "1" {
		t.Fatal("planning mutated a remote command")
	}
	changes, err = PlanCommandSync(desired, remote, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Action != CommandSyncUnchanged {
		t.Fatalf("non-deleting changes = %#v", changes)
	}
}

func TestPlanCommandSyncCreatesUpdatesAndValidatesDuplicates(t *testing.T) {
	desired := []*dgo.ApplicationCommand{
		{Type: dgo.ChatApplicationCommand, Name: "alpha", Description: "New"},
		{Type: dgo.UserApplicationCommand, Name: "Inspect"},
	}
	remote := []*dgo.ApplicationCommand{{ID: "alpha", Type: dgo.ChatApplicationCommand, Name: "alpha", Description: "Old"}}
	changes, err := PlanCommandSync(desired, remote, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := []CommandSyncAction{changes[0].Action, changes[1].Action}; !reflect.DeepEqual(got, []CommandSyncAction{CommandSyncUpdate, CommandSyncCreate}) {
		t.Fatalf("actions = %v", got)
	}
	_, err = PlanCommandSync(desired, append(remote, remote[0]), true)
	if !errors.Is(err, ErrDuplicateRemoteCommand) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestSyncCommandDiffAppliesOnlyChanges(t *testing.T) {
	framework := newTestBot(t)
	for _, command := range []*Command{
		Slash("ping", "Ping", func(*Context) error { return nil }),
		Slash("echo", "New echo", func(*Context) error { return nil }),
		Slash("new", "New command", func(*Context) error { return nil }),
	} {
		if err := framework.Register(command); err != nil {
			t.Fatal(err)
		}
	}
	remote := []*dgo.ApplicationCommand{
		{ID: "echo-id", Type: dgo.ChatApplicationCommand, Name: "echo", Description: "Old echo"},
		{ID: "ping-id", Type: dgo.ChatApplicationCommand, Name: "ping", Description: "Ping"},
		{ID: "stale-id", Type: dgo.ChatApplicationCommand, Name: "stale", Description: "Stale"},
	}
	var mutations []string
	framework.Session().Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.Method {
		case http.MethodGet:
			return commandJSONResponse(t, remote), nil
		case http.MethodPost, http.MethodPatch:
			var command dgo.ApplicationCommand
			if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
				t.Fatal(err)
			}
			mutations = append(mutations, request.Method+":"+command.Name)
			command.ID = command.Name + "-id"
			return commandJSONResponse(t, &command), nil
		case http.MethodDelete:
			parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
			mutations = append(mutations, request.Method+":"+parts[len(parts)-1])
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
		default:
			t.Fatalf("unexpected method %s", request.Method)
			return nil, nil
		}
	})}

	report, err := framework.SyncCommandDiff(context.Background(), "app", "guild")
	if err != nil {
		t.Fatal(err)
	}
	wantMutations := []string{"PATCH:echo", "POST:new", "DELETE:stale-id"}
	if !reflect.DeepEqual(mutations, wantMutations) {
		t.Fatalf("mutations = %v, want %v", mutations, wantMutations)
	}
	if len(report.Changes) != 4 || len(report.Created) != 1 || len(report.Updated) != 1 || len(report.Deleted) != 1 || len(report.Unchanged) != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestSyncCommandDiffDryRunDoesNotMutate(t *testing.T) {
	framework := newTestBot(t)
	if err := framework.Register(Slash("local", "Local", func(*Context) error { return nil })); err != nil {
		t.Fatal(err)
	}
	framework.Session().Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Fatalf("dry run sent %s", request.Method)
		}
		return commandJSONResponse(t, []*dgo.ApplicationCommand{}), nil
	})}
	report, err := framework.SyncCommandDiff(context.Background(), "app", "", WithCommandSyncDryRun(true))
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || len(report.Changes) != 1 || report.Changes[0].Action != CommandSyncCreate || len(report.Created) != 0 {
		t.Fatalf("dry-run report = %#v", report)
	}
}

func commandJSONResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
