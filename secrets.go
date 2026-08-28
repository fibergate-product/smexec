package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// secretsClient is the slice of the Secrets Manager API smexec needs, so tests
// can supply a fake.
type secretsClient interface {
	GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// fetchAll retrieves every secret concurrently and returns the parsed maps in
// the same order as ids, so the caller can merge them last-wins. Cold start is
// the entire runtime cost of this design, so the fetches must not be serial.
func fetchAll(ctx context.Context, client secretsClient, ids []string) ([]map[string]string, error) {
	out := make([]map[string]string, len(ids))
	errs := make([]error, len(ids))

	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = fetch(ctx, client, id)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("secret %q: %w", ids[i], err)
		}
	}
	return out, nil
}

func fetch(ctx context.Context, client secretsClient, id string) (map[string]string, error) {
	// SecretId is passed through verbatim - ARN or name, any case. Normalising
	// it here is exactly the bug that rules out chamber.
	res, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &id})
	if err != nil {
		return nil, err
	}
	if res.SecretString == nil {
		return nil, errors.New("has no SecretString (binary secrets are not supported)")
	}
	return parseSecret(id, *res.SecretString)
}

// parseSecret decodes a secret payload as a flat JSON object of scalars. It
// walks the object as a token stream rather than unmarshalling into a map,
// because a map keeps the last of two duplicate keys and smexec keeps the
// first, matching how it merges across secrets.
func parseSecret(id, payload string) (map[string]string, error) {
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()

	if tok, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("is not a JSON object: %w", err)
	} else if tok != json.Delim('{') {
		return nil, errors.New("is not a JSON object")
	}

	out := make(map[string]string)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("is not a JSON object: %w", err)
		}
		k := tok.(string) // JSON object keys are always strings

		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		if err := checkKey(k); err != nil {
			return nil, err
		}
		val, err := scalar(v)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		// execve would reject this with a bare EINVAL.
		if strings.ContainsRune(val, 0) {
			return nil, fmt.Errorf("key %q: value contains NUL", k)
		}

		// Every occurrence is validated, but only the first is kept.
		if _, dup := out[k]; dup {
			logf("key %s: duplicated in secret %s, kept the first", k, id)
			continue
		}
		out[k] = val
	}

	if _, err := dec.Token(); err != nil { // closing brace
		return nil, fmt.Errorf("is not a JSON object: %w", err)
	}
	if dec.More() {
		return nil, errors.New("is not a JSON object: trailing data")
	}
	return out, nil
}

// scalar renders a JSON scalar as an environment variable value. Non-scalars
// are an error rather than an empty string - chamber's silent footgun.
func scalar(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		// Raw token text, so 1e6 stays 1e6.
		return t.String(), nil
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "", errors.New("value is null")
	default:
		return "", errors.New("value is not a scalar")
	}
}

// checkKey rejects names that cannot survive the execve wire format: envp is an
// array of NUL-terminated KEY=VALUE strings with no escaping. NUL fails the exec
// outright, while '=' silently shifts into the value, so a key like "PATH=/evil"
// would displace an inherited variable smexec has promised never to replace.
// Anything else execve accepts stays allowed, including names no shell could set.
func checkKey(k string) error {
	if k == "" {
		return errors.New("empty key name")
	}
	if strings.ContainsAny(k, "=\x00") {
		return fmt.Errorf("key %q contains '=' or NUL", k)
	}
	return nil
}
