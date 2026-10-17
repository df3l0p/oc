# Sandbox skills: GLM-5.3 vs Claude Opus 5.5

Two `rnes` (opencode) instances implemented the same spec,
[`2026-10-13-sandbox-skills-design.md`](../../../.worktree/skill-glm-01/docs/2026-10-13-sandbox-skills-design.md),
each in its own worktree, one model per worktree:

| | GLM-5.3 (via OpenRouter) | Claude Opus 5.5 (via OpenRouter) |
|---|---|---|
| Worktree | `.worktree/skill-glm-01` | `.worktree/skill-claude-01` |
| Wall time | 20m 32s | 10m 37s |
| Context at finish | 156,541 tokens (15%) | 141,274 tokens (14%) |
| Cost (from the TUI sidebar) | $0.74 | $2.82 |
| Go toolchain in the session | none: `make lint test` **not run** (egress proxy blocked the download); checked with a tree-sitter parse and manual review | fetched Go 1.26.8 from an npm package into `/tmp/w`, ran the tests; avoided syntax newer than `go 1.23.4` |

Both sandboxes had no Docker, so **neither agent ever ran a real sandbox session**.

## How this review was done

- Read both diffs in full against `25eeeb0`, plus the new files.
- On the host (Go 1.23.4, the repo's `go.mod` version): `gofmt -l`, `go vet ./...`,
  `go test ./...` in both worktrees.
- Wrote throwaway probe tests (deleted afterwards, worktrees left as the models
  left them) that call each resolver with awkward filesystem layouts: symlinked
  sources, symlinked skill folders, odd names, a `SKILL.md` symlinked outside
  its folder, `all` mixed with an explicit path, relative paths.
- Checked the README changes against the spec's "Docs" section.
- **Not done:** no real `rnes -sandbox -skill ...` run, so the docker mount
  behavior (including the writable-home requirement) is judged from reading the
  code and its tests, not observed. See "Still to verify".

## Verdict

**Opus 5.5's implementation is the better one, and I'd merge it (after the
checks at the end) over GLM's.** Both satisfy the five acceptance criteria on
the happy path and both pass their own tests plus the existing suite on the
host. The difference is in the edges: GLM's resolver has two bugs on layouts
that are common in practice (symlinked skills), and its name rules are looser
than opencode's. Opus found and handled both, and made more deliberate
decisions where the spec was silent. GLM was twice as slow and a quarter of the
price, and its design is smaller and simpler; that is its real advantage.

## Results on the host

| Check | GLM-5.3 | Opus 5.5 |
|---|---|---|
| `gofmt -l .` | clean | clean |
| `go vet ./...` | clean | clean |
| `go test ./...` | all pass | all pass |
| Package coverage | `internal/harness` not measured separately | `internal/skills` 92.3%, `internal/harness` 84.3% |
| Pre-existing failure the agent reported | n/a | Opus reported `TestStopRecordedServer...` failing in its sandbox; it passes on the host, so that was environmental, not a repo bug |

## Size and structure

| | GLM-5.3 | Opus 5.5 |
|---|---|---|
| Resolution code | `internal/harness/skills.go`, 236 lines, inside the `harness` package | new package `internal/skills`, 388 lines, no docker or harness imports |
| Tests | `internal/harness/skills_test.go`, 365 lines (+40 in `main_test.go`) | `internal/skills/skills_test.go`, 408 lines, plus 103 in `sandbox_test.go`, 85 in `main_test.go` |
| Modified files | 7 (README, `base.Dockerfile`, `harness.go`, `opencode.go`, `sandbox.go`, `main.go`, `main_test.go`) | 6 (README, `harness.go`, `sandbox.go`, `sandbox_test.go`, `main.go`, `main_test.go`) |
| Where resolution runs | in `newOpencode`, i.e. inside `harness.New`, before the docker check | in `main.run`, before `harness.New` |
| `Options` carries | raw flag values (`[]string` x2); harness resolves | already-resolved `[]skills.Skill` |

Opus's split (pure resolver package, harness gets resolved values) is cleaner
and is why its resolver can be tested in isolation. GLM's resolution is tied to
the harness and uses a package-level `warnf` variable replaced in tests, a
small smell compared to Opus returning warnings as data.

## Spec compliance

| Spec item | GLM-5.3 | Opus 5.5 |
|---|---|---|
| No flag: unchanged container command, nothing read | yes (tested) | yes (tested) |
| Flags repeatable, require `-sandbox`, same error style | yes | yes (adds a repeatability test) |
| `-sandbox-update` ignores them with a note | yes | yes |
| Source order, missing default dirs skipped, missing `-skills-dir` errors | yes | yes |
| Ambiguous name: error listing all candidates | yes, **but false positives with symlinks** (see bugs) | yes; same directory reached twice counts once |
| Path form never ambiguous | yes | yes |
| `all`: first wins, one warning per shadowed skill | yes | yes |
| Duplicate requests merged silently | yes | yes |
| Unknown name: error naming sources searched | yes, lists all three defaults | lists only the folders that exist; when none exist, lists the three defaults |
| Missing `SKILL.md` errors | yes | yes |
| Invalid entries under `all`: warn and skip | yes, including noise such as `.git` | warns, but skips hidden entries and plain files silently |
| Project collision warning | yes, emitted in `Sandbox.Run` | yes, emitted from `Resolve` |
| Read-only mount at the skills location | `-v host:dest:ro` | `--mount type=bind,...,readonly` (CSV-quoted) |
| Symlink inside a skill pointing out is dangling | by virtue of the bind mount | same, **and** rejects a `SKILL.md` that is itself a symlink out of its folder |
| Agent home stays writable | pre-creates world-writable dirs in `base.Dockerfile` | mounts a per-session host staging dir as `~/.config` |
| One startup line: names and source folder | yes (in `Sandbox.Run`) | yes (in `main.run`) |
| More than 50 skills: warning | yes | yes |
| README: flags table, Skills subsection, security note incl. OpenRouter | table rows and a Skills bullet list (31 lines) | table rows and a full Skills subsection with examples (69 lines) |

## Bugs and risks found

### GLM-5.3

1. **`-skill all` silently drops symlinked skill folders** (confirmed with a
   probe). The code uses `DirEntry.IsDir()`, which is false for a symlink, so
   `~/.claude/skills/foo -> ~/repos/skills/foo` is not attached and **no
   warning is printed**. Symlinking skills in from a checkout is a very common
   setup, so `all` would quietly attach less than the user expects. Naming such
   a skill explicitly works, which makes the gap harder to spot.
2. **False "ambiguous" error when two sources are the same folder** (confirmed).
   If `~/.agents/skills` is a symlink to `~/.claude/skills` (a common way to
   share skills between tools), `-skill foo` fails as ambiguous with two
   candidates that are the same directory. Under `all` the same layout prints a
   bogus "shadows" warning for every skill. Candidates are compared by path,
   not by resolved path.
3. **Name rules are looser than opencode's.** `My_Skill.v2` is accepted
   (uppercase, underscore, dots). Opencode's documented rule, as I recall it, is
   lowercase alphanumerics with single hyphens, 64 characters at most, so such a
   skill would be mounted but not usable. I did not re-check this against the
   opencode source.
4. **A `SKILL.md` that symlinks outside the folder is accepted** (confirmed).
   In the container that file dangles, so the skill is attached but empty. The
   spec only requires that the target isn't exposed, which holds; but the
   result is a silently broken skill.
5. **`-skill all` plus an explicit path is order dependent.** `-skill all -skill /x/q`
   fails with "another skill named q is already attached"; `-skill /x/q -skill all`
   works with a shadow warning. The spec says duplicates in the request are
   merged, so a user would reasonably expect both to work.
6. **Image rebuild and custom images.** The writable-home fix edits
   `images/base.Dockerfile`. That changes the base image hash, so every user
   rebuilds on their next run, even without skills (the agent noted this). It
   only helps images built `FROM` the rnes base; a custom `-image` that isn't
   would get docker-created root-owned `~/.config/opencode`, which is exactly
   the failure the spec says to avoid. The agent also did not test this
   because it had no Docker.
7. **`-v host:container:ro`** splits on colons, so a skill path containing `:`
   breaks. Minor.
8. **Lint/test were not run in the session.** It happened to compile and pass
   on the host, which is to the model's credit, but the claim in its summary was
   "verified by parse and review".

### Claude Opus 5.5

1. **Staging directory instead of an image change** (design trade-off, not a
   bug). With skills attached, `~/.config` in the container is a host temp dir
   (`rnes-sandbox-home-config-*`), removed on a normal exit. Consequences: it
   works for any image and doesn't force a rebuild; but anything an image puts
   in `~/.config` is hidden while skills are attached (documented in the
   README); the agent's `~/.config/opencode` writes (the Opus agent measured
   roughly 63 MB of plugin files) land on the host disk; and the directory is
   leaked if rnes is killed with SIGKILL or crashes. Ownership is handled
   (`--user uid:gid` is already used, and a test checks the directory is the
   user's).
2. **Stricter names than the spec states.** Uppercase or underscored folder
   names are rejected (an error if requested, a warning under `all`). I believe
   this matches opencode, but a reviewer should decide whether rnes should
   enforce opencode's rule or leave it to opencode.
3. **Silent skipping under `all`** of hidden entries and plain files. This is
   reasonable (and documented in the README) but the spec literally says
   invalid entries warn; `.git` is not an invalid skill, so I read it as
   compatible.
4. **"Not found" message** lists only the existing source folders. Listing the
   defaults too (as GLM does) would be a bit more helpful, and matches "naming
   the sources searched" more literally.
5. `all` is applied after explicit values, so an explicit path wins over `all`
   with a shadow warning. That is a sensible, documented rule, but it goes
   beyond the spec.

## Decisions the spec left open

| Question | GLM-5.3 | Opus 5.5 |
|---|---|---|
| Same real folder reached twice | two candidates, so an error | one skill (resolved path compared) |
| Explicit path vs `all` for the same name | order dependent; may error | explicit wins, shadow warning; two different explicit paths error |
| Symlinked skill folder under `all` | **dropped silently** | attached |
| `SKILL.md` symlinked out | accepted | rejected as invalid |
| Hidden entries under `all` | warned about | skipped silently |
| `-skills-dir` without `-skill` | resolves and attaches nothing, no note | attaches nothing and prints a note saying so |
| Skill named `all` | by path only | by path only (documented) |
| `-skill .` | "not a valid skill name" | treated as a path, then "no SKILL.md" |
| Warnings printed | at construction (resolution) and run time | all from `Resolve`, printed by `main` |

## Process observations

- **Speed vs cost:** Opus finished in half the time at about 3.8x the cost, using
  slightly fewer tokens. For a task this small the dollar difference is minor;
  the 10 minutes saved and the correctness gap matter more.
- **Verification:** Opus got a working Go toolchain by finding an npm package
  that bundles it, so it ran the real tests. GLM, blocked, fell back to a parse
  check and said so plainly; that is honest reporting, but its result is weaker
  evidence. Both were upfront about not running Docker.
- **Spec adherence:** both followed the spec's structure closely and both added
  the tests it lists. Opus went further on edge cases (symlinks, relative paths,
  `~user`, empty value) and documented its decisions in the final summary under
  "Choices the design didn't settle", which makes review much easier. GLM's
  summary didn't list its judgment calls.
- **Honest limits:** neither agent claimed the mounts work end to end.

## Still to verify (neither implementation has been run for real)

For both, on a machine with Docker, ideally Linux and Docker Desktop for macOS:

1. `rnes -sandbox -skill <name>`: opencode lists the skill and can still write
   under `~/.config/opencode` (the writable-home requirement).
2. The skill is read-only: a write into it from the container fails.
3. `rnes -sandbox` with no flags: container command identical to before.
4. GLM only: a custom `-image` not based on the rnes base, with a skill attached.
5. Opus only: the staging directory is removed after Ctrl+C, and its ownership
   is right on Linux when the host uid differs from the image's `rnes` user.

## Recommendation

1. Take **Opus 5.5's implementation** as the base.
2. Before merging, run the manual checks above, especially 1 and 5.
3. If a smaller change to `images/` is preferred over the staging directory,
   port GLM's Dockerfile approach, accepting the rebuild and the custom-image
   gap.
4. If GLM's version is kept for its smaller footprint, fix at least bugs 1 and
   2 (resolve symlinks when listing and when comparing candidates) and add tests
   for symlinked skills, which neither suite of GLM's covers.
