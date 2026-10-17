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
	"fmt"
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

// configContent is the inline opencode config defining a command per name,
// for OPENCODE_CONFIG_CONTENT on the host. Empty for no names.
func configContent(names []string) string {
	if len(names) == 0 {
		return ""
	}
	type def struct {
		Description string `json:"description"`
		Template    string `json:"template"`
	}
	commands := make(map[string]def, len(names))
	for _, n := range names {
		commands[n] = def{Description: "Run the " + n + " skill", Template: commandTemplate(n)}
	}
	out, err := json.Marshal(map[string]map[string]def{"commands": commands})
	if err != nil {
		return "" // unreachable: plain JSON data
	}
	return string(out)
}

// commandNames picks the names worth a generated command. One the project
// at cwd already defines as a command is dropped with a warning: on the
// host the generated commands merge above every file-based config and
// would override the project's own command, so rnes leaves it alone (the
// sandbox's generated commands rank below project commands anyway, but the
// skip keeps both places behaving the same). The skill itself is unaffected;
// the agent can still load it by name.
func commandNames(cwd string, names []string) ([]string, []string) {
	proj := projectCommands(cwd)
	var out, warnings []string
	for _, n := range names {
		if p, ok := proj[n]; ok {
			warnings = append(warnings, fmt.Sprintf("skill command /%s skipped: the project already defines a command with that name (%s); the skill itself is still available to the agent", n, p))
			continue
		}
		out = append(out, n)
	}
	return out, warnings
}

// projectCommands lists the commands the project at cwd defines, by name
// with the file defining each: root-level .md files in .opencode/commands
// (and the legacy .opencode/command), plus the commands map of the project's
// opencode.json(c). Nested files name commands with a "/" separator, which a
// skill name never contains, so they cannot collide. Only cwd is searched,
// not its ancestors, so the check is about this session's directory; a
// JSONC file with comments fails plain JSON parsing and is skipped.
func projectCommands(cwd string) map[string]string {
	out := map[string]string{}
	for _, sub := range []string{"commands", "command"} {
		dir := filepath.Join(cwd, ".opencode", sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.Type().IsRegular() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
				continue
			}
			out[strings.TrimSuffix(name, ".md")] = filepath.Join(dir, name)
		}
	}
	configs := []string{"opencode.json", "opencode.jsonc",
		filepath.Join(".opencode", "opencode.json"), filepath.Join(".opencode", "opencode.jsonc")}
	for _, f := range configs {
		raw, err := os.ReadFile(filepath.Join(cwd, f))
		if err != nil {
			continue
		}
		var cfg struct {
			Commands map[string]json.RawMessage `json:"commands"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			continue
		}
		for name := range cfg.Commands {
			out[name] = filepath.Join(cwd, f)
		}
	}
	return out
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
