// Command fulltest runs the repository's contributor verification suite.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	liveTokenEnvironment = "test_bot_token"
	fullTestTimeout      = 30 * time.Minute
)

type module struct {
	name            string
	path            string
	minimumCoverage float64
}

var modules = []module{
	{name: "root", path: ".", minimumCoverage: 31},
	{name: "linked_roles", path: "examples/linked_roles", minimumCoverage: 60},
	{name: "voice_receive", path: "examples/voice_receive", minimumCoverage: 40},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nfulltest FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nfulltest PASSED")
}

func run() error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), fullTestTimeout)
	defer cancel()

	liveEnabled := os.Getenv(liveTokenEnvironment) != ""
	if liveEnabled {
		fmt.Printf("Live Discord E2E: enabled by %s (value hidden)\n", liveTokenEnvironment)
	} else {
		fmt.Printf("Live Discord E2E: skipped; %s is not set\n", liveTokenEnvironment)
	}

	if err := checkFormatting(root); err != nil {
		return err
	}

	offlineEnvironment := environmentWithout(os.Environ(), liveTokenEnvironment)
	for _, item := range modules {
		directory := filepath.Join(root, filepath.FromSlash(item.path))
		if err := runCommand(ctx, item.name+" race tests", directory, offlineEnvironment, "go", "test", "-race", "./..."); err != nil {
			return err
		}
		if err := runCommand(ctx, item.name+" vet", directory, offlineEnvironment, "go", "vet", "./..."); err != nil {
			return err
		}
	}

	temporaryDirectory, err := os.MkdirTemp("", "discord-go-fulltest-")
	if err != nil {
		return fmt.Errorf("create coverage directory: %w", err)
	}
	defer os.RemoveAll(temporaryDirectory)
	for _, item := range modules {
		directory := filepath.Join(root, filepath.FromSlash(item.path))
		profile := filepath.Join(temporaryDirectory, item.name+".out")
		if err := runCommand(ctx, item.name+" coverage", directory, offlineEnvironment, "go", "test", "-coverprofile", profile, "./..."); err != nil {
			return err
		}
		coverage, err := coverageTotal(ctx, directory, offlineEnvironment, profile)
		if err != nil {
			return fmt.Errorf("%s coverage: %w", item.name, err)
		}
		fmt.Printf("coverage: %.1f%% (minimum %.1f%%)\n", coverage, item.minimumCoverage)
		if coverage < item.minimumCoverage {
			return fmt.Errorf("%s statement coverage %.1f%% is below %.1f%%", item.name, coverage, item.minimumCoverage)
		}
	}

	python, err := pythonCommand()
	if err != nil {
		return err
	}
	if err := runCommand(ctx, "strict documentation build", root, offlineEnvironment, python, "-m", "mkdocs", "build", "--clean", "--strict", "--site-dir", "site"); err != nil {
		return fmt.Errorf("%w (install with: %s -m pip install -r requirements-docs.txt)", err, python)
	}

	if liveEnabled {
		if err := runCommand(ctx, "live Discord connectivity", root, os.Environ(), "go", "test", "-count=1", "-v", "./e2e"); err != nil {
			return err
		}
	}
	return nil
}

func repositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		contents, readErr := os.ReadFile(filepath.Join(directory, "go.mod"))
		if readErr == nil && bytes.Contains(contents, []byte("module github.com/darui3018823/discord.go")) {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("run fulltest from inside the discord.go repository")
		}
		directory = parent
	}
}

func checkFormatting(root string) error {
	fmt.Println("\n==> Go formatting")
	var unformatted []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "site", "vendor":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Git may check files out with CRLF on Windows while go/format always
		// emits LF. Newline policy is enforced by the repository attributes;
		// this check should report semantic gofmt differences only.
		normalized := bytes.ReplaceAll(contents, []byte("\r\n"), []byte("\n"))
		formatted, err := format.Source(normalized)
		if err != nil {
			return fmt.Errorf("format %s: %w", path, err)
		}
		if !bytes.Equal(normalized, formatted) {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			unformatted = append(unformatted, relative)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("check Go formatting: %w", err)
	}
	if len(unformatted) != 0 {
		return fmt.Errorf("gofmt required for: %s", strings.Join(unformatted, ", "))
	}
	fmt.Println("PASS")
	return nil
}

func runCommand(ctx context.Context, name, directory string, environment []string, executable string, arguments ...string) error {
	fmt.Printf("\n==> %s\n", name)
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	command.Env = environment
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", name, ctxErr)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func coverageTotal(ctx context.Context, directory string, environment []string, profile string) (float64, error) {
	command := exec.CommandContext(ctx, "go", "tool", "cover", "-func", profile)
	command.Dir = directory
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && fields[0] == "total:" {
			return strconv.ParseFloat(strings.TrimSuffix(fields[2], "%"), 64)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("go tool cover did not report a total")
}

func environmentWithout(environment []string, key string) []string {
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if found && strings.EqualFold(name, key) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func pythonCommand() (string, error) {
	for _, candidate := range []string{"python", "python3"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("Python was not found; install Python and requirements-docs.txt")
}
