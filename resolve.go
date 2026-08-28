package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// resolve merges secret values into env and returns the environment for the
// child process, both in os.Environ form.
//
// First writer wins, at every level: an inherited variable is never replaced,
// and among secrets the earliest id to supply a key keeps it. Whatever set a
// variable - the Lambda environment block, ENV in the image, the shell - did so
// deliberately, so overriding one key for one function is just setting it there.
func resolve(ctx context.Context, client secretsClient, env, ids, require []string) ([]string, error) {
	values, order := splitEnv(env)

	secrets, err := fetchAll(ctx, client, ids)
	if err != nil {
		return nil, err
	}

	merged := make(map[string]string)
	for i, s := range secrets {
		for k, v := range s {
			if _, dup := merged[k]; dup {
				logf("key %s: already provided by an earlier secret, ignored copy in %s", k, ids[i])
				continue
			}
			merged[k] = v
		}
	}

	var shadowed []string
	for _, k := range slices.Sorted(maps.Keys(merged)) {
		if _, inherited := values[k]; inherited {
			shadowed = append(shadowed, k)
			continue
		}
		order = append(order, k)
		values[k] = merged[k]
	}

	logf("loaded %d keys from %d secret(s)", len(merged), len(ids))
	if len(shadowed) > 0 {
		// Names only. Surfaces leftover plaintext env vars during rollout, where
		// an inherited value silently outranking the secret is the likely bug.
		logf("kept inherited env over secret: %s", strings.Join(shadowed, ", "))
	}

	var missing []string
	for _, k := range require {
		if values[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("required keys not resolved: %s", strings.Join(missing, ", "))
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+values[k])
	}
	return out, nil
}

// splitEnv indexes an os.Environ slice, preserving the original ordering of
// keys so the child environment stays readable.
func splitEnv(env []string) (map[string]string, []string) {
	values := make(map[string]string, len(env))
	order := make([]string, 0, len(env))
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			continue
		}
		if _, dup := values[k]; !dup {
			order = append(order, k)
		}
		values[k] = v
	}
	return values, order
}

// lookupEnv reads one variable out of an os.Environ slice.
func lookupEnv(env []string, key string) string {
	prefix := key + "="
	val := ""
	for _, e := range env {
		if after, ok := strings.CutPrefix(e, prefix); ok {
			val = after
		}
	}
	return val
}

// splitList parses a comma-separated setting, trimming blanks.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
