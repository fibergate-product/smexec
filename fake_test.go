package main

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// fakeClient records every SecretId it is asked for, so tests can assert the
// identifier is passed through untouched.
type fakeClient struct {
	mu    sync.Mutex
	seen  []string
	reply func(id string) (*secretsmanager.GetSecretValueOutput, error)
}

func (f *fakeClient) GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.seen = append(f.seen, *in.SecretId)
	f.mu.Unlock()
	return f.reply(*in.SecretId)
}

func (f *fakeClient) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// stringSecrets serves the given id -> SecretString payloads.
func stringSecrets(m map[string]string) *fakeClient {
	return &fakeClient{reply: func(id string) (*secretsmanager.GetSecretValueOutput, error) {
		s, ok := m[id]
		if !ok {
			return nil, fmt.Errorf("ResourceNotFoundException: %s", id)
		}
		return &secretsmanager.GetSecretValueOutput{SecretString: &s}, nil
	}}
}

// captureLog redirects smexec diagnostics into a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := errOut
	errOut = buf
	t.Cleanup(func() { errOut = prev })
	return buf
}

// envValue reads a key out of an os.Environ-shaped slice.
func envValue(t *testing.T, env []string, key string) string {
	t.Helper()
	values, _ := splitEnv(env)
	return values[key]
}
