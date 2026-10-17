package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// env is a fake host: a home directory and a working directory.
type env struct {
	t    *testing.T
	home string
	cwd  string
}

func newEnv(t *testing.T) env {
	t.Helper()
	// Discovery reads this variable; a developer's own must not leak in.
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	root := t.TempDir()
	// Resolved, so the paths Resolve reports (symlinks resolved) compare equal
	// on systems where the temp dir is itself behind a symlink (macOS).
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	e := env{t: t, home: filepath.Join(root, "home"), cwd: filepath.Join(root, "proj")}
	for _, d := range []string{e.home, e.cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// skill creates dir/name/SKILL.md and returns the skill's directory.
func (e env) skill(dir, name string) string {
	e.t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: test\n---\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e env) opencode() string { return filepath.Join(e.home, ".config", "opencode", "skills") }
func (e env) agents() string   { return filepath.Join(e.home, ".agents", "skills") }
func (e env) claude() string   { return filepath.Join(e.home, ".claude", "skills") }

func (e env) resolve(values []string, dirs ...string) ([]Skill, []string, error) {
	return Resolve(Request{Values: values, Dirs: dirs, Home: e.home, Cwd: e.cwd})
}

func names(skills []Skill) string {
	var n []string
	for _, s := range skills {
		n = append(n, s.Name)
	}
	return strings.Join(n, ",")
}

func TestNoValuesAttachNothingAndReadNothing(t *testing.T) {
	e := newEnv(t)
	e.skill(e.opencode(), "foo")
	// An unreadable default folder would fail a lookup; with no -skill it must
	// not even be looked at.
	if err := os.Chmod(e.opencode(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(e.opencode(), 0o755) })
	got, warns, err := e.resolve(nil)
	if err != nil || len(got) != 0 || len(warns) != 0 {
		t.Errorf("Resolve(nil) = %v, %v, %v; want nothing", got, warns, err)
	}
}

func TestNameResolvesInSourceOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source func(env) string
	}{
		{"opencode", env.opencode},
		{"agents", env.agents},
		{"claude", env.claude},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			src := tc.source(e)
			dir := e.skill(src, "foo")
			e.skill(src, "bar") // not requested
			got, warns, err := e.resolve([]string{"foo"})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Name != "foo" || got[0].Dir != dir || got[0].Source != src {
				t.Errorf("got %+v, want foo from %s", got, src)
			}
			if len(warns) != 0 {
				t.Errorf("warnings = %v", warns)
			}
		})
	}
}

func TestSkillsDirIsSearchedFirstAndInOrder(t *testing.T) {
	e := newEnv(t)
	d1, d2 := filepath.Join(e.home, "d1"), filepath.Join(e.home, "d2")
	e.skill(d1, "a")
	e.skill(d2, "b")
	e.skill(e.opencode(), "c")
	got, _, err := e.resolve([]string{"a", "b", "c"}, d1, d2)
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "a,b,c" || got[0].Source != d1 || got[1].Source != d2 || got[2].Source != e.opencode() {
		t.Errorf("got %+v", got)
	}
}

func TestSkillsDirAcceptsTildeAndRelativePaths(t *testing.T) {
	e := newEnv(t)
	e.skill(filepath.Join(e.home, "mine"), "a")
	e.skill(filepath.Join(e.cwd, "local"), "b")
	got, _, err := e.resolve([]string{"a", "b"}, "~/mine", "local")
	if err != nil || names(got) != "a,b" {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestAmbiguousNameErrorsWithEveryCandidate(t *testing.T) {
	e := newEnv(t)
	d := filepath.Join(e.home, "extra")
	a := e.skill(d, "foo")
	b := e.skill(e.opencode(), "foo")
	c := e.skill(e.claude(), "foo")
	_, _, err := e.resolve([]string{"foo"}, d)
	if err == nil {
		t.Fatal("want an ambiguity error")
	}
	for _, want := range []string{a, b, c, "-skill <path>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}

	// A path picks one.
	got, _, err := e.resolve([]string{c}, d)
	if err != nil || len(got) != 1 || got[0].Dir != c {
		t.Errorf("path form: %+v, %v; want %s", got, err, c)
	}
}

func TestTheSameDirectoryInTwoSourcesIsNotAmbiguous(t *testing.T) {
	e := newEnv(t)
	real := e.skill(filepath.Join(e.home, "dotfiles"), "foo")
	if err := os.MkdirAll(e.claude(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(e.claude(), "foo")); err != nil {
		t.Fatal(err)
	}
	// -skills-dir naming a default folder again doesn't make it two sources.
	got, _, err := e.resolve([]string{"foo"}, filepath.Join(e.home, "dotfiles"), e.claude())
	if err != nil || len(got) != 1 || got[0].Dir != real {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestPathForms(t *testing.T) {
	e := newEnv(t)
	abs := e.skill(filepath.Join(e.home, "x"), "abs-one")
	e.skill(filepath.Join(e.home, "y"), "tilde-one")
	rel := e.skill(filepath.Join(e.cwd, "z"), "rel-one")
	got, _, err := e.resolve([]string{abs, "~/y/tilde-one", "z/rel-one"})
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "abs-one,rel-one,tilde-one" || got[1].Dir != rel || got[1].Source != filepath.Join(e.cwd, "z") {
		t.Errorf("got %+v", got)
	}
}

func TestAllTakesTheFirstSourceAndWarnsOncePerShadowedSkill(t *testing.T) {
	e := newEnv(t)
	win := e.skill(e.opencode(), "foo")
	shadowA := e.skill(e.agents(), "foo")
	shadowC := e.skill(e.claude(), "foo")
	e.skill(e.agents(), "bar")
	e.skill(e.claude(), "baz")
	got, warns, err := e.resolve([]string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "bar,baz,foo" {
		t.Errorf("got %v", names(got))
	}
	for _, s := range got {
		if s.Name == "foo" && s.Dir != win {
			t.Errorf("foo = %s, want the first source's %s", s.Dir, win)
		}
	}
	if len(warns) != 2 {
		t.Fatalf("want one warning per shadowed skill, got %q", warns)
	}
	for i, shadowed := range []string{shadowA, shadowC} {
		if !strings.Contains(warns[i], win) || !strings.Contains(warns[i], shadowed) {
			t.Errorf("warning %q should name the winner %s and the shadowed %s", warns[i], win, shadowed)
		}
	}
}

func TestDuplicateRequestsCollapse(t *testing.T) {
	e := newEnv(t)
	dir := e.skill(e.opencode(), "foo")
	e.skill(e.opencode(), "bar")
	got, warns, err := e.resolve([]string{"foo", "foo", dir, "all", "all"})
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "bar,foo" || len(warns) != 0 {
		t.Errorf("got %v, warnings %q", names(got), warns)
	}
}

func TestAnExplicitPathWinsOverAllWithAWarning(t *testing.T) {
	e := newEnv(t)
	listed := e.skill(e.opencode(), "foo")
	mine := e.skill(filepath.Join(e.home, "mine"), "foo")
	got, warns, err := e.resolve([]string{"all", mine})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Dir != mine {
		t.Errorf("got %+v, want %s", got, mine)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], listed) {
		t.Errorf("warnings = %q", warns)
	}
}

func TestTwoDifferentSkillsUnderOneNameError(t *testing.T) {
	e := newEnv(t)
	a := e.skill(filepath.Join(e.home, "a"), "foo")
	b := e.skill(filepath.Join(e.home, "b"), "foo")
	if _, _, err := e.resolve([]string{a, b}); err == nil || !strings.Contains(err.Error(), a) || !strings.Contains(err.Error(), b) {
		t.Errorf("err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	e := newEnv(t)
	e.skill(e.opencode(), "good")
	noMD := filepath.Join(e.opencode(), "no-md")
	if err := os.MkdirAll(noMD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.home, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	badName := filepath.Join(e.home, "x", "Bad_Name")
	e.skill(filepath.Dir(badName), "Bad_Name")

	for _, tc := range []struct {
		name   string
		values []string
		dirs   []string
		want   []string
	}{
		{"unknown name names the sources", []string{"nope"}, nil, []string{`"nope" not found`, e.opencode()}},
		{"missing -skills-dir", nil, []string{filepath.Join(e.home, "absent")}, []string{"-skills-dir", "no such directory"}},
		{"-skills-dir that is a file", nil, []string{filepath.Join(e.home, "file")}, []string{"not a directory"}},
		{"missing SKILL.md by name", []string{"no-md"}, nil, []string{"SKILL.md"}},
		{"missing SKILL.md by path", []string{noMD}, nil, []string{"SKILL.md"}},
		{"missing path", []string{filepath.Join(e.home, "absent", "x")}, nil, []string{"no such directory"}},
		{"invalid name", []string{"Bad_Name"}, nil, []string{"not a valid skill name"}},
		{"invalid folder name by path", []string{badName}, nil, []string{"not a valid skill name"}},
		{"~user", []string{"~root/x"}, nil, []string{"~user"}},
		{"empty", []string{""}, nil, []string{"empty"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := e.resolve(tc.values, tc.dirs...)
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q is missing %q", err, w)
				}
			}
		})
	}

	empty := newEnv(t)
	if _, _, err := empty.resolve([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "no skills folder") {
		t.Errorf("no sources: err = %v", err)
	}
}

func TestSkillMDLinkingOutsideTheSkillIsAnError(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.opencode(), "foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(e.home, "SKILL.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.resolve([]string{"foo"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("err = %v", err)
	}
}

func TestAllSkipsInvalidEntriesWithAWarning(t *testing.T) {
	e := newEnv(t)
	e.skill(e.opencode(), "good")
	if err := os.MkdirAll(filepath.Join(e.opencode(), "no-md"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.skill(e.opencode(), "Bad_Name")
	if err := os.Symlink(filepath.Join(e.home, "gone"), filepath.Join(e.opencode(), "dangling")); err != nil {
		t.Fatal(err)
	}
	// Not skill candidates at all: no warning.
	if err := os.WriteFile(filepath.Join(e.opencode(), "README.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.opencode(), ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, warns, err := e.resolve([]string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "good" {
		t.Errorf("got %v", names(got))
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"no-md", "Bad_Name", "dangling"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning for %s:\n%s", want, joined)
		}
	}
	if len(warns) != 3 {
		t.Errorf("want exactly 3 warnings, got %q", warns)
	}
}

func TestProjectCollisionWarns(t *testing.T) {
	for _, d := range projectDirs {
		t.Run(d, func(t *testing.T) {
			e := newEnv(t)
			e.skill(e.opencode(), "foo")
			e.skill(filepath.Join(e.cwd, d), "foo")
			got, warns, err := e.resolve([]string{"foo"})
			if err != nil || len(got) != 1 {
				t.Fatalf("got %v, %v", got, err)
			}
			if len(warns) != 1 || !strings.Contains(warns[0], filepath.Join(d, "foo")) {
				t.Errorf("warnings = %q", warns)
			}
		})
	}
}

func TestSkillInsideTheProjectWarnsItIsWritable(t *testing.T) {
	e := newEnv(t)
	inProject := e.skill(filepath.Join(e.cwd, "team-skills"), "foo")
	outside := e.skill(e.opencode(), "bar")

	// By path, alongside one outside the project.
	got, warns, err := e.resolve([]string{inProject, "bar"})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], `"foo"`) || !strings.Contains(warns[0], "writable") {
		t.Errorf("want one writable warning for foo only, got %q", warns)
	}
	// By name, through a -skills-dir inside the project.
	_, warns, err = e.resolve([]string{"foo"}, filepath.Join(e.cwd, "team-skills"))
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "writable") {
		t.Errorf("-skills-dir inside the project: %v, %q", err, warns)
	}

	// A skill outside the project, or in a sibling whose name merely starts
	// with the project's, doesn't warn.
	sibling := e.skill(e.cwd+"-other", "baz")
	_, warns, err = e.resolve([]string{outside, sibling})
	if err != nil || len(warns) != 0 {
		t.Errorf("outside the project: %v, %q", err, warns)
	}
}

func TestAllWithNothingFoundWarns(t *testing.T) {
	e := newEnv(t)
	for name, dirs := range map[string][]string{
		"no folders":   nil,
		"empty folder": {t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			got, warns, err := e.resolve([]string{"all"}, dirs...)
			if err != nil || len(got) != 0 {
				t.Fatalf("got %v, %v", got, err)
			}
			if len(warns) != 1 || !strings.Contains(warns[0], "nothing is attached") {
				t.Errorf("warnings = %q", warns)
			}
		})
	}
	// Something found: no such warning.
	e.skill(e.opencode(), "foo")
	if _, warns, _ := e.resolve([]string{"all"}); len(warns) != 0 {
		t.Errorf("warnings = %q", warns)
	}
}

func TestManySkillsWarn(t *testing.T) {
	e := newEnv(t)
	for i := 0; i <= ManyMounts; i++ {
		e.skill(e.opencode(), fmt.Sprintf("s%d", i))
	}
	got, warns, err := e.resolve([]string{"all"})
	if err != nil || len(got) != ManyMounts+1 {
		t.Fatalf("got %d skills, %v", len(got), err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "mounts") {
		t.Errorf("warnings = %q", warns)
	}
}

func TestValidNameAndIsPath(t *testing.T) {
	for n, want := range map[string]bool{
		"foo": true, "foo-bar": true, "a1-2b": true, strings.Repeat("a", 64): true,
		"": false, "Foo": false, "foo_bar": false, "-foo": false, "foo-": false, "foo--bar": false, strings.Repeat("a", 65): false,
	} {
		if ValidName(n) != want {
			t.Errorf("ValidName(%q) = %v", n, !want)
		}
	}
	for v, want := range map[string]bool{"foo": false, "all": false, "./foo": true, "a/b": true, "~": true, "~/x": true, ".": true} {
		if IsPath(v) != want {
			t.Errorf("IsPath(%q) = %v", v, !want)
		}
	}
}

func TestSummary(t *testing.T) {
	got := Summary([]Skill{{Name: "a", Source: "/s1"}, {Name: "b", Source: "/s2"}})
	if got != "a (/s1), b (/s2)" {
		t.Errorf("Summary = %q", got)
	}
}
