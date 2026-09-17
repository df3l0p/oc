# oc

`oc` runs a coding agent against a local model with one command: it starts
`llama-server` (llama.cpp) if one isn't already running, points the agent at
it, and launches the agent in the current directory.

Concretely, running `oc` in a project:

1. Checks whether a `llama-server`/`llama serve` instance is already
   healthy on the target host/port; if not, starts one with `-hf <model>`.
2. Discovers the model(s) it's actually serving and merges an
   OpenAI-compatible provider entry for them into the agent's config.
3. Runs the agent in the current directory.
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
| `-model`    | `unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M`             | Model passed to llama-server's `-hf` flag        |
| `-host`     | `127.0.0.1`                                       | llama-server host                                |
| `-port`     | `8080`                                            | llama-server port                                |
| `-harness`  | `opencode`                                        | Coding agent to run                              |

Each harness owns its own config path internally (opencode's is its default
global config, `~/.config/opencode/opencode.jsonc`) — there's no flag for it.
`oc` merges into that file without touching any other keys already there,
and points the agent at it via that agent's own config env var
(`OPENCODE_CONFIG` for opencode), so it works even if the agent's normal
config resolution would otherwise pick something else up.

### Sharing a llama-server across sessions

If a healthy llama-server is already listening on the target port, `oc`
reuses it instead of starting a second one. Each running `oc` process
registers itself as a user of that port; when an `oc` that started the
server exits, it only stops the server once no other `oc` process is still
registered against it.

## Notes

- llama-server's own logs are written to
  `$TMPDIR/oc-llama-server-<port>.log` rather than to the terminal, since
  opencode's full-screen UI shares the same tty and raw log output would
  corrupt it. The path is printed on startup.
- Only opencode is implemented as a harness today. Adding another means
  implementing the small `Harness` interface in `internal/harness` and
  registering it there.
