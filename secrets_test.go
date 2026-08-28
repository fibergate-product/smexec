package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

func TestParseSecretScalars(t *testing.T) {
	got, err := parseSecret("test", `{"S":"str","N":1e6,"I":42,"T":true,"F":false,"E":""}`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"S": "str", "N": "1e6", "I": "42", "T": "true", "F": "false", "E": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys, want %d", len(got), len(want))
	}
}

func TestParseSecretRejects(t *testing.T) {
	cases := map[string]string{
		"object value":   `{"K":{"a":1}}`,
		"array value":    `{"K":[1,2]}`,
		"null value":     `{"K":null}`,
		"json string":    `"hello"`,
		"json array":     `[1,2]`,
		"json null":      `null`,
		"json number":    `42`,
		"trailing data":  `{"K":"v"} {"K2":"v"}`,
		"not json":       `K=v`,
		"empty key":      `{"":"v"}`,
		"key with eq":    `{"A=B":"v"}`,
		"key with nul":   "{\"A\\u0000B\":\"v\"}",
		"value with nul": "{\"K\":\"a\\u0000b\"}",
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSecret("test", payload); err == nil {
				t.Fatalf("parseSecret(%q) succeeded, want error", payload)
			}
		})
	}
}

// A map decode would keep the last duplicate; smexec keeps the first, matching
// how it merges across secrets.
func TestParseSecretKeepsFirstDuplicateKey(t *testing.T) {
	log := captureLog(t)
	got, err := parseSecret("dup-secret", `{"A":"first","B":"x","A":"second"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] != "first" {
		t.Fatalf("A = %q, want the first occurrence", got["A"])
	}
	if got["B"] != "x" {
		t.Fatalf("B = %q", got["B"])
	}
	if !strings.Contains(log.String(), "key A: duplicated in secret dup-secret") {
		t.Errorf("duplicate not logged: %s", log)
	}
	if strings.Contains(log.String(), "second") {
		t.Errorf("value leaked into log: %s", log)
	}
}

// Every occurrence is validated even though only the first is kept.
func TestParseSecretValidatesShadowedDuplicate(t *testing.T) {
	captureLog(t)
	if _, err := parseSecret("test", `{"A":"ok","A":{"nested":1}}`); err == nil {
		t.Fatal("want an error for a malformed duplicate")
	}
}

func TestFetchRejectsBinarySecret(t *testing.T) {
	client := &fakeClient{reply: func(string) (*secretsmanager.GetSecretValueOutput, error) {
		return &secretsmanager.GetSecretValueOutput{SecretBinary: []byte("blob")}, nil
	}}
	_, err := fetch(context.Background(), client, "bin")
	if err == nil || !strings.Contains(err.Error(), "SecretString") {
		t.Fatalf("err = %v, want a SecretString complaint", err)
	}
}

func TestFetchAllPreservesOrder(t *testing.T) {
	client := stringSecrets(map[string]string{
		"one": `{"A":"1"}`,
		"two": `{"A":"2"}`,
	})
	got, err := fetchAll(context.Background(), client, []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["A"] != "1" || got[1]["A"] != "2" {
		t.Fatalf("results out of order: %v", got)
	}
}

func TestFetchAllReportsErrorWithID(t *testing.T) {
	client := stringSecrets(map[string]string{"good": `{"A":"1"}`})
	_, err := fetchAll(context.Background(), client, []string{"good", "missing"})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want it to name the failing secret", err)
	}
}
