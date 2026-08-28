package main

import (
	"context"
	"slices"
	"strings"
	"testing"
)

const arn = "arn:aws:secretsmanager:ap-northeast-1:123456789012:secret:OPDXFGBeat2-Secret-AbC123"

func TestResolveInjectsKeys(t *testing.T) {
	captureLog(t)
	client := stringSecrets(map[string]string{arn: `{"APP_KEY":"base64:abc","DB_PASSWORD":"hunter2"}`})

	env, err := resolve(context.Background(), client, []string{"PATH=/bin"}, []string{arn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(t, env, "APP_KEY"); got != "base64:abc" {
		t.Errorf("APP_KEY = %q", got)
	}
	if got := envValue(t, env, "DB_PASSWORD"); got != "hunter2" {
		t.Errorf("DB_PASSWORD = %q", got)
	}
	if got := envValue(t, env, "PATH"); got != "/bin" {
		t.Errorf("PATH = %q, inherited vars must survive", got)
	}
}

// The whole reason chamber was rejected: it lowercases SecretId before the API
// call, and Secrets Manager names are case-sensitive.
func TestResolvePassesSecretIDVerbatim(t *testing.T) {
	ids := []string{arn, "OPDXFGBeat2-MixedCase-Name"}
	client := stringSecrets(map[string]string{ids[0]: `{"A":"1"}`, ids[1]: `{"B":"2"}`})
	captureLog(t)

	if _, err := resolve(context.Background(), client, nil, ids, nil); err != nil {
		t.Fatal(err)
	}
	got := client.requests()
	slices.Sort(got)
	want := slices.Sorted(slices.Values(ids))
	if !slices.Equal(got, want) {
		t.Fatalf("SecretIds sent = %q, want %q byte-identical", got, want)
	}
}

func TestResolveFirstSecretWins(t *testing.T) {
	log := captureLog(t)
	client := stringSecrets(map[string]string{
		"override": `{"SHARED":"specific"}`,
		"base":     `{"SHARED":"general","ONLY_BASE":"b"}`,
	})

	env, err := resolve(context.Background(), client, nil, []string{"override", "base"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(t, env, "SHARED"); got != "specific" {
		t.Errorf("SHARED = %q, want the first secret to win", got)
	}
	if got := envValue(t, env, "ONLY_BASE"); got != "b" {
		t.Errorf("ONLY_BASE = %q, want later secrets to still fill new keys", got)
	}
	if !strings.Contains(log.String(), "key SHARED: already provided by an earlier secret") {
		t.Errorf("collision not logged: %s", log)
	}
	if strings.Contains(log.String(), "general") {
		t.Errorf("value leaked into log: %s", log)
	}
}

func TestInheritedEnvIsNeverReplaced(t *testing.T) {
	log := captureLog(t)
	client := stringSecrets(map[string]string{arn: `{"DB_PASSWORD":"from-secret","APP_KEY":"from-secret"}`})

	env, err := resolve(context.Background(), client, []string{"DB_PASSWORD=set-by-cdk"}, []string{arn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(t, env, "DB_PASSWORD"); got != "set-by-cdk" {
		t.Fatalf("DB_PASSWORD = %q, want the inherited value preserved", got)
	}
	if got := envValue(t, env, "APP_KEY"); got != "from-secret" {
		t.Fatalf("APP_KEY = %q, want the secret to fill a key with no inherited value", got)
	}
	if !strings.Contains(log.String(), "kept inherited env over secret: DB_PASSWORD") {
		t.Errorf("shadowing not logged: %s", log)
	}
}

// Presence wins, not non-emptiness: an explicitly empty variable is still a
// deliberate setting. SMEXEC_REQUIRE is what catches it when that is a mistake.
func TestInheritedEmptyValueStillWins(t *testing.T) {
	captureLog(t)
	client := stringSecrets(map[string]string{arn: `{"DB_PASSWORD":"from-secret"}`})

	env, err := resolve(context.Background(), client, []string{"DB_PASSWORD="}, []string{arn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(t, env, "DB_PASSWORD"); got != "" {
		t.Fatalf("DB_PASSWORD = %q, want the inherited empty value preserved", got)
	}

	captureLog(t)
	if _, err := resolve(context.Background(), client, []string{"DB_PASSWORD="}, []string{arn}, []string{"DB_PASSWORD"}); err == nil {
		t.Fatal("SMEXEC_REQUIRE should reject a key left empty by the inherited environment")
	}
}

func TestResolveKeepsEnvOrderAndAppendsSorted(t *testing.T) {
	captureLog(t)
	client := stringSecrets(map[string]string{arn: `{"ZED":"z","ALPHA":"a"}`})

	env, err := resolve(context.Background(), client, []string{"PATH=/bin", "HOME=/root"}, []string{arn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "HOME=/root", "ALPHA=a", "ZED=z"}
	if !slices.Equal(env, want) {
		t.Fatalf("env = %q, want %q", env, want)
	}
}

func TestResolveRequire(t *testing.T) {
	client := stringSecrets(map[string]string{arn: `{"APP_KEY":"k","BLANK":""}`})

	t.Run("satisfied", func(t *testing.T) {
		captureLog(t)
		if _, err := resolve(context.Background(), client, nil, []string{arn}, []string{"APP_KEY"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("lists every missing key", func(t *testing.T) {
		captureLog(t)
		_, err := resolve(context.Background(), client, nil, []string{arn}, []string{"APP_KEY", "DB_PASSWORD", "BLANK", "GCP_SA"})
		if err == nil {
			t.Fatal("want an error")
		}
		for _, k := range []string{"DB_PASSWORD", "BLANK", "GCP_SA"} {
			if !strings.Contains(err.Error(), k) {
				t.Errorf("%q missing from %q", k, err)
			}
		}
		if strings.Contains(err.Error(), "APP_KEY") {
			t.Errorf("APP_KEY resolved but was reported: %q", err)
		}
	})

	t.Run("inherited env can satisfy", func(t *testing.T) {
		captureLog(t)
		_, err := resolve(context.Background(), client, []string{"DB_PASSWORD=x"}, []string{arn}, []string{"DB_PASSWORD"})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestResolveFailsClosedOnFetchError(t *testing.T) {
	captureLog(t)
	client := stringSecrets(nil)
	if _, err := resolve(context.Background(), client, nil, []string{arn}, nil); err == nil {
		t.Fatal("want an error rather than a partially populated environment")
	}
}

func TestResolveHonoursContextDeadline(t *testing.T) {
	captureLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := stringSecrets(map[string]string{arn: `{"A":"1"}`})
	if _, err := resolve(ctx, client, nil, []string{arn}, nil); err == nil {
		t.Fatal("want the cancelled context to fail the fetch")
	}
}

// Cross-cutting: no secret value may reach the log, on any path.
func TestResolveNeverLogsValues(t *testing.T) {
	const secret = "s3cr3t-canary-value"
	payloads := []map[string]string{
		{arn: `{"A":"` + secret + `"}`},
		{arn: `{"A":"` + secret + `"}`, "second": `{"A":"` + secret + `"}`},
		{arn: `{"A":{"nested":"` + secret + `"}}`},
		{arn: `"` + secret + `"`},
	}
	for _, payload := range payloads {
		log := captureLog(t)
		ids := []string{arn}
		if _, ok := payload["second"]; ok {
			ids = append(ids, "second")
		}
		_, err := resolve(context.Background(), stringSecrets(payload), []string{"A=" + secret}, ids, []string{"A", "MISSING"})
		if strings.Contains(log.String(), secret) {
			t.Fatalf("secret value leaked into log: %s", log)
		}
		// main logs whatever resolve returns, so the error text counts too.
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Fatalf("secret value leaked into error: %v", err)
		}
	}
}
