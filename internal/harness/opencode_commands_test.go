package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandTemplateHasNoArgumentsPlaceholder(t *testing.T) {
	got := commandTemplate("review")
	want := "Load the `review` skill with the skill tool and follow its instructions."
	if got != want {
		t.Errorf("commandTemplate = %q, want %q", got, want)
	}
	// opencode appends arguments after a blank line only when the template
	// has no placeholder, which is what keeps "/review" with no arguments
	// a valid command.
	if strings.Contains(got, "$ARGUMENTS") || strings.Contains(got, "$1") {
		t.Errorf("commandTemplate contains an arguments placeholder: %q", got)
	}
}

func TestCommandMarkdown(t *testing.T) {
	got := commandMarkdown("review")
	want := "---\ndescription: Run the review skill\n---\n\n" + commandTemplate("review") + "\n"
	if got != want {
		t.Errorf("commandMarkdown = %q, want %q", got, want)
	}
	if strings.Contains(got, "template:") {
		// The body is the template; frontmatter must not carry one.
		t.Errorf("commandMarkdown puts template in frontmatter: %q", got)
	}
}

func TestConfigContent(t *testing.T) {
	if got := configContent(nil); got != "" {
		t.Errorf("configContent(nil) = %q, want empty", got)
	}
	raw := configContent([]string{"alpha", "beta"})
	var got struct {
		Commands map[string]struct {
			Description string `json:"description"`
			Template    string `json:"template"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("configContent is not valid JSON: %v\n%s", err, raw)
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
		if def.Template != commandTemplate(name) {
			t.Errorf("template for %q = %q, want %q", name, def.Template, commandTemplate(name))
		}
	}
}

func TestCommandNamesSkipsProjectCommands(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(cwd string)
		want    string // the names kept, comma-joined
		warnSub string // a substring every warning must have, or ""
	}{
		{"no project commands", func(string) {}, "alpha", ""},
		{"project command file", func(cwd string) { writeProjectFile(t, cwd, ".opencode/commands/alpha.md") }, "", ".opencode/commands/alpha.md"},
		{"legacy command file", func(cwd string) { writeProjectFile(t, cwd, ".opencode/command/alpha.md") }, "", ".opencode/command/alpha.md"},
		{"nested command cannot collide", func(cwd string) { writeProjectFile(t, cwd, ".opencode/commands/team/alpha.md") }, "alpha", ""},
		{"non-markdown is ignored", func(cwd string) { writeProjectFile(t, cwd, ".opencode/commands/alpha.txt") }, "alpha", ""},
		{"hidden file is ignored", func(cwd string) { writeProjectFile(t, cwd, ".opencode/commands/.alpha.md") }, "alpha", ""},
		{"json config command", func(cwd string) {
			writeProjectFile(t, cwd, "opencode.json")
		}, "", "opencode.json"},
		{"jsonc config command in .opencode", func(cwd string) {
			writeProjectFile(t, cwd, ".opencode/opencode.json")
		}, "", "opencode.json"},
		{"jsonc with comments is skipped", func(cwd string) {
			writeProjectFile(t, cwd, "opencode.jsonc")
		}, "alpha", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			tt.setup(cwd)
			got, warns := commandNames(cwd, []string{"alpha"})
			if strings.Join(got, ",") != tt.want {
				t.Errorf("commandNames kept %q, want %q", strings.Join(got, ","), tt.want)
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
	cwd := t.TempDir()
	writeProjectFile(t, cwd, ".opencode/commands/review.md")
	got, warns := commandNames(cwd, []string{"alpha", "review", "beta"})
	if strings.Join(got, ",") != "alpha,beta" {
		t.Errorf("kept %q, want alpha,beta", strings.Join(got, ","))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "/review") {
		t.Errorf("warnings = %q, want one about /review", warns)
	}
}

func TestWriteCommandFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeCommandFiles(dir, []string{"alpha", "beta"}); err != nil {
		t.Fatalf("writeCommandFiles: %v", err)
	}
	for _, n := range []string{"alpha", "beta"} {
		raw, err := os.ReadFile(filepath.Join(dir, "opencode", "commands", n+".md"))
		if err != nil {
			t.Fatalf("reading %s command: %v", n, err)
		}
		if string(raw) != commandMarkdown(n) {
			t.Errorf("%s command = %q, want %q", n, raw, commandMarkdown(n))
		}
	}
	// Writing again into the same directory must not fail.
	if err := writeCommandFiles(dir, []string{"alpha"}); err != nil {
		t.Fatalf("second writeCommandFiles: %v", err)
	}
}

// writeProjectFile creates a file in a project directory, with parents. A
// .md file gets command frontmatter and body; the config files get a commands
// map, except opencode.jsonc, which gets a comment so it exercises the
// "JSONC with comments is skipped" case.
func writeProjectFile(t *testing.T, cwd, rel string) {
	t.Helper()
	p := filepath.Join(cwd, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ndescription: test\n---\n\nBody.\n"
	switch filepath.Base(rel) {
	case "opencode.json":
		content = `{"commands":{"alpha":{"template":"x"}}}`
	case "opencode.jsonc":
		content = "{// note\n\"commands\":{\"alpha\":{\"template\":\"x\"}}}"
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
