// Skill command generation for the opencode harness: rnes exposes each
// skill it knows about as a /<name> command — on the host every skill in the
// default skill folders, in the sandbox the attached ones — so a skill can be
// invoked explicitly from the prompt, the way Claude exposes skills as slash
// commands.
//
// Custom slash commands are a cross-agent convention (Claude Code, Cursor,
// Codex, Gemini CLI all have them), but every agent's encoding differs. This
// file is opencode's: markdown command files with a description, a
// $ARGUMENTS-less template so a bare /name stays valid, .opencode/commands
// paths, and an inline config for OPENCODE_CONFIG_CONTENT. Skill discovery
// itself stays in package skills.
//
// The two sessions carry the commands differently. On the host they ride in
// the OPENCODE_CONFIG_CONTENT environment variable: an inline opencode
// config merged above every file-based source (see opencode v2's config
// loader), so nothing is ever written to the user's ~/.config. A server only
// reads that variable when it starts, which is why a host session with
// commands runs opencode with --standalone (its own server) instead of the
// shared background one. In the sandbox the commands are markdown files
// written into the staged ~/.config next to the mounted skills, where
// opencode finds them as global commands.
package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// commandTemplate is the prompt a /<name> command expands to. It has no
// $ARGUMENTS placeholder, so opencode appends any arguments after a blank
// line: "/name" with no arguments stays a valid command, and "/name some
// target" passes the target along.
func commandTemplate(name string) string {
	return fmt.Sprintf("Load the `%s` skill with the skill tool and follow its instructions.", name)
}

// commandMarkdown is the command file for name: frontmatter for opencode's
// command list, body as the prompt template.
func commandMarkdown(name string) string {
	return fmt.Sprintf("---\ndescription: Run the %s skill\n---\n\n%s\n", name, commandTemplate(name))
}

// inlineConfigEnv is the environment variable that carries an inline opencode
// config, merged above every file-based source.
const inlineConfigEnv = "OPENCODE_CONFIG_CONTENT"

// commandDef is a command in an opencode config's commands map.
type commandDef struct {
	Description string `json:"description"`
	Template    string `json:"template"`
}

func newCommandDef(name string) commandDef {
	return commandDef{Description: "Run the " + name + " skill", Template: commandTemplate(name)}
}

// configContent is the inline opencode config defining a command per name,
// for OPENCODE_CONFIG_CONTENT on the host. Empty for no names.
func configContent(names []string) string {
	if len(names) == 0 {
		return ""
	}
	commands := make(map[string]commandDef, len(names))
	for _, n := range names {
		commands[n] = newCommandDef(n)
	}
	out, err := json.Marshal(map[string]map[string]commandDef{"commands": commands})
	if err != nil {
		return "" // unreachable: plain JSON data
	}
	return string(out)
}

// parseInline splits an inline opencode config, which must be a plain JSON
// object, into its top-level keys and its commands map. Either may come back
// nil when the config has none.
func parseInline(content string) (top, commands map[string]json.RawMessage, err error) {
	if err := json.Unmarshal([]byte(content), &top); err != nil {
		return nil, nil, err
	}
	if raw, ok := top["commands"]; ok {
		if err := json.Unmarshal(raw, &commands); err != nil {
			return nil, nil, fmt.Errorf("its commands: %w", err)
		}
	}
	return top, commands, nil
}

// mergeConfigContent adds a command per name to the user's own inline config,
// leaving every other setting, and any command they already define, as it is.
func mergeConfigContent(existing string, names []string) (string, error) {
	top, commands, err := parseInline(existing)
	if err != nil {
		return "", err
	}
	if top == nil {
		top = map[string]json.RawMessage{}
	}
	if commands == nil {
		commands = map[string]json.RawMessage{}
	}
	for _, n := range names {
		if _, ok := commands[n]; ok {
			continue
		}
		raw, err := json.Marshal(newCommandDef(n))
		if err != nil {
			return "", err
		}
		commands[n] = raw
	}
	if top["commands"], err = json.Marshal(commands); err != nil {
		return "", err
	}
	out, err := json.Marshal(top)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// withEnv returns env with key set to value, replacing any earlier entry
// rather than leaving a duplicate whose winner depends on the reader.
func withEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return append(out, key+"="+value)
}

// builtinCommands are opencode's own slash commands (see the TUI docs); a
// generated command would replace them, so they are skipped. Not exhaustive:
// keep in sync with https://opencode.ai/v2/docs/cli/tui/.
var builtinCommands = map[string]bool{
	"agents": true, "btw": true, "compact": true, "editor": true,
	"help": true, "highlights": true, "mcps": true, "models": true,
	"new": true, "redo": true, "sessions": true, "undo": true,
}

// commandScope is where a session's opencode can see existing commands from,
// for the collision check.
type commandScope struct {
	// cwd is the session's working directory.
	cwd string
	// stop is the topmost directory to inspect; "" walks to the filesystem
	// root, matching opencode's config discovery. The sandbox passes cwd:
	// only the workdir is mounted, so host ancestors are not visible there.
	stop string
	// globalDir is the user's opencode config directory; "" when the session
	// cannot see it (the sandbox's staged ~/.config hides it).
	globalDir string
	// extra are commands defined somewhere other than those files (the
	// user's own OPENCODE_CONFIG_CONTENT), by name with where they come from.
	extra map[string]string
}

// commandNames picks the names worth a generated command. A name opencode
// already has a command for is dropped with a warning: a built-in one (which
// the generated command would replace), one the project at cwd defines
// (including its ancestors, since opencode discovers those too), or — when
// the scope includes it — one the user's global commands define. The
// generated commands merge above every file-based source on the host, so
// leaving those alone keeps them working. The skill itself is unaffected; the
// agent can still load it by name.
func commandNames(scope commandScope, names []string) ([]string, []string) {
	defined, warnings := commandsVisibleTo(scope)
	var out []string
	for _, n := range names {
		if builtinCommands[n] {
			warnings = append(warnings, fmt.Sprintf("skill command /%s skipped: it would replace opencode's built-in /%s command; the skill itself is still available to the agent", n, n))
			continue
		}
		if p, ok := defined[n]; ok {
			warnings = append(warnings, fmt.Sprintf("skill command /%s skipped: %s already defines a command with that name; the skill itself is still available to the agent", n, p))
			continue
		}
		out = append(out, n)
	}
	return out, warnings
}

// commandsVisibleTo lists the commands the session's opencode has, by name
// with the file defining each: project command files and configs from cwd up
// to scope.stop, plus the global config directory when the scope has one.
func commandsVisibleTo(scope commandScope) (map[string]string, []string) {
	out := map[string]string{}
	addCommands(out, scope.extra)
	var warnings []string
	for dir := scope.cwd; ; dir = filepath.Dir(dir) {
		addCommands(out, commandFiles(filepath.Join(dir, ".opencode", "commands")))
		addCommands(out, commandFiles(filepath.Join(dir, ".opencode", "command")))
		for _, f := range []string{
			filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc"),
			filepath.Join(dir, ".opencode", "opencode.json"), filepath.Join(dir, ".opencode", "opencode.jsonc"),
		} {
			cmds, err := configCommands(f)
			if err != nil {
				warnings = append(warnings, unreadableConfig(f, err))
			}
			addCommands(out, cmds)
		}
		if dir == scope.stop {
			break
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	if scope.globalDir != "" {
		addCommands(out, commandFiles(filepath.Join(scope.globalDir, "commands")))
		addCommands(out, commandFiles(filepath.Join(scope.globalDir, "command")))
		for _, f := range []string{
			filepath.Join(scope.globalDir, "opencode.json"),
			filepath.Join(scope.globalDir, "opencode.jsonc"),
		} {
			cmds, err := configCommands(f)
			if err != nil {
				warnings = append(warnings, unreadableConfig(f, err))
			}
			addCommands(out, cmds)
		}
	}
	return out, warnings
}

// unreadableConfig is the warning for a config file the collision check
// couldn't parse: JSONC with comments or trailing commas is valid to opencode
// but not to encoding/json.
func unreadableConfig(path string, err error) string {
	return fmt.Sprintf("couldn't read %s (%v): generated skill commands may shadow commands it defines", path, err)
}

// commandFiles lists the .md command files in dir by name, with the file
// defining each. Symlinks are followed: opencode reads the file they point
// at. Nested files name commands with a "/" separator, which a skill name
// never contains, so they cannot collide.
func commandFiles(dir string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out[strings.TrimSuffix(name, ".md")] = p
	}
	return out
}

// configCommands reads the commands map from an opencode config file. A
// missing file is not an error.
func configCommands(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var cfg struct {
		Commands map[string]json.RawMessage `json:"commands"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(cfg.Commands))
	for name := range cfg.Commands {
		out[name] = path
	}
	return out, nil
}

// addCommands merges src into dst, keeping dst's (nearer) definitions.
func addCommands(dst, src map[string]string) {
	for name, p := range src {
		if _, ok := dst[name]; !ok {
			dst[name] = p
		}
	}
}

// writeCommandFiles writes a command markdown file per name into dir's
// opencode/commands (creating it): the sandbox's staged ~/.config, where
// opencode finds them as global commands.
func writeCommandFiles(dir string, names []string) error {
	cmds := filepath.Join(dir, "opencode", "commands")
	if err := os.MkdirAll(cmds, 0o755); err != nil {
		return err
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(cmds, n+".md"), []byte(commandMarkdown(n)), 0o644); err != nil {
			return err
		}
	}
	return nil
}
