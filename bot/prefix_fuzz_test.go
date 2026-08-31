package bot

import (
	"testing"
	"unicode/utf8"
)

func FuzzParsePrefixArguments(f *testing.F) {
	for _, seed := range []string{
		`ping`,
		`say "hello world" tail`,
		`command escaped\ space`,
		`nested '日本語 value' 42`,
		`unterminated "quote`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 || !utf8.ValidString(input) {
			t.Skip()
		}
		arguments, err := parsePrefixArguments(input)
		if err != nil {
			return
		}
		for _, argument := range arguments {
			if len(argument) > len(input) {
				t.Fatalf("argument length %d exceeds input length %d", len(argument), len(input))
			}
		}
	})
}
