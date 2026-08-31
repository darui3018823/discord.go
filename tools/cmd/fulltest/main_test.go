package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEnvironmentWithoutRemovesLiveTokenCaseInsensitively(t *testing.T) {
	environment := []string{
		"PATH=/bin",
		"test_bot_token=secret",
		"TEST_BOT_TOKEN=also-secret",
		"OTHER=value",
	}
	want := []string{"PATH=/bin", "OTHER=value"}
	if got := environmentWithout(environment, liveTokenEnvironment); !reflect.DeepEqual(got, want) {
		t.Fatalf("environmentWithout() = %v, want %v", got, want)
	}
}

func TestCheckFormattingAcceptsCRLF(t *testing.T) {
	directory := t.TempDir()
	formattedCRLF := []byte("package sample\r\n\r\nfunc Value() int {\r\n\treturn 1\r\n}\r\n")
	if err := os.WriteFile(filepath.Join(directory, "sample.go"), formattedCRLF, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkFormatting(directory); err != nil {
		t.Fatalf("checkFormatting() rejected formatted CRLF: %v", err)
	}
}

func TestCheckFormattingRejectsUnformattedGo(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "sample.go"), []byte("package sample\nfunc Value( )int{return 1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkFormatting(directory); err == nil {
		t.Fatal("checkFormatting() accepted unformatted Go")
	}
}

func TestConfigureInteractiveOfflineClearsLiveEnvironment(t *testing.T) {
	t.Setenv(liveTokenEnvironment, "secret")
	t.Setenv(liveGuildEnvironment, "123")
	t.Setenv(liveVoiceEnvironment, "456")
	var output bytes.Buffer
	if err := configureInteractive(strings.NewReader("\n"), &output); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{liveTokenEnvironment, liveGuildEnvironment, liveVoiceEnvironment} {
		if value := os.Getenv(key); value != "" {
			t.Fatalf("%s remained configured", key)
		}
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("interactive output exposed the token")
	}
}

func TestConfigureInteractiveLiveSetsResourceIDs(t *testing.T) {
	t.Setenv(liveTokenEnvironment, "secret")
	t.Setenv(liveGuildEnvironment, "")
	t.Setenv(liveVoiceEnvironment, "")
	var output bytes.Buffer
	if err := configureInteractive(strings.NewReader("yes\n123\n456\n"), &output); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(liveGuildEnvironment); got != "123" {
		t.Fatalf("guild ID = %q, want 123", got)
	}
	if got := os.Getenv(liveVoiceEnvironment); got != "456" {
		t.Fatalf("Voice channel ID = %q, want 456", got)
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("interactive output exposed the token")
	}
}

func TestConfigureInteractiveLiveRequiresToken(t *testing.T) {
	t.Setenv(liveTokenEnvironment, "")
	var output bytes.Buffer
	err := configureInteractive(strings.NewReader("yes\n"), &output)
	if err == nil || !strings.Contains(err.Error(), liveTokenEnvironment) {
		t.Fatalf("configureInteractive() error = %v", err)
	}
}

func TestValidateDiscordID(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "not-an-id"} {
		if err := validateDiscordID("id", value); err == nil {
			t.Fatalf("validateDiscordID(%q) succeeded", value)
		}
	}
	if err := validateDiscordID("id", "1472567651086893238"); err != nil {
		t.Fatal(err)
	}
}
