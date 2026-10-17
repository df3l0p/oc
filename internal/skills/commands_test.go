package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandTemplateHasNoArgumentsPlaceholder(t *testing.T) {
	got := CommandTemplate("review")
	want := "Load the `review` skill with the skill tool and follow its instructions."
	if got != want {
		t.Errorf("CommandTemplate = %q, want %q", got, want)
	}
	// opencode appends arguments after a blank line only when the template
	// has no placeholder, which is what keeps "/review" with no arguments
	// a valid command.
	if strings.Contains(got, "$ARGUMENTS") || strings.Contains(got, "$1") {
		t.Errorf("CommandTemplate contains an arguments placeholder: %q", got)
	}
}

func TestCommandMarkdown(t *testing.T) {
	got := CommandMarkdown("review")
	want := "---\ndescription: Run the review skill\n---\n\n" + CommandTemplate("review") + "\n"
	if got != want {
		t.Errorf("CommandMarkdown = %q, want %q", got, want)
	}
	if strings.Contains(got, "template:") {
		// The body is the template; frontmatter must not carry one.
		t.Errorf("CommandMarkdown puts template in frontmatter: %q", got)
	}
}

func TestConfigContent(t *testing.T) {
	if got := ConfigContent(nil); got != "" {
		t.Errorf("ConfigContent(nil) = %q, want empty", got)
	}
	raw := ConfigContent([]string{"alpha", "beta"})
	var got struct {
		Commands map[string]struct {
			Description string `json:"description"`
			Template    string `json:"template"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("ConfigContent is not valid JSON: %v\n%s", err, raw)
	}
	if len(got.Commands) != 2 {
		t.Errorf("commands = %v, want one per skill", got.Commands)
	}
	for _, name := range []string{"alpha", "beta"} {
		def, ok := got.Commands[name]
		if !ok {
			t.Errorf("no command for %q in %s", name, raw)
			continue
		}
		if def.Description != "Run the "+name+" skill" {
			t.Errorf("description for %q = %q", name, def.Description)
		}
		if def.Template != CommandTemplate(name) {
			t.Errorf("template for %q = %q, want %q", name, def.Template, CommandTemplate(name))
		}
	}
}

func TestCommandNamesSkipsProjectCommands(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(e env)
		want    string // the names kept, comma-joined
		warnSub string // a substring every warning must have, or ""
	}{
		{"no project commands", func(e env) {}, "alpha", ""},
		{"project command file", func(e env) { e.command(".opencode/commands/alpha.md") }, "", ".opencode/commands/alpha.md"},
		{"legacy command file", func(e env) { e.command(".opencode/command/alpha.md") }, "", ".opencode/command/alpha.md"},
		{"nested command cannot collide", func(e env) { e.command(".opencode/commands/team/alpha.md") }, "alpha", ""},
		{"non-markdown is ignored", func(e env) { e.command(".opencode/commands/alpha.txt") }, "alpha", ""},
		{"hidden file is ignored", func(e env) { e.command(".opencode/commands/.alpha.md") }, "alpha", ""},
		{"json config command", func(e env) {
			e.write("opencode.json", `{"commands":{"alpha":{"template":"x"}}}`)
		}, "", "opencode.json"},
		{"jsonc config command in .opencode", func(e env) {
			e.write(".opencode/opencode.json", `{"commands":{"alpha":{"template":"x"}}}`)
		}, "", "opencode.json"},
		{"jsonc with comments is skipped", func(e env) {
			e.write("opencode.jsonc", "{// note\n\"commands\":{\"alpha\":{\"template\":\"x\"}}}")
		}, "alpha", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fresh := newEnv(t)
			fresh.skill(fresh.opencode(), "alpha")
			tt.setup(fresh)
			got, warns := CommandNames(fresh.cwd, []string{"alpha"})
			if names2(got) != tt.want {
				t.Errorf("CommandNames kept %q, want %q", names2(got), tt.want)
			}
			if tt.warnSub == "" {
				if len(warns) != 0 {
					t.Errorf("warnings = %q, want none", warns)
				}
				return
			}
			if len(warns) != 1 || !strings.Contains(warns[0], tt.warnSub) || !strings.Contains(warns[0], "/alpha") {
				t.Errorf("warnings = %q, want one mentioning /alpha and %q", warns, tt.warnSub)
			}
		})
	}
}

func TestCommandNamesKeepsUnrelatedSkills(t *testing.T) {
	e := newEnv(t)
	e.command(".opencode/commands/review.md")
	got, warns := CommandNames(e.cwd, []string{"alpha", "review", "beta"})
	if names2(got) != "alpha,beta" {
		t.Errorf("kept %q, want alpha,beta", names2(got))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "/review") {
		t.Errorf("warnings = %q, want one about /review", warns)
	}
}

func TestWriteCommandFiles(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCommandFiles(dir, []string{"alpha", "beta"}); err != nil {
		t.Fatalf("WriteCommandFiles: %v", err)
	}
	for _, n := range []string{"alpha", "beta"} {
		raw, err := os.ReadFile(filepath.Join(dir, "opencode", "commands", n+".md"))
		if err != nil {
			t.Fatalf("reading %s command: %v", n, err)
		}
		if string(raw) != CommandMarkdown(n) {
			t.Errorf("%s command = %q, want %q", n, raw, CommandMarkdown(n))
		}
	}
	// Writing again into the same directory must not fail.
	if err := WriteCommandFiles(dir, []string{"alpha"}); err != nil {
		t.Fatalf("second WriteCommandFiles: %v", err)
	}
}

func TestDiscoverAll(t *testing.T) {
	e := newEnv(t)
	// alpha is in two sources: the first one (opencode's own) wins.
	e.skill(e.opencode(), "alpha")
	e.skill(e.agents(), "alpha")
	e.skill(e.agents(), "beta")
	e.skill(e.claude(), "gamma")
	// Not a skill: no SKILL.md. Skipped with a warning.
	broken := filepath.Join(e.claude(), "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	// Hidden entries and plain files are not skills.
	if err := os.MkdirAll(filepath.Join(e.opencode(), ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.write(filepath.Join(".config", "opencode", "skills", "loose.md"), "x")

	got, warns := DiscoverAll(e.home)
	if names(got) != "alpha,beta,gamma" {
		t.Errorf("DiscoverAll = %v, want alpha,beta,gamma", names(got))
	}
	if got[0].Source != e.opencode() {
		t.Errorf("alpha's source = %q, want the opencode folder (first source wins)", got[0].Source)
	}
	if len(warns) != 2 {
		t.Fatalf("warnings = %q, want the shadowed alpha and the broken entry", warns)
	}
	if !strings.Contains(warns[0], "alpha") || !strings.Contains(warns[0], "shadowing") {
		t.Errorf("first warning = %q, want the shadowed alpha", warns[0])
	}
	if !strings.Contains(warns[1], "broken") || !strings.Contains(warns[1], "SKILL.md") {
		t.Errorf("second warning = %q, want the broken entry's skipped warning", warns[1])
	}
}

func TestDiscoverAllWithNoFoldersIsEmpty(t *testing.T) {
	e := newEnv(t)
	got, warns := DiscoverAll(e.home)
	if len(got) != 0 || len(warns) != 0 {
		t.Errorf("DiscoverAll = %v, %v; want nothing", names(got), warns)
	}
}

// command creates a project command file at rel under the working directory,
// creating parent directories.
func (e env) command(rel string) {
	e.write(rel, "---\ndescription: test\n---\n\nBody.\n")
}

// write creates the file rel under the working directory (or a path relative
// to home when it starts with .config, .agents or .claude), with parents.
func (e env) write(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.cwd, rel)
	if strings.HasPrefix(rel, ".config/") || strings.HasPrefix(rel, ".agents/") || strings.HasPrefix(rel, ".claude/") {
		p = filepath.Join(e.home, rel)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// names2 is names for plain name slices.
func names2(names []string) string {
	return strings.Join(names, ",")
}
