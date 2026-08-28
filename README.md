# smexec

Fetch secrets from AWS Secrets Manager, inject them as plain environment variables, and `exec` the
real command — for container-image Lambda functions, which have no native secret injection.
Applications keep reading ordinary environment variables, so no per-language Secrets Manager client
is needed and nothing changes above the process boundary.

## Use

Set the variables, then launch:

```sh
# One or more ids, comma-separated, ARN or name; the earliest to supply a key wins.
# Here APP_KEY comes from the per-service secret, DB_PASSWORD from the shared one.
export SMEXEC_SECRET_IDS=arn:aws:secretsmanager:ap-northeast-1:123456789012:secret:ApiStack-AppSecret-Wj4kTz,prod/shared/rds
export SMEXEC_REQUIRE=APP_KEY,DB_PASSWORD   # optional
smexec php-fpm --nodaemonize --force-stderr
```

Everything after the command is passed straight through as its arguments.

| Variable | Default | Meaning |
|---|---|---|
| `SMEXEC_SECRET_IDS` | — | Comma-separated secret ARNs or names. **Unset or empty: exec immediately and do nothing else.** |
| `SMEXEC_REQUIRE` | — | Comma-separated keys that must resolve to a non-empty value. Exits non-zero listing every one that did not. |
| `SMEXEC_TIMEOUT` | `5s` | Deadline covering all fetching. |

In an image, as the entrypoint:

```dockerfile
# The secret ids are per-environment, so they come from the function or task
# definition rather than the image.
ENTRYPOINT ["/usr/local/bin/smexec"]
CMD ["php-fpm", "--nodaemonize"]

# where the stage already has an entrypoint, prepend smexec to it
ENTRYPOINT ["/usr/local/bin/smexec", "docker-php-entrypoint"]
```

`ENTRYPOINT` rather than a rewritten `CMD`, so an ECS `command:` override still flows through as
arguments.

```ts
environment: { SMEXEC_SECRET_IDS: appSecret.secretArn, SMEXEC_REQUIRE: 'APP_KEY,DB_PASSWORD' }
appSecret.grantRead(fn)
```

Each `SecretString` must be a flat JSON object; keys become environment variables 1:1, with no
renaming. `SecretId` is passed through verbatim — ARN or name, any case — so existing secrets need
no renaming or value migration.

## Which value wins

Highest priority first:

1. **The inherited environment, always.** A variable already set in the process — the Lambda
   environment block, `ENV` in the image, whatever the shell exported — is **never replaced**.
   Presence is what counts, not emptiness: an explicit `FOO=` still wins. Every key a secret could
   not fill is logged by name.
2. **Secrets, in `SMEXEC_SECRET_IDS` order.** The earliest id to supply a key keeps it; later ones
   still fill keys nothing has supplied yet.
3. **Duplicate keys within one secret's JSON.** The first occurrence wins. Both collisions are
   logged by key name.

The rule is **first writer wins, everywhere** — a secret fills gaps and never overrides. To override
one key for a single function, set it in that function's own environment; to override across
secrets, list the more specific one first.

The trap is the empty inherited value: `FOO=` blocks the secret and the application sees nothing.
Listing the key in `SMEXEC_REQUIRE` turns that into a startup failure naming `FOO`.

## Notes

- **Inert without configuration.** No secret IDs means no AWS call and no `SMEXEC_REQUIRE` check, so
  one image runs unchanged under Compose, in CI, and in an ECS debug task.
- **Fails closed.** Any fetch, parse or requirement failure exits non-zero rather than starting the
  process with missing secrets. Values may be strings, numbers or booleans; `null`, objects and
  arrays are errors, never silently empty strings.
- **Never logs values** — key names and counts only, to stderr. Nothing reaches stdout except
  `smexec --version`.
- **Replaces itself** via `syscall.Exec`, so the target keeps PID 1 and owns the process's signals.
- ECS does not need smexec: use `ecs.Secret.fromSecretsManager`, which is also the only thing
  visible to ECS Exec sessions.

Exit codes: `2` no command, `126` not executable, `127` not found, `1` configuration, fetch, parse
or requirement failure.

## Install

Verify the release once, then pin the digest — this binary runs as PID 1 with read access to every
application secret.

```sh
gh release download v0.1.0 --repo sarisia/smexec --pattern smexec-linux-amd64
gh attestation verify smexec-linux-amd64 --repo sarisia/smexec && sha256sum smexec-linux-amd64
```

GitHub's signed build provenance proves these exact bytes came out of this repository's release
workflow at a specific commit, rather than being uploaded by hand.

An image build has no GitHub credentials, so the verified digest is what carries that into the
build:

```dockerfile
ARG SMEXEC_VERSION=v0.1.0
RUN curl -fsSL -o /usr/local/bin/smexec \
      https://github.com/sarisia/smexec/releases/download/${SMEXEC_VERSION}/smexec-linux-amd64 \
 && echo "<sha256>  /usr/local/bin/smexec" | sha256sum -c - \
 && chmod +x /usr/local/bin/smexec
```

Releases carry `smexec-linux-{amd64,arm64}`, `SHA256SUMS` and provenance, and repeat the checksums
in the notes. Tags are never moved.

## Development

```sh
go test ./...
```

No AWS access needed: the Secrets Manager client is faked. The process tests re-execute the test
binary, so `syscall.Exec` and the exit codes are exercised for real.
