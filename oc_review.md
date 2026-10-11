# Review: `issue-14` → `main` (PR #15, 1 commit)

**Commit:** `e6b3b03` — feat: key-gated OpenRouter provider for the sandbox (credential providers)
**Diff:** 9 files, +219 / −7

> Note: this review was produced from a clone of `github.com/df3l0p/oc`, diffing
> `origin/main..origin/issue-14`. The clone was byte-identical to the local
> worktree, so the review is exact. CI (`test`) was green on the tip at review
> time; tests could not be run locally (no Go toolchain in the review
> environment).

## Summary of the change

Adds a second credential provider alongside the existing GitHub one. With
`OC_OPENROUTER_API_KEY` set (and `-sandbox` on), the sandbox can use OpenRouter
models while the key never enters the container:

- `oc` reads and unsets the env var, validates it (rejects whitespace/control
  chars without echoing the secret), and refuses `-no-inspect` (injection needs
  TLS interception).
- The sandbox gets only `OPENROUTER_API_KEY=<placeholder>`; the real key is
  handed to the proxy on stdin as one JSON line together with the GitHub
  provider config.
- The proxy swaps the placeholder for the real key in the `Authorization`
  header, only at `openrouter.ai:443`, which is added to the policy only when
  the key is set.
- Local llama stays the default; switching to an OpenRouter model happens
  inside opencode (built-in `openrouter` provider).

## Verdict

**Approve, with one caveat** (see "Caveat" below).

The change cleanly extends the existing github-provider pattern everywhere:
env read+unset (`main.go:120`), validation without echoing the secret,
`-no-inspect` refusal, "ignored without -sandbox" note, policy entry gated on
the key being set (`internal/harness/sandbox.go:343`), a single JSON-line
provider config covering both providers (`providerConfig()` in
`internal/harness/sandbox.go:417`), and a new `NewOpenRouterProvider` bound to
`openrouter.ai:443` (`internal/proxy/provider.go:70`). Nothing changes when the
variable is unset — policy stays clean, no extra provider, no extra config.

## What was verified

- **Policy ↔ provider consistency:** pre-existing `InjectorFromStdin` check
  requires every provider host to be explicitly listed in the policy (only
  explicit hosts are decrypted), so a credential can never be injected at a
  host that isn't MITM'd.
- **Catalog fetch works:** `models.dev:443` is already in the default policy
  (`internal/harness/policy.txt`), so opencode's model-catalog fetch inside the
  sandbox succeeds — the OpenRouter flow is functional, not just authorized.
- **Placeholder hygiene:** placeholders are crypto-random; `ReadProviders`
  rejects duplicate provider names and duplicate placeholders.
- **Secret containment:** tests verify the key reaches the proxy's stdin but
  never a docker argv (`TestSandboxRunSendsTheOpenRouterKeyOnStdinNotArgv`),
  that the env var is unset so child processes don't inherit it, on/off policy
  gating, multi-provider encoding, `-no-inspect` rejection, and that the usage
  text documents the variable.
- **Test coverage is good and security-focused** across
  `main_test.go`, `internal/harness/sandbox_test.go`, and
  `internal/proxy/providerconfig_test.go`.

## Caveat

**Host config can leak a real key into the sandbox.**
`Render` copies the host `opencode.jsonc` verbatim into the generated sandbox
config (`internal/opencodeconfig/config.go:87`). If a user puts a real
`apiKey` under `provider.openrouter` in their host config — easy to do, since
the README's "Curating models" tip directs them to edit that block — the real
key lands inside the container and defeats the entire placeholder scheme.

Suggested fixes (any one of these):

1. Strip or neutralize literal `apiKey` / `options.apiKey` values in the
   generated sandbox config, or
2. Warn at startup when the host config's `provider.openrouter` block contains
   an `apiKey`, or
3. At minimum, add a README warning telling users to keep keys out of the host
   config and use `OC_OPENROUTER_API_KEY` instead.

(This hazard predates this PR for other providers, but this feature actively
invites the mistake.)

## Nits

- **Redundant startup output:** with the key set, `oc` prints both the long
  OpenRouter warning and the shorter `credential providers:` line; the long
  line alone would suffice.
- **Partial placeholder state:** in `Sandbox.Run`
  (`internal/harness/sandbox.go:286`), if `newPlaceholder()` fails for
  OpenRouter after the GitHub placeholder was generated, `githubPlaceholder`
  remains set on the struct. Harmless since the session aborts, but a shared
  "generate all placeholders, then proceed" step would be tidier.
- **Cosmetic:** the new README table row for `OC_OPENROUTER_API_KEY` isn't
  aligned to the table's column widths (renders fine either way).
