package main

import (
	"os"
	"path/filepath"
	"reflect"
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
