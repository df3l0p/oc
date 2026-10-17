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
	// Not a skill: no SKILL.md. Skipped, and nobody is told: nothing was asked
	// for, so a plain launch must stay quiet about it.
	if err := os.MkdirAll(filepath.Join(e.claude(), "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Neither are hidden entries, plain files, or a name opencode's skill
	// rules reject.
	if err := os.MkdirAll(filepath.Join(e.opencode(), ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.write(filepath.Join(".config", "opencode", "skills", "loose.md"), "x")
	e.skill(e.opencode(), "Not_A_Valid_Name")

	got := DiscoverAll(e.home, e.cwd)
	if names(got) != "alpha,beta,gamma" {
		t.Errorf("DiscoverAll = %v, want alpha,beta,gamma", names(got))
	}
	if got[0].Source != e.opencode() {
		t.Errorf("alpha's source = %q, want the opencode folder (first source wins)", got[0].Source)
	}
}

func TestDiscoverAllWithNoFoldersIsEmpty(t *testing.T) {
	e := newEnv(t)
	if got := DiscoverAll(e.home, e.cwd); len(got) != 0 {
		t.Errorf("DiscoverAll = %v; want nothing", names(got))
	}
}

func TestDiscoverAllPrefersProjectSkills(t *testing.T) {
	e := newEnv(t)
	// The project's own skill wins over the global one of the same name, as
	// in opencode's precedence.
	e.skill(e.opencode(), "shared")
	e.skill(filepath.Join(e.cwd, ".opencode", "skills"), "shared")
	e.skill(filepath.Join(e.cwd, ".claude", "skills"), "local")

	got := DiscoverAll(e.home, e.cwd)
	if names(got) != "local,shared" {
		t.Errorf("DiscoverAll = %v, want local,shared", names(got))
	}
	if got[1].Source != filepath.Join(e.cwd, ".opencode", "skills") {
		t.Errorf("shared's source = %q, want the project's folder", got[1].Source)
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

	got := DiscoverAll(e.home, e.cwd)
	if names(got) != "compat,custom" {
		t.Errorf("DiscoverAll = %v, want compat,custom", names(got))
	}
}

func TestDiscoverAllReadsTheLegacySingularFolders(t *testing.T) {
	e := newEnv(t)
	e.skill(filepath.Join(e.home, ".config", "opencode", "skill"), "global-legacy")
	e.skill(filepath.Join(e.cwd, ".opencode", "skill"), "project-legacy")

	got := DiscoverAll(e.home, e.cwd)
	if names(got) != "global-legacy,project-legacy" {
		t.Errorf("DiscoverAll = %v, want global-legacy,project-legacy", names(got))
	}
}

func TestDiscoverAllWalksUpToTheRepositoryRoot(t *testing.T) {
	e := newEnv(t)
	repo := filepath.Join(e.cwd, "repo")
	sub := filepath.Join(repo, "packages", "web")
	for _, d := range []string{filepath.Join(repo, ".git"), sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.skill(filepath.Join(repo, ".opencode", "skills"), "root-skill")
	e.skill(filepath.Join(repo, ".opencode", "skills"), "shared")
	e.skill(filepath.Join(sub, ".opencode", "skills"), "shared")
	// Above the repository root is not the project.
	e.skill(filepath.Join(e.cwd, ".opencode", "skills"), "outside")

	got := DiscoverAll(e.home, sub)
	if names(got) != "root-skill,shared" {
		t.Errorf("DiscoverAll = %v, want root-skill,shared", names(got))
	}
	// The nearer definition wins a name.
	if got[1].Source != filepath.Join(sub, ".opencode", "skills") {
		t.Errorf("shared's source = %q, want the working directory's folder", got[1].Source)
	}
}

func TestDiscoverAllWithoutARepositoryChecksOnlyTheWorkingDirectory(t *testing.T) {
	e := newEnv(t)
	sub := filepath.Join(e.cwd, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	e.skill(filepath.Join(e.cwd, ".opencode", "skills"), "parent")
	e.skill(filepath.Join(sub, ".opencode", "skills"), "here")

	if got := DiscoverAll(e.home, sub); names(got) != "here" {
		t.Errorf("DiscoverAll = %v, want only here", names(got))
	}
}

func TestDiscoverProjectIsTheWorkingDirectoryOnly(t *testing.T) {
	e := newEnv(t)
	repo := filepath.Join(e.cwd, "repo")
	sub := filepath.Join(repo, "sub")
	for _, d := range []string{filepath.Join(repo, ".git"), sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.skill(filepath.Join(repo, ".opencode", "skills"), "root-skill")
	e.skill(filepath.Join(sub, ".agents", "skills"), "here")
	// Global skills aren't the project's.
	e.skill(e.opencode(), "global")

	// The sandbox mounts only the working directory, so a parent's skills
	// aren't there.
	if got := DiscoverProject(sub); names(got) != "here" {
		t.Errorf("DiscoverProject = %v, want only here", names(got))
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
