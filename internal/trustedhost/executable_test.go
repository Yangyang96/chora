package trustedhost

import "testing"

func TestValidateAllowedArgumentsTrailingRoot(t *testing.T) {
	expected := []string{"--mode", "rpc", "--session-dir"}
	root := "/sessions"

	if err := validateAllowedArguments([]string{"--mode", "rpc", "--session-dir", "/sessions/attempt-1"}, expected, root); err != nil {
		t.Fatalf("inside-root trailing path rejected: %v", err)
	}
	if err := validateAllowedArguments([]string{"--mode", "rpc", "--session-dir", "/sessions/attempt-1", "--session", "123e4567-e89b-12d3-a456-426614174000"}, expected, root); err != nil {
		t.Fatalf("valid explicit session rejected: %v", err)
	}
	if err := validateAllowedArguments([]string{"--mode", "rpc", "--session-dir", "/sessions/attempt-1", "--session", "bad"}, expected, root); err == nil {
		t.Fatal("invalid explicit session accepted")
	}
	if err := validateAllowedArguments([]string{"--mode", "rpc", "--session-dir", "/sessions/attempt-1", "--no-approve", "123e4567-e89b-12d3-a456-426614174000"}, expected, root); err == nil {
		t.Fatal("permission flag accepted")
	}
	if err := validateAllowedArguments([]string{"--mode", "rpc", "--session-dir", "/etc/passwd"}, expected, root); err == nil {
		t.Fatal("outside-root trailing path accepted")
	}
	if err := validateAllowedArguments([]string{"--mode", "rpc", "--no-session"}, expected, root); err == nil {
		t.Fatal("wrong prefix accepted")
	}
	if err := validateAllowedArguments([]string{"--mode", "rpc", "--session-dir", "/sessions/a", "extra"}, expected, root); err == nil {
		t.Fatal("extra trailing argument accepted")
	}
	if err := validateAllowedArguments([]string{"--mode", "rpc", "--session-dir", "relative"}, expected, root); err == nil {
		t.Fatal("relative trailing path accepted")
	}
}

func TestValidateAllowedArgumentsExactWhenNoRoot(t *testing.T) {
	if err := validateAllowedArguments([]string{"--mode", "rpc"}, []string{"--mode", "rpc"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := validateAllowedArguments([]string{"--mode", "rpc", "--session-dir", "/x"}, []string{"--mode", "rpc"}, ""); err == nil {
		t.Fatal("trailing path accepted without a declared root")
	}
}

func TestValidateAllowedArgumentsOnlyAddsPinnedReadOnlyObserver(t *testing.T) {
	base := []string{"--mode", "rpc", "--session-dir"}
	observer := "/private/observer.mjs"
	for _, args := range [][]string{
		{"--mode", "rpc", "--session-dir", "/sessions/task"},
		{"--mode", "rpc", "--extension", observer, "--session-dir", "/sessions/task"},
		{"--mode", "rpc", "--extension", observer, "--session-dir", "/sessions/task", "--session", "123e4567-e89b-12d3-a456-426614174000"},
	} {
		if err := validateAllowedArguments(args, base, "/sessions", observer); err != nil {
			t.Fatalf("allowed observer args rejected: %v", err)
		}
	}
	for _, args := range [][]string{
		{"--mode", "rpc", "--extension", "/other.mjs", "--session-dir", "/sessions/task"},
		{"--mode", "rpc", "--extension", observer, "--extension", observer, "--session-dir", "/sessions/task"},
		{"--mode", "rpc", "--no-extensions", "--extension", observer, "--session-dir", "/sessions/task"},
	} {
		if err := validateAllowedArguments(args, base, "/sessions", observer); err == nil {
			t.Fatalf("unapproved native args accepted: %v", args)
		}
	}
}
