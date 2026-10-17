package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	got, warns := DiscoverAll(e.home, e.cwd)
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
	got, warns := DiscoverAll(e.home, e.cwd)
	if len(got) != 0 || len(warns) != 0 {
		t.Errorf("DiscoverAll = %v, %v; want nothing", names(got), warns)
	}
}

// write creates the file rel under the working directory (or, for the
// default skill folders, under home), with parents.
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

func TestDiscoverAllPrefersProjectSkills(t *testing.T) {
	e := newEnv(t)
	// The project's own skill wins over the global one of the same name, as
	// in opencode's precedence.
	e.skill(e.opencode(), "shared")
	e.skill(filepath.Join(e.cwd, ".opencode", "skills"), "shared")
	e.skill(filepath.Join(e.cwd, ".claude", "skills"), "local")

	got, warns := DiscoverAll(e.home, e.cwd)
	if names(got) != "local,shared" {
		t.Errorf("DiscoverAll = %v, want local,shared", names(got))
	}
	if got[1].Source != filepath.Join(e.cwd, ".opencode", "skills") {
		t.Errorf("shared's source = %q, want the project's folder", got[1].Source)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "shared") || !strings.Contains(warns[0], "shadowing") {
		t.Errorf("warnings = %q, want one about the shadowed global shared skill", warns)
	}
}

func TestDiscoverAllHonorsOpencodeConfigDir(t *testing.T) {
	e := newEnv(t)
	custom := filepath.Join(e.home, "custom-config")
	e.skill(filepath.Join(custom, "skills"), "custom")
	// The default global folder is ignored while OPENCODE_CONFIG_DIR is set,
	// since opencode reads skills from the configured directory instead.
	e.skill(e.opencode(), "default")
	// The compatibility folders are still read.
	e.skill(e.claude(), "compat")
	t.Setenv("OPENCODE_CONFIG_DIR", custom)

	got, _ := DiscoverAll(e.home, e.cwd)
	if names(got) != "compat,custom" {
		t.Errorf("DiscoverAll = %v, want compat,custom", names(got))
	}
}
