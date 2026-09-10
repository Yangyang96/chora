package speccoding

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidCheckCommand = errors.New("invalid check command")

// NormalizedCheckCommand is an observed Pi command reduced to argv and, when
// Pi emitted an explicit `cd <relative-directory> &&`, its repository-relative
// working directory. Callers must otherwise bind cwd from trusted event state.
type NormalizedCheckCommand struct {
	Argv                     []string
	WorkingDirectory         string
	ExplicitWorkingDirectory bool
}

// ParseCheckCommand parses one ordinary executable invocation. It supports
// spaces, single and double quotes, and backslash escapes, but deliberately
// rejects shell programs and shell composition syntax.
func ParseCheckCommand(text string) ([]string, error) {
	words, split, err := tokenizeCheckCommand(text, false)
	if err != nil {
		return nil, err
	}
	if split >= 0 {
		return nil, invalidCheck("command chaining is not supported")
	}
	if err := validateCheckArgv(words); err != nil {
		return nil, err
	}
	return append([]string(nil), words...), nil
}

// CanonicalCheckCommand renders argv without losing empty, quoted, spaced, or
// sensitive arguments. Parsing the result produces the original argv.
func CanonicalCheckCommand(argv []string) (string, error) {
	if err := validateCheckArgv(argv); err != nil {
		return "", err
	}
	quoted := make([]string, len(argv))
	for index, word := range argv {
		quoted[index] = quoteCheckWord(word)
	}
	return strings.Join(quoted, " "), nil
}

// NormalizeObservedCheckCommand parses an observed raw Pi bash command. A
// single generated `cd <safe-relative-directory> && command` prefix is allowed
// for attribution; arbitrary shell composition remains ambiguous and fails.
func NormalizeObservedCheckCommand(text string) (NormalizedCheckCommand, error) {
	return normalizeObservedCheckCommand(text, "")
}

// NormalizeObservedCheckCommandAtRoot additionally accepts one absolute cd
// operand when it is lexically inside the trusted, canonical Task root. The
// returned working directory is always Task-relative so event projection and
// the filesystem-backed observer hash the same authority without retaining a
// host path.
func NormalizeObservedCheckCommandAtRoot(text, taskRoot string) (NormalizedCheckCommand, error) {
	return normalizeObservedCheckCommand(text, taskRoot)
}

func normalizeObservedCheckCommand(text, taskRoot string) (NormalizedCheckCommand, error) {
	words, split, err := tokenizeCheckCommand(text, true)
	if err != nil {
		return NormalizedCheckCommand{}, err
	}
	if split < 0 {
		if err := validateCheckArgv(words); err != nil {
			return NormalizedCheckCommand{}, err
		}
		return NormalizedCheckCommand{Argv: append([]string(nil), words...)}, nil
	}
	if split != 2 || words[0] != "cd" {
		return NormalizedCheckCommand{}, invalidCheck("only an explicit 'cd <repository-relative directory> && command' observation can establish cwd")
	}
	directory := words[1]
	if filepath.IsAbs(directory) {
		directory, err = taskRelativeCheckDirectory(taskRoot, directory)
		if err != nil {
			return NormalizedCheckCommand{}, err
		}
	}
	if !safeCheckDirectory(directory) {
		return NormalizedCheckCommand{}, invalidCheck("observed working directory must be repository-relative or inside the proven Task root, and outside .git")
	}
	argv := words[split:]
	if err := validateCheckArgv(argv); err != nil {
		return NormalizedCheckCommand{}, err
	}
	return NormalizedCheckCommand{
		Argv: append([]string(nil), argv...), WorkingDirectory: directory, ExplicitWorkingDirectory: true,
	}, nil
}

func taskRelativeCheckDirectory(taskRoot, directory string) (string, error) {
	if taskRoot == "" || taskRoot == string(filepath.Separator) || !filepath.IsAbs(taskRoot) || filepath.Clean(taskRoot) != taskRoot ||
		!filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", invalidCheck("absolute observed working directory requires an exact proven Task root")
	}
	relative, err := filepath.Rel(taskRoot, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", invalidCheck("absolute observed working directory is outside the proven Task root")
	}
	relative = filepath.ToSlash(relative)
	if !safeCheckDirectory(relative) {
		return "", invalidCheck("absolute observed working directory is unsafe")
	}
	return relative, nil
}

func tokenizeCheckCommand(text string, allowObservedAnd bool) ([]string, int, error) {
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
		return nil, -1, invalidCheck("enter an executable and its arguments")
	}
	var words []string
	var current strings.Builder
	started := false
	quote := rune(0)
	escaped := false
	split := -1
	flush := func() {
		if started {
			words = append(words, current.String())
			current.Reset()
			started = false
		}
	}
	runes := []rune(text)
	for index, character := range runes {
		if character == 0 || unicode.IsControl(character) {
			return nil, -1, invalidCheck("control characters and newlines are not supported")
		}
		if escaped {
			current.WriteRune(character)
			started = true
			escaped = false
			continue
		}
		if quote == '\'' {
			if character == '\'' {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			started = true
			continue
		}
		if quote == '"' {
			switch character {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			case '$', '`':
				return nil, -1, invalidCheck("variable and command substitution are not supported")
			default:
				current.WriteRune(character)
			}
			started = true
			continue
		}
		switch character {
		case '\\':
			escaped = true
			started = true
		case '\'', '"':
			quote = character
			started = true
		case ' ', '\t':
			flush()
		case '$', '`':
			return nil, -1, invalidCheck("variable and command substitution are not supported")
		case '|', ';', '<', '>', '(', ')':
			return nil, -1, invalidCheck("pipes, chaining, redirection, and shell grouping are not supported")
		case '&':
			if index+1 >= len(runes) || runes[index+1] != '&' || !allowObservedAnd || split >= 0 {
				return nil, -1, invalidCheck("background execution and command chaining are not supported")
			}
			flush()
			split = len(words)
			runes[index+1] = ' '
		default:
			current.WriteRune(character)
			started = true
		}
	}
	if escaped {
		return nil, -1, invalidCheck("a trailing backslash must escape another character")
	}
	if quote != 0 {
		return nil, -1, invalidCheck("close the quoted argument")
	}
	flush()
	if split >= 0 && (split == 0 || split == len(words)) {
		return nil, -1, invalidCheck("both sides of the observed directory prefix are required")
	}
	return words, split, nil
}

func validateCheckArgv(argv []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return invalidCheck("enter an executable")
	}
	for _, argument := range argv {
		if !utf8.ValidString(argument) || strings.IndexFunc(argument, unicode.IsControl) >= 0 {
			return invalidCheck("arguments must be valid text without control characters")
		}
	}
	executable := argv[0]
	if strings.HasPrefix(executable, "/") || strings.Contains(executable, "\\") {
		return invalidCheck("the executable must be a PATH name or safe repository-relative path")
	}
	checkPath := executable
	if strings.HasPrefix(checkPath, "./") {
		checkPath = strings.TrimPrefix(checkPath, "./")
	}
	cleaned := path.Clean(checkPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != checkPath {
		return invalidCheck("the executable must be a PATH name or safe repository-relative path")
	}
	for _, component := range strings.Split(cleaned, "/") {
		if component == ".git" {
			return invalidCheck("the executable must be outside .git")
		}
	}
	if shellAssignment(executable) {
		return invalidCheck("environment assignments are not supported; use a repository script")
	}
	base := path.Base(executable)
	if base == "sh" || base == "bash" || base == "zsh" || base == "dash" || base == "ksh" || base == "eval" || base == "env" {
		return invalidCheck("shell and environment wrappers are not supported; use a repository script")
	}
	return nil
}

func shellAssignment(value string) bool {
	separator := strings.IndexByte(value, '=')
	if separator <= 0 {
		return false
	}
	for index, character := range value[:separator] {
		if !(character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || index > 0 && character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func safeCheckDirectory(value string) bool {
	if value == "." {
		return true
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || component == ".git" {
			return false
		}
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func quoteCheckWord(value string) string {
	if value == "" {
		return "''"
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_@%+=:,./-", character) {
			continue
		}
		return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
	}
	return value
}

func invalidCheck(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCheckCommand, reason)
}
