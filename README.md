# oc

`oc` runs a coding agent against a local model with one command: it starts
`llama-server` (llama.cpp) if one isn't already running, points the agent at
it, and launches the agent in the current directory.

Concretely, running `oc` in a project:

1. Checks whether a `llama-server`/`llama serve` instance is already
   healthy on the target host/port; if not, starts one in router mode (no
   model flag), which serves every model in the llama.cpp cache and loads
   them on demand.
2. Discovers the served models and merges an OpenAI-compatible provider
   entry listing all of them into the agent's config. If `-model` isn't
   among them, it's downloaded first (`llama download`, unified CLI only).
3. Runs the agent in the current directory with `-model` selected.
4. On exit, stops the `llama-server` it started — unless another `oc`
   process is still using it, in which case it's left running.

## Requirements

- [opencode](https://opencode.ai) on `PATH` (the only harness today).
- llama.cpp on `PATH`: either the unified `llama` CLI (`llama serve`) or the
  older standalone `llama-server` binary. On macOS, `brew install llama.cpp`
  provides both.

## Install

```sh
go install github.com/df3l0p/oc@latest
```

or build from a checkout:

```sh
go build -o oc .
```

## Usage

```sh
cd your-project
oc
```

This starts (or reuses) a llama-server serving the default model, merges a
`llama-cpp` provider into opencode's config, and runs `opencode .`.

### Flags

| Flag        | Default                                          | Description                                     |
|-------------|---------------------------------------------------|--------------------------------------------------|
| `-model`    | `unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M`             | Model to select in the agent (quant optional; downloaded if not cached) |
| `-host`     | `127.0.0.1`                                       | llama-server host (with `-sandbox`, the narrowest address containers can reach; see below) |
| `-port`     | `8080`                                            | llama-server port                                |
| `-harness`  | `opencode`                                        | Coding agent to run                              |
| `-sandbox`  | off                                               | Run the agent in a Docker container (see below)  |
| `-image`    | `default`                                         | Bundled sandbox image to use (see below; requires `-sandbox`) |
| `-build`    | off                                               | Rebuild the sandbox image even if it exists (requires `-sandbox`) |
| `-all-net`  | off                                               | Let the sandbox reach any public host, not just the default allow-list (requires `-sandbox`; see below) |
| `-no-inspect` | off                                             | Don't decrypt the sandbox's HTTPS in the proxy; tunnel it unread (requires `-sandbox`; see below) |

Each harness owns its own config path internally (opencode's is its default
global config, `~/.config/opencode/opencode.jsonc`) — there's no flag for it.
`oc` merges into that file without touching any other keys already there,
and points the agent at it via that agent's own config env var
(`OPENCODE_CONFIG` for opencode), so it works even if the agent's normal
config resolution would otherwise pick something else up.

### Environment

| Variable          | Description |
| ----------------- | ----------- |
| `OC_GITHUB_TOKEN` | GitHub token the sandbox's proxy injects for `git` and `gh` (requires `-sandbox`; see *GitHub credentials* below). Prefer a fine-grained token; to reuse your `gh` login, `export OC_GITHUB_TOKEN=$(gh auth token)`. |

### Sharing a llama-server across sessions

If a healthy llama-server is already listening on the target port, `oc`
reuses it instead of starting a second one. Each running `oc` process
registers itself as a user of that port; when an `oc` that started the
server exits, it only stops the server once no other `oc` process is still
registered against it.

Because the server runs in router mode, sessions sharing it can each pick a
different `-model`. A server started some other way with a fixed model
(`-hf`/`-m`) only serves that model; `oc -model <other>` against it fails
with an error rather than silently using the wrong model — stop it or use
`-port`.

### Sandbox mode

`oc -sandbox` runs the agent inside a Docker container instead of on the
host. The llama-server still runs on the host; the sandbox container has no
direct route to it and instead reaches it through its per-session egress
proxy, which is what resolves `host.docker.internal`. The current directory
is bind-mounted at `/workspace`, and the container runs as your uid/gid so
files it writes keep your ownership. Only `docker` is needed on the host (not
opencode).

- **Config:** `oc` generates a private copy of your opencode config with the
  `llama-cpp` provider's `baseURL` rewritten for the container and mounts it
  read-only. Your real config is not modified.
- **Images:** `-image <name>` selects one of the images bundled in
  [`images/`](images), each a `<name>.Dockerfile`. The default, `default`,
  has opencode, git, `jq`, `ripgrep` and `curl`. Every image is a layer on an
  internal `base` image (opencode, git, the non-root user) that isn't
  selectable itself.
  `oc` never pulls a sandbox image from a registry: a missing image is built
  from its Dockerfile (a build failure is an error). `-build` rebuilds the
  images from scratch, ignoring docker's layer cache, so it also picks up new
  apt package versions and a newer `opencode-ai`; without it, an existing image
  is reused indefinitely.
  Images are tagged `oc-sandbox-<name>:<hash>` from the Dockerfile's contents
  (and that of the base), so a changed Dockerfile builds a new image and an
  unchanged one is reused. Note the build itself still fetches the
  `node:22-slim` base image, apt packages and the `opencode-ai` npm package; to
  control that, review or pin them in the Dockerfiles. Old tags are not
  removed automatically; list them with `docker images 'oc-sandbox-*'` and
  delete the ones you no longer need with `docker rmi`.
  - **Adding an image:** add `images/<name>.Dockerfile` as a layer on the
    base: declare `ARG OC_BASE`, make the final stage `FROM ${OC_BASE}` (oc
    passes the built base image as that build arg), and end with `USER oc`,
    after a `USER root` for installs. A test checks every bundled Dockerfile
    against this. Dockerfiles are built without a build context, so they can't
    `COPY` local files.
- **Parallel sessions:** each session gets a uniquely named container, and
  any number can share one host llama-server (same lifecycle rules as above).
- **Network exposure:** `oc` never listens on all interfaces by default. With
  the default `-host`, llama-server listens on `127.0.0.1` on macOS and Docker
  Desktop (which forward `host.docker.internal` to the host's loopback), and on
  the default Docker bridge's gateway IP (usually `172.17.0.1`) with a native
  Linux engine. That address isn't reachable from your network, and the
  proxy container (the only one with a route to the host — see Egress below)
  is pointed at the same IP. An explicit `-host` is used as is (a wildcard
  like `0.0.0.0` prints a warning).
  - On Linux, an already-running server bound to `127.0.0.1` isn't reachable
    from the proxy container, so `oc` stops with an error instead of reusing
    it.
- **Egress:** the sandbox container has no direct network access. Instead,
  each session gets its own proxy container on a private network between the
  two, and all of the sandbox's HTTP(S) traffic is forced through it via
  `HTTP_PROXY`/`HTTPS_PROXY`. The proxy only allows the session's model
  server plus `registry.npmjs.org`, `github.com`, `api.github.com`,
  `codeload.github.com`, `raw.githubusercontent.com`,
  `objects.githubusercontent.com` and `models.dev` (the
  latter ones over https) and blocks everything else, including non-HTTP
  protocols such as git over ssh. Those defaults live in
  `internal/harness/policy.txt` (one `host:port` per line), built into `oc`.
  `-all-net` opens the sandbox up to any public host over http(s) instead;
  your machine, your LAN and other private or VPN ranges stay blocked even
  by IP (the proxy checks the address it actually connects to), apart from
  llama-server. A blocked host shows up to the agent as a 403 response from
  `oc-proxy`. The proxy container itself
  reaches the host over the default Docker bridge, same as llama-server's own listen
  address above, so on Linux a host firewall (e.g. `ufw`) may still need to
  allow bridge-to-host traffic, and other containers on that bridge can
  still reach llama-server directly. The first `-sandbox` run also builds
  the proxy image (pulling the `golang` and `alpine` base images); both the
  network and the proxy container are torn down when the session ends.
- **TLS interception:** by default the proxy decrypts HTTPS to the hosts on
  its allow-list so it can log what the agent does with them: one line per
  request in `$TMPDIR/oc-proxy-<id>.log` (the path is printed at startup, and
  the file is kept after the session) with the method, host, path, status and
  duration, and never the query string, headers or bodies. Hosts opened only by
  `-all-net` are not decrypted; they stay tunnelled with their real
  certificates. `-no-inspect` turns the whole thing off.
  - *The CA:* the proxy creates a certificate authority for each session. Its
    private key lives only in the proxy's memory: it is never written to disk
    and never reaches the sandbox. The CA is restricted (an x509 name
    constraint) to the allow-listed host names, and valid for 30 days.
  - *Trust:* the proxy writes a bundle (the system roots plus the session CA's
    certificate) to a per-session docker volume, which the sandbox mounts
    read-only at `/etc/oc-ca`. `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
    `GIT_SSL_CAINFO`, `CURL_CA_BUNDLE` and `REQUESTS_CA_BUNDLE` point at it, so
    opencode, git, curl and most other tools trust the proxy. The volume is
    removed with the network when the session ends. Nothing about the CA is
    baked into an image.
  - *Upstream:* the proxy still verifies the real server's certificate itself.
  - *Limits:* HTTP/1.1 only, and WebSocket (`Upgrade`) requests are refused
    with a 501. A tool that ignores the variables above, or pins certificates,
    fails the TLS handshake; the reason is in the proxy log
    (`TLS handshake for <host> failed`).
- **GitHub credentials:** with `OC_GITHUB_TOKEN` set, the proxy injects the
  token into requests to `github.com` (git over https) and `api.github.com`
  (`gh`), so the agent can clone private repos, push and open PRs without the
  token ever being inside the sandbox.
  - *What the sandbox gets:* `GH_TOKEN` set to a random per-session placeholder
    and a git credential helper that answers with it. The proxy replaces the
    placeholder in the `Authorization` header (Bearer, token or Basic) and only
    for those two hosts. On decrypted HTTPS requests the placeholder anywhere
    else (path, query, other headers, a second `Authorization` value), or sent
    to any other decrypted host, is refused with a 403 and not forwarded. Request
    bodies aren't inspected, and plain-http requests and tunnelled hosts (those
    opened only by `-all-net`) are never checked; a placeholder is worthless
    outside the session's proxy, so the worst case is that it is seen upstream.
  - *How the token reaches the proxy:* `oc` reads `OC_GITHUB_TOKEN`, unsets it,
    and sends it to the proxy container on its stdin: never as an argument or
    environment variable (those show in `docker inspect`), never on disk. The
    proxy keeps it in memory only.
  - *Scope:* the token is the only limit on what the agent can do with it; the
    proxy doesn't restrict reads or writes. Use a fine-grained token scoped to
    what the agent needs. `export OC_GITHUB_TOKEN=$(gh auth token)` reuses your
    `gh` login, whose scopes are broad.
  - Requires `-sandbox` (ignored with a note otherwise) and TLS interception:
    `oc` refuses `-no-inspect` while the variable is set.
- Session data inside the container is discarded on exit (`--rm`).

## Notes

- llama-server's own logs are written to
  `$TMPDIR/oc-llama-server-<port>.log` rather than to the terminal, since
  opencode's full-screen UI shares the same tty and raw log output would
  corrupt it. The path is printed on startup.
- Only opencode is implemented as a harness today. Adding another means
  implementing the small `Harness` interface in `internal/harness` and
  registering it there.
