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
		name     string
		setup    func(cwd string)
		want     string   // the names kept, comma-joined
		warnSubs []string // substrings that must each appear in some warning
	}{
		{"no project commands", func(string) {}, "alpha", nil},
		{"project command file", func(cwd string) { writeProjectFile(t, cwd, ".opencode/commands/alpha.md") }, "", []string{".opencode/commands/alpha.md", "/alpha"}},
		{"legacy command file", func(cwd string) { writeProjectFile(t, cwd, ".opencode/command/alpha.md") }, "", []string{".opencode/command/alpha.md", "/alpha"}},
		{"nested command cannot collide", func(cwd string) { writeProjectFile(t, cwd, ".opencode/commands/team/alpha.md") }, "alpha", nil},
		{"non-markdown is ignored", func(cwd string) { writeProjectFile(t, cwd, ".opencode/commands/alpha.txt") }, "alpha", nil},
		{"hidden file is ignored", func(cwd string) { writeProjectFile(t, cwd, ".opencode/commands/.alpha.md") }, "alpha", nil},
		{"json config command", func(cwd string) {
			writeProjectFile(t, cwd, "opencode.json")
		}, "", []string{"opencode.json", "/alpha"}},
		{"jsonc config command in .opencode", func(cwd string) {
			writeProjectFile(t, cwd, ".opencode/opencode.json")
		}, "", []string{"opencode.json", "/alpha"}},
		{"jsonc with comments is skipped with a warning", func(cwd string) {
			writeProjectFile(t, cwd, "opencode.jsonc")
		}, "alpha", []string{"opencode.jsonc", "couldn't read"}},
		{"jsonc with a trailing comma is skipped with a warning", func(cwd string) {
			writeFile(t, filepath.Join(cwd, "opencode.jsonc"), `{"commands":{"alpha":{"template":"x",},},}`)
		}, "alpha", []string{"opencode.jsonc", "couldn't read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			tt.setup(cwd)
			got, warns := commandNames(commandScope{cwd: cwd}, []string{"alpha"})
			if strings.Join(got, ",") != tt.want {
				t.Errorf("commandNames kept %q, want %q", strings.Join(got, ","), tt.want)
			}
			if len(tt.warnSubs) == 0 {
				if len(warns) != 0 {
					t.Errorf("warnings = %q, want none", warns)
				}
				return
			}
			joined := strings.Join(warns, "\n")
			for _, sub := range tt.warnSubs {
				if !strings.Contains(joined, sub) {
					t.Errorf("warnings = %q, want one containing %q", warns, sub)
				}
			}
		})
	}
}

func TestCommandNamesSkipsBuiltins(t *testing.T) {
	cwd := t.TempDir()
	got, warns := commandNames(commandScope{cwd: cwd}, []string{"alpha", "models", "undo"})
	if strings.Join(got, ",") != "alpha" {
		t.Errorf("kept %q, want alpha", strings.Join(got, ","))
	}
	joined := strings.Join(warns, "\n")
	for _, n := range []string{"/models", "/undo", "built-in"} {
		if !strings.Contains(joined, n) {
			t.Errorf("warnings = %q, want one mentioning %q", warns, n)
		}
	}
}

func TestCommandNamesWalksAncestors(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, ".opencode/commands/alpha.md")
	cwd := filepath.Join(root, "packages", "web")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	got, warns := commandNames(commandScope{cwd: cwd}, []string{"alpha"})
	if len(got) != 0 {
		t.Errorf("kept %q, want none: the ancestor defines /alpha", strings.Join(got, ","))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], filepath.Join(".opencode", "commands", "alpha.md")) {
		t.Errorf("warnings = %q, want one naming the ancestor's file", warns)
	}
}

func TestCommandNamesSandboxScopeIgnoresHostAncestors(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, ".opencode/commands/alpha.md")
	cwd := filepath.Join(root, "packages", "web")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only the workdir is mounted in the container, so the ancestor's command
	// is not visible there and must not suppress the generated one.
	got, warns := commandNames(commandScope{cwd: cwd, stop: cwd}, []string{"alpha"})
	if strings.Join(got, ",") != "alpha" || len(warns) != 0 {
		t.Errorf("kept %q, warnings %q; want alpha and none", strings.Join(got, ","), warns)
	}
}

func TestCommandNamesIncludesGlobalCommands(t *testing.T) {
	cwd := t.TempDir()
	global := t.TempDir()
	writeProjectFile(t, global, "commands/alpha.md")
	writeProjectFile(t, global, "opencode.json")

	got, warns := commandNames(commandScope{cwd: cwd, globalDir: global}, []string{"alpha", "beta"})
	if strings.Join(got, ",") != "beta" {
		t.Errorf("kept %q, want beta", strings.Join(got, ","))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "/alpha") {
		t.Errorf("warnings = %q, want one about /alpha", warns)
	}

	// Without a global dir (the sandbox), the same commands don't collide.
	got, warns = commandNames(commandScope{cwd: cwd}, []string{"alpha"})
	if strings.Join(got, ",") != "alpha" || len(warns) != 0 {
		t.Errorf("kept %q, warnings %q; want alpha and none", strings.Join(got, ","), warns)
	}
}

func TestCommandFilesFollowSymlinks(t *testing.T) {
	cwd := t.TempDir()
	shared := filepath.Join(t.TempDir(), "alpha.md")
	if err := os.WriteFile(shared, []byte("shared"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cwd, ".opencode", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(dir, "alpha.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, _ := commandNames(commandScope{cwd: cwd}, []string{"alpha"})
	if len(got) != 0 {
		t.Errorf("kept %q, want none: the symlinked command defines /alpha", strings.Join(got, ","))
	}
}

func TestCommandNamesKeepsUnrelatedSkills(t *testing.T) {
	cwd := t.TempDir()
	writeProjectFile(t, cwd, ".opencode/commands/review.md")
	got, warns := commandNames(commandScope{cwd: cwd}, []string{"alpha", "review", "beta"})
	if strings.Join(got, ",") != "alpha,beta" {
		t.Errorf("kept %q, want alpha,beta", strings.Join(got, ","))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "/review") {
		t.Errorf("warnings = %q, want one about /review", warns)
	}
}

func TestMergeConfigContent(t *testing.T) {
	existing := `{"model":"a/b","commands":{"mine":{"template":"keep"},"alpha":{"template":"theirs"}}}`
	out, err := mergeConfigContent(existing, []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Model    string
		Commands map[string]commandDef
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Model != "a/b" {
		t.Errorf("model = %q, want the user's a/b", got.Model)
	}
	if got.Commands["mine"].Template != "keep" {
		t.Errorf("the user's /mine changed: %s", out)
	}
	if got.Commands["alpha"].Template != "theirs" {
		t.Errorf("the user's own /alpha was replaced: %s", out)
	}
	if got.Commands["beta"] != newCommandDef("beta") {
		t.Errorf("/beta wasn't added: %s", out)
	}
}

func TestMergeConfigContentIntoConfigsWithoutCommands(t *testing.T) {
	for _, existing := range []string{`{}`, `null`, `{"commands":null}`, `{"model":"a/b"}`} {
		out, err := mergeConfigContent(existing, []string{"alpha"})
		if err != nil {
			t.Errorf("%s: %v", existing, err)
			continue
		}
		var got struct{ Commands map[string]commandDef }
		if err := json.Unmarshal([]byte(out), &got); err != nil || got.Commands["alpha"] != newCommandDef("alpha") {
			t.Errorf("%s: merged = %s (%v), want /alpha added", existing, out, err)
		}
	}
}

func TestParseInlineRejectsWhatItCannotRead(t *testing.T) {
	for _, bad := range []string{``, `// c\n{}`, `{"a":1,}`, `[]`, `"x"`, `{"commands":"nope"}`, `{"commands":[]}`} {
		if _, _, err := parseInline(bad); err == nil {
			t.Errorf("parseInline(%q) succeeded, want an error", bad)
		}
	}
}

func TestCommandNamesSkipsCommandsInTheScopesExtra(t *testing.T) {
	scope := commandScope{cwd: t.TempDir(), extra: map[string]string{"alpha": "OPENCODE_CONFIG_CONTENT"}}
	got, warns := commandNames(scope, []string{"alpha", "beta"})
	if strings.Join(got, ",") != "beta" {
		t.Errorf("kept %q, want beta", strings.Join(got, ","))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "OPENCODE_CONFIG_CONTENT") || !strings.Contains(warns[0], "/alpha") {
		t.Errorf("warnings = %q, want one naming OPENCODE_CONFIG_CONTENT and /alpha", warns)
	}
}

func TestWithEnvReplacesInsteadOfDuplicating(t *testing.T) {
	env := withEnv([]string{"A=1", "K=old", "K_OTHER=keep", "K=older"}, "K", "new")
	if v, n := envValue(env, "K"); v != "new" || n != 1 {
		t.Errorf("K = %q (%d entries), want exactly one, new", v, n)
	}
	if !hasEnv(env, "A=1") || !hasEnv(env, "K_OTHER=keep") {
		t.Errorf("env = %q, want the other entries kept (K_OTHER shares only a prefix)", env)
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
	content := "---\ndescription: test\n---\n\nBody.\n"
	switch filepath.Base(rel) {
	case "opencode.json":
		content = `{"commands":{"alpha":{"template":"x"}}}`
	case "opencode.jsonc":
		content = "{// note\n\"commands\":{\"alpha\":{\"template\":\"x\"}}}"
	}
	writeFile(t, filepath.Join(cwd, rel), content)
}

// writeFile creates path with parents.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
