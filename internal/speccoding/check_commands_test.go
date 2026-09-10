package speccoding

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseAndCanonicalCheckCommandRoundTrip(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{`npm run 'unit tests' -- --name "sign in"`, []string{"npm", "run", "unit tests", "--", "--name", "sign in"}},
		{`./scripts/check path\ with\ spaces "quoted value" ''`, []string{"./scripts/check", "path with spaces", "quoted value", ""}},
		{`custom-check '--token=sword|fish' '$(literal)'`, []string{"custom-check", "--token=sword|fish", "$(literal)"}},
		{`custom-check "it's literal"`, []string{"custom-check", "it's literal"}},
	}
	for _, test := range tests {
		argv, err := ParseCheckCommand(test.text)
		if err != nil {
			t.Fatalf("ParseCheckCommand(%q): %v", test.text, err)
		}
		if !reflect.DeepEqual(argv, test.want) {
			t.Fatalf("ParseCheckCommand(%q)=%#v want %#v", test.text, argv, test.want)
		}
		canonical, err := CanonicalCheckCommand(argv)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, err := ParseCheckCommand(canonical)
		if err != nil || !reflect.DeepEqual(roundTrip, argv) {
			t.Fatalf("canonical roundtrip %q = %#v, %v", canonical, roundTrip, err)
		}
	}
}

func TestParseCheckCommandRejectsShellSyntax(t *testing.T) {
	tests := []struct {
		text   string
		reason string
	}{
		{"go test ./... | tee out", "pipes"},
		{"go test ./... && npm test", "chaining"},
		{"go test > result", "redirection"},
		{"echo $(whoami)", "substitution"},
		{"echo `whoami`", "substitution"},
		{"TOKEN=value go test", "environment assignments"},
		{"sh -c 'go test; curl example.test'", "shell"},
		{"/usr/bin/go test ./...", "PATH name"},
		{"../bin/check", "repository-relative"},
	}
	for _, test := range tests {
		_, err := ParseCheckCommand(test.text)
		if !errors.Is(err, ErrInvalidCheckCommand) || !strings.Contains(err.Error(), test.reason) {
			t.Fatalf("ParseCheckCommand(%q) error=%v, want reason %q", test.text, err, test.reason)
		}
	}
}

func TestNormalizeObservedCheckCommand(t *testing.T) {
	observed, err := NormalizeObservedCheckCommand(`cd 'web/client tests' && npm run "unit tests"`)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.ExplicitWorkingDirectory || observed.WorkingDirectory != "web/client tests" || !reflect.DeepEqual(observed.Argv, []string{"npm", "run", "unit tests"}) {
		t.Fatalf("observed=%+v", observed)
	}
	plain, err := NormalizeObservedCheckCommand("go test ./...")
	if err != nil || plain.ExplicitWorkingDirectory || plain.WorkingDirectory != "" || !reflect.DeepEqual(plain.Argv, []string{"go", "test", "./..."}) {
		t.Fatalf("plain=%+v err=%v", plain, err)
	}
	for _, ambiguous := range []string{"cd one && cmd && other", "cd ../escape && go test", "echo one | cat", "cd one; go test"} {
		if _, err := NormalizeObservedCheckCommand(ambiguous); !errors.Is(err, ErrInvalidCheckCommand) {
			t.Fatalf("NormalizeObservedCheckCommand(%q) error=%v", ambiguous, err)
		}
	}
}

func TestNormalizeObservedCheckCommandAtRoot(t *testing.T) {
	root := "/private/Task root"
	commands := []string{
		`cd '/private/Task root/0123456789ab/client tests' && npm run "unit tests"`,
		`cd "/private/Task root/0123456789ab/client tests" && npm run 'unit tests'`,
	}
	for _, command := range commands {
		observed, err := NormalizeObservedCheckCommandAtRoot(command, root)
		if err != nil {
			t.Fatalf("NormalizeObservedCheckCommandAtRoot(%q): %v", command, err)
		}
		if !observed.ExplicitWorkingDirectory || observed.WorkingDirectory != "0123456789ab/client tests" || !reflect.DeepEqual(observed.Argv, []string{"npm", "run", "unit tests"}) {
			t.Fatalf("observed=%+v", observed)
		}
	}

	for _, test := range []struct {
		name    string
		root    string
		command string
	}{
		{name: "without trusted root", command: `cd '/private/Task root/repo' && npm test`},
		{name: "sibling", root: root, command: `cd '/private/Task root-other/repo' && npm test`},
		{name: "parent spelling", root: root, command: `cd '/private/Task root/repo/../other' && npm test`},
		{name: "root authority", root: "/", command: `cd '/private/Task root/repo' && npm test`},
		{name: "multiple operations", root: root, command: `cd '/private/Task root/repo' && npm test && npm lint`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeObservedCheckCommandAtRoot(test.command, test.root); !errors.Is(err, ErrInvalidCheckCommand) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
