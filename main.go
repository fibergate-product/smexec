package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

const (
	envSecretIDs = "SMEXEC_SECRET_IDS"
	envRequire   = "SMEXEC_REQUIRE"
	envTimeout   = "SMEXEC_TIMEOUT"

	defaultTimeout = 5 * time.Second
)

// version is stamped at build time.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Environ()))
}

// run performs everything up to the point of no return. It only returns when
// the child could not be started: on success the process is replaced.
func run(args, env []string) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Println("smexec " + version)
		return 0
	}
	if len(args) == 0 {
		logf("usage: smexec <command> [args...]")
		return 2
	}

	ids := splitList(lookupEnv(env, envSecretIDs))
	if len(ids) == 0 {
		// Inert: no AWS call, and no SMEXEC_REQUIRE check either. This is what
		// lets one image serve compose, CI and the ECS debug task unmodified.
		return execute(args, env)
	}

	timeout := defaultTimeout
	if s := lookupEnv(env, envTimeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			logf("invalid %s: %v", envTimeout, err)
			return 1
		}
		timeout = d
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		logf("aws config: %v", err)
		return 1
	}

	// Fails closed: a fetch, parse or requirement failure must not start the
	// process with missing secrets.
	newEnv, err := resolve(ctx, secretsmanager.NewFromConfig(cfg), env, ids, splitList(lookupEnv(env, envRequire)))
	if err != nil {
		logf("%v", err)
		return 1
	}

	return execute(args, newEnv)
}

// execute replaces smexec with the target command, so it keeps PID 1 and
// Lambda's runtime loop and container signal handling behave as if smexec had
// never been there.
func execute(args, env []string) int {
	path, err := exec.LookPath(args[0])
	if err == nil {
		err = syscall.Exec(path, args, env)
	}
	logf("%v", err)
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return 127
	}
	return 126
}
