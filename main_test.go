package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMain doubles as the smexec entry point: re-executing the test binary
// exercises the real code path, including syscall.Exec. SMEXEC_TEST_CHILD
// enters at run(), SMEXEC_TEST_EXEC at execute() - the latter so exec
// semantics can be tested without configuring secrets, which run() requires.
func TestMain(m *testing.M) {
	if os.Getenv("SMEXEC_TEST_CHILD") == "1" {
		os.Exit(run(os.Args[1:], os.Environ()))
	}
	if os.Getenv("SMEXEC_TEST_EXEC") == "1" {
		os.Exit(execute(os.Args[1:], os.Environ()))
	}
	os.Exit(m.Run())
}

type result struct {
	stdout, stderr string
	code           int
	pid            int
}

// child runs the full run() path.
func child(t *testing.T, env []string, args ...string) result {
	t.Helper()
	return spawn(t, "SMEXEC_TEST_CHILD=1", env, args...)
}

// execChild enters at execute(), skipping configuration entirely.
func execChild(t *testing.T, env []string, args ...string) result {
	t.Helper()
	return spawn(t, "SMEXEC_TEST_EXEC=1", env, args...)
}

func spawn(t *testing.T, entry string, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append([]string{entry, "PATH=" + os.Getenv("PATH")}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("running child: %v", err)
	}
	return result{stdout.String(), stderr.String(), cmd.ProcessState.ExitCode(), cmd.Process.Pid}
}

// Missing configuration is the quietest way to start a process with no
// secrets, so it must fail rather than pass the command through.
func TestMissingSecretIDsRaises(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"unset", nil, "SMEXEC_SECRET_IDS is not set"},
		{"empty", []string{"SMEXEC_SECRET_IDS="}, "SMEXEC_SECRET_IDS is not set"},
		{"blanks only", []string{"SMEXEC_SECRET_IDS= , ,"}, "contains no secret ids"},
		// The complaint is about the missing ids, not the unmet requirement.
		{"with SMEXEC_REQUIRE", []string{"SMEXEC_REQUIRE=APP_KEY"}, "SMEXEC_SECRET_IDS is not set"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := child(t, c.env, "sh", "-c", "echo COMMAND RAN")
			if got.code != 1 {
				t.Fatalf("code=%d, want 1 (stderr=%q)", got.code, got.stderr)
			}
			if got.stdout != "" {
				t.Fatalf("the command ran: %q", got.stdout)
			}
			if !strings.Contains(got.stderr, c.want) {
				t.Errorf("stderr=%q, want it to contain %q", got.stderr, c.want)
			}
		})
	}
}

// The target must replace smexec rather than run as its child, so it keeps
// PID 1 and owns the process's signals.
func TestExecReplacesProcess(t *testing.T) {
	got := execChild(t, nil, "sh", "-c", "echo $$")
	if got.code != 0 {
		t.Fatalf("code=%d stderr=%q", got.code, got.stderr)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(got.stdout))
	if err != nil {
		t.Fatal(err)
	}
	if pid != got.pid {
		t.Fatalf("child pid %d != smexec pid %d: the process was forked, not replaced", pid, got.pid)
	}
}

func TestArgumentsReachTheCommand(t *testing.T) {
	got := execChild(t, nil, "sh", "-c", "printf '%s|' \"$@\"", "sh", "one", "two three")
	if got.stdout != "one|two three|" {
		t.Fatalf("stdout=%q stderr=%q", got.stdout, got.stderr)
	}
}

func TestExitCodes(t *testing.T) {
	notExecutable := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(notExecutable, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		spawn func(*testing.T, []string, ...string) result
		env   []string
		args  []string
		want  int
	}{
		{"no command", child, nil, nil, 2},
		{"no secret ids", child, nil, []string{"sh", "-c", "true"}, 1},
		{"bad timeout", child, []string{"SMEXEC_SECRET_IDS=x", "SMEXEC_TIMEOUT=soon"}, []string{"sh", "-c", "true"}, 1},
		{"command not found", execChild, nil, []string{"smexec-no-such-command"}, 127},
		{"path does not exist", execChild, nil, []string{"/nonexistent/smexec-target"}, 127},
		{"not executable", execChild, nil, []string{notExecutable}, 126},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.spawn(t, c.env, c.args...)
			if got.code != c.want {
				t.Fatalf("code=%d, want %d (stderr=%q)", got.code, c.want, got.stderr)
			}
			if got.stderr == "" {
				t.Errorf("failure should explain itself on stderr")
			}
		})
	}
}

// --version is the one path that works with no configuration at all, so it
// stays usable for checking what an image baked.
func TestVersion(t *testing.T) {
	got := child(t, nil, "--version")
	if got.code != 0 || !strings.HasPrefix(got.stdout, "smexec ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
	}
}

func TestSplitList(t *testing.T) {
	got := splitList("  a , ,b,  ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("splitList = %q", got)
	}
	if splitList("") != nil {
		t.Fatal("empty string should yield no entries")
	}
}

func TestLookupEnvTakesLastDefinition(t *testing.T) {
	if got := lookupEnv([]string{"A=1", "B=2", "A=3"}, "A"); got != "3" {
		t.Fatalf("lookupEnv = %q, want 3", got)
	}
	if got := lookupEnv([]string{"AB=1"}, "A"); got != "" {
		t.Fatalf("lookupEnv matched a prefix: %q", got)
	}
}
