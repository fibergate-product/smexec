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

// TestMain doubles as the smexec entry point: re-executing the test binary with
// SMEXEC_TEST_CHILD set exercises the real main path, including syscall.Exec.
func TestMain(m *testing.M) {
	if os.Getenv("SMEXEC_TEST_CHILD") == "1" {
		os.Exit(run(os.Args[1:], os.Environ()))
	}
	os.Exit(m.Run())
}

type result struct {
	stdout, stderr string
	code           int
	pid            int
}

func child(t *testing.T, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append([]string{"SMEXEC_TEST_CHILD=1", "PATH=" + os.Getenv("PATH")}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("running child: %v", err)
	}
	return result{stdout.String(), stderr.String(), cmd.ProcessState.ExitCode(), cmd.Process.Pid}
}

// Inert with no secret IDs: no AWS call, no requirement check, so one image can
// serve compose, CI and the ECS debug task unmodified.
func TestInertWithoutSecretIDs(t *testing.T) {
	got := child(t, []string{"FOO=bar"}, "sh", "-c", "echo $FOO")
	if got.code != 0 || got.stdout != "bar\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("inert run should be silent, got %q", got.stderr)
	}
}

func TestInertSkipsRequireCheck(t *testing.T) {
	got := child(t, []string{"SMEXEC_REQUIRE=NEVER_SET"}, "sh", "-c", "echo ok")
	if got.code != 0 || got.stdout != "ok\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
	}
}

func TestEmptySecretIDsIsInert(t *testing.T) {
	got := child(t, []string{"SMEXEC_SECRET_IDS= , ,"}, "sh", "-c", "echo ok")
	if got.code != 0 || got.stdout != "ok\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
	}
}

// The target must replace smexec rather than run as its child, so it keeps
// PID 1 and owns the process's signals.
func TestExecReplacesProcess(t *testing.T) {
	got := child(t, nil, "sh", "-c", "echo $$")
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
	got := child(t, nil, "sh", "-c", "printf '%s|' \"$@\"", "sh", "one", "two three")
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
		name string
		env  []string
		args []string
		want int
	}{
		{"no command", nil, nil, 2},
		{"command not found", nil, []string{"smexec-no-such-command"}, 127},
		{"path does not exist", nil, []string{"/nonexistent/smexec-target"}, 127},
		{"not executable", nil, []string{notExecutable}, 126},
		{"bad timeout", []string{"SMEXEC_SECRET_IDS=x", "SMEXEC_TIMEOUT=soon"}, []string{"sh", "-c", "true"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := child(t, c.env, c.args...)
			if got.code != c.want {
				t.Fatalf("code=%d, want %d (stderr=%q)", got.code, c.want, got.stderr)
			}
			if got.stderr == "" {
				t.Errorf("failure should explain itself on stderr")
			}
		})
	}
}

func TestVersion(t *testing.T) {
	got := child(t, nil, "--version")
	if got.code != 0 || !strings.HasPrefix(got.stdout, "smexec ") {
		t.Fatalf("code=%d stdout=%q", got.code, got.stdout)
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
