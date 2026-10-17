// Package skills resolves the host skills a user asks to attach to a sandbox
// session (rnes -skill / -skills-dir). A skill is a directory holding a
// SKILL.md, named after its folder.
//
// Resolution only reads the host filesystem; it never involves docker, so a
// bad request fails before any container starts.
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// All is the -skill value that attaches every skill found.
const All = "all"

// ManyMounts is the number of attached skills above which a warning is
// printed: each one is a separate mount on the container.
const ManyMounts = 50

// maxNameLen is opencode's limit on a skill name.
const maxNameLen = 64

// namePattern is opencode's rule for a skill name: lowercase alphanumerics
// separated by single hyphens.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// projectDirs are where opencode looks for a project's own skills, relative to
// the working directory. .opencode/skill is the legacy singular form, which
// opencode v2 still reads.
var projectDirs = []string{".opencode/skills", ".opencode/skill", ".claude/skills", ".agents/skills"}

// Skill is one skill to attach.
type Skill struct {
	// Name is the skill's folder name, which is also where it is mounted.
	Name string
	// Path is where it was found: the source folder joined with Name, or the
	// path given to -skill.
	Path string
	// Dir is Path with symlinks resolved: what gets mounted.
	Dir string
	// Source is the folder it was found in (for a path, its parent).
	Source string
}

// Request is what the user asked for.
type Request struct {
	// Values are the -skill values, in order: names, paths or All.
	Values []string
	// Dirs are the -skills-dir folders, in order.
	Dirs []string
	// Home is the host user's home directory, for ~ and the default sources.
	Home string
	// Cwd is the working directory: relative paths are resolved against it,
	// and it is checked for project skills of the same name.
	Cwd string
}

// DefaultSources are the host folders searched after any -skills-dir, in
// order.
func DefaultSources(home string) []string {
	return []string{
		filepath.Join(home, ".config", "opencode", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
}

// ValidName reports whether name is a valid skill name.
func ValidName(name string) bool {
	return len(name) <= maxNameLen && namePattern.MatchString(name)
}

// IsPath reports whether a -skill value names a directory rather than a skill
// to look up.
func IsPath(v string) bool {
	return strings.ContainsRune(v, '/') || strings.ContainsRune(v, filepath.Separator) ||
		strings.HasPrefix(v, "~") || v == "." || v == ".."
}

// Resolve turns req into the skills to attach, sorted by name, plus warnings
// to show the user (shadowed skills, skipped entries, project collisions, a
// large number of mounts). With no Values nothing is attached and only the
// -skills-dir folders are checked. Any error means the session must not start.
func Resolve(req Request) ([]Skill, []string, error) {
	sources, err := sources(req)
	if err != nil {
		return nil, nil, err
	}
	if len(req.Values) == 0 {
		return nil, nil, nil
	}

	r := resolver{sources: sources, chosen: map[string]Skill{}}
	all := false
	for _, v := range req.Values {
		switch {
		case v == "":
			return nil, nil, fmt.Errorf("-skill: empty value")
		case v == All:
			all = true
		case IsPath(v):
			s, err := r.byPath(v, req)
			if err != nil {
				return nil, nil, err
			}
			if err := r.choose(s); err != nil {
				return nil, nil, err
			}
		default:
			s, err := r.byName(v)
			if err != nil {
				return nil, nil, err
			}
			if err := r.choose(s); err != nil {
				return nil, nil, err
			}
		}
	}
	if all {
		r.all()
		if len(r.chosen) == 0 {
			r.warnings = append(r.warnings, fmt.Sprintf("-skill %s: no skills found in any folder searched, so nothing is attached", All))
		}
	}

	out := make([]Skill, 0, len(r.chosen))
	for _, s := range r.chosen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	warnings := r.warnings
	for _, s := range out {
		for _, d := range projectDirs {
			p := filepath.Join(req.Cwd, d, s.Name)
			if _, err := os.Lstat(p); err == nil {
				warnings = append(warnings, fmt.Sprintf("skill %q is attached from %s, but this project also has %s; which one opencode uses is up to opencode", s.Name, s.Path, filepath.Join(d, s.Name)))
			}
		}
	}
	// The working directory is mounted read-write in the sandbox, so a skill
	// inside it can be changed by the agent through that mount, whatever the
	// skill's own mount says. Dir has symlinks resolved, so compare against
	// the resolved working directory.
	cwd := req.Cwd
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	for _, s := range out {
		if within(cwd, s.Dir) {
			warnings = append(warnings, fmt.Sprintf("skill %q (%s) is inside the project, which is writable in the sandbox: the agent can modify it despite its read-only mount", s.Name, s.Dir))
		}
	}
	if len(out) > ManyMounts {
		warnings = append(warnings, fmt.Sprintf("%d skills attached: the container gets one read-only mount per skill, which is a lot of mounts", len(out)))
	}
	return out, warnings, nil
}

// within reports whether path is dir itself or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Summary is the startup line's text: each attached skill and the folder it
// came from.
func Summary(skills []Skill) string {
	parts := make([]string, len(skills))
	for i, s := range skills {
		parts[i] = fmt.Sprintf("%s (%s)", s.Name, s.Source)
	}
	return strings.Join(parts, ", ")
}

// sources lists the folders name lookup and All search, in order: every
// -skills-dir (which must exist), then the default folders that exist. A
// folder reached twice (the same path, or through a symlink) is kept once,
// at its first position.
func sources(req Request) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(dir string) {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			real = dir
		}
		if !seen[real] {
			seen[real] = true
			out = append(out, dir)
		}
	}
	for _, d := range req.Dirs {
		p, err := expand(d, req)
		if err != nil {
			return nil, fmt.Errorf("-skills-dir %s: %w", d, err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("-skills-dir %s: no such directory", d)
			}
			return nil, fmt.Errorf("-skills-dir %s: %w", d, err)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("-skills-dir %s: not a directory", d)
		}
		add(p)
	}
	// The defaults are only read when a skill is actually requested.
	if len(req.Values) == 0 {
		return out, nil
	}
	for _, d := range DefaultSources(req.Home) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			add(d)
		}
	}
	return out, nil
}

// expand resolves a user-given path: ~ and ~/ are the home directory, and a
// relative path is relative to the working directory.
func expand(p string, req Request) (string, error) {
	switch {
	case p == "~":
		p = req.Home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(req.Home, p[2:])
	case strings.HasPrefix(p, "~"):
		return "", fmt.Errorf("only ~ and ~/ are expanded, not ~user")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(req.Cwd, p)
	}
	return filepath.Clean(p), nil
}

type resolver struct {
	sources  []string
	chosen   map[string]Skill
	warnings []string
}

// choose records an explicitly requested skill. The same skill asked for
// twice is merged; two different directories under one name can't both be
// mounted there.
func (r *resolver) choose(s Skill) error {
	if prev, ok := r.chosen[s.Name]; ok {
		if prev.Dir == s.Dir {
			return nil
		}
		return fmt.Errorf("two different skills named %q requested: %s and %s", s.Name, prev.Path, s.Path)
	}
	r.chosen[s.Name] = s
	return nil
}

// byPath resolves a -skill path: exactly that directory, no lookup.
func (r *resolver) byPath(v string, req Request) (Skill, error) {
	p, err := expand(v, req)
	if err != nil {
		return Skill{}, fmt.Errorf("-skill %s: %w", v, err)
	}
	name := filepath.Base(p)
	dir, err := check(p, name)
	if err != nil {
		return Skill{}, fmt.Errorf("-skill %s: %w", v, err)
	}
	return Skill{Name: name, Path: p, Dir: dir, Source: filepath.Dir(p)}, nil
}

// byName looks a skill up in every source. It must be found in exactly one
// (the same directory reached through a symlink counts once); in more than
// one it is ambiguous and the user has to pick with a path.
func (r *resolver) byName(name string) (Skill, error) {
	if !ValidName(name) {
		return Skill{}, fmt.Errorf("-skill %s: %s", name, invalidNameReason)
	}
	var found []Skill
	var invalid error
	for _, src := range r.sources {
		p := filepath.Join(src, name)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		dir, err := check(p, name)
		if err != nil {
			if invalid == nil {
				invalid = fmt.Errorf("-skill %s: %s: %w", name, p, err)
			}
			continue
		}
		dup := false
		for _, f := range found {
			dup = dup || f.Dir == dir
		}
		if !dup {
			found = append(found, Skill{Name: name, Path: p, Dir: dir, Source: src})
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		var b strings.Builder
		fmt.Fprintf(&b, "skill %q is in more than one place; pick one with -skill <path>:", name)
		for _, f := range found {
			fmt.Fprintf(&b, "\n  %s", f.Path)
		}
		return Skill{}, errors.New(b.String())
	case invalid != nil:
		return Skill{}, invalid
	case len(r.sources) == 0:
		return Skill{}, fmt.Errorf("skill %q not found: no skills folder exists (searched for %s)", name, strings.Join(DefaultSources("~"), ", "))
	default:
		return Skill{}, fmt.Errorf("skill %q not found (searched %s)", name, strings.Join(r.sources, ", "))
	}
}

// all adds every skill in the sources, the first source winning a name, on
// top of the explicitly requested ones (which win over any source). It warns
// once per shadowed skill and skips invalid entries with a warning; it never
// fails on a duplicate.
func (r *resolver) all() {
	for _, src := range r.sources {
		entries, err := os.ReadDir(src)
		if err != nil {
			r.warnings = append(r.warnings, fmt.Sprintf("skipping skills folder %s: %v", src, err))
			continue
		}
		for _, e := range entries {
			name := e.Name()
			// Hidden entries (.git, .DS_Store) and plain files aren't skills.
			if strings.HasPrefix(name, ".") {
				continue
			}
			p := filepath.Join(src, name)
			if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
				if err != nil && e.Type()&fs.ModeSymlink != 0 {
					r.warnings = append(r.warnings, fmt.Sprintf("skipping %s: %v", p, err))
				}
				continue
			}
			dir, err := check(p, name)
			if err != nil {
				r.warnings = append(r.warnings, fmt.Sprintf("skipping %s: %v", p, err))
				continue
			}
			if prev, ok := r.chosen[name]; ok {
				if prev.Dir != dir {
					r.warnings = append(r.warnings, fmt.Sprintf("skill %q: using %s, shadowing %s", name, prev.Path, p))
				}
				continue
			}
			r.chosen[name] = Skill{Name: name, Path: p, Dir: dir, Source: src}
		}
	}
}

// DiscoverProject lists the skills the project at cwd defines, in its
// .opencode/skills (and legacy .opencode/skill), .claude/skills and
// .agents/skills folders. It is for a session that only sees cwd (the
// sandbox mounts just the working directory). Like DiscoverAll, it skips
// entries that aren't valid skills without a word.
func DiscoverProject(cwd string) []Skill {
	return discover(projectSources(cwd, false))
}

// DiscoverAll lists the skills a host session in cwd has available: the
// project's own (the working directory, and each parent up to the
// repository root, nearer ones winning a name), then the global folders. The
// first source wins a name.
//
// Nothing was asked for here, unlike -skill all, so an entry that isn't a
// valid skill is skipped silently rather than warned about on every launch.
// The scope is also narrower than opencode's: a skill must be a folder with
// a SKILL.md directly inside a skills folder, with a name that is a valid
// skill name, so flat skills/<name>.md files, deeper SKILL.md files and
// unconventional names get no command.
func DiscoverAll(home, cwd string) []Skill {
	srcs := projectSources(cwd, true)
	for _, d := range globalSources(home) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			srcs = append(srcs, d)
		}
	}
	return discover(srcs)
}

// projectSources are the skill folders that exist in the project, nearest
// first and in opencode's precedence order within a directory. With walk,
// that is cwd and every parent up to the repository root (the nearest
// directory holding a .git); without a repository above cwd, or without
// walk, cwd alone.
func projectSources(cwd string, walk bool) []string {
	var out []string
	for _, dir := range projectLevels(cwd, walk) {
		for _, d := range projectDirs {
			p := filepath.Join(dir, d)
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				out = append(out, p)
			}
		}
	}
	return out
}

// projectLevels are the directories projectSources looks in, nearest first.
func projectLevels(cwd string, walk bool) []string {
	if !walk {
		return []string{cwd}
	}
	var levels []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		levels = append(levels, dir)
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return levels
		}
		if filepath.Dir(dir) == dir {
			return []string{cwd}
		}
	}
}

// globalSources are the host folders opencode reads global skills from, in
// precedence order. OPENCODE_CONFIG_DIR replaces opencode's global config
// directory, and so its skills folders, but not the .agents and .claude
// compatibility folders. Unlike DefaultSources it includes the legacy
// singular skill folder, which opencode v2 still reads but -skill doesn't
// search.
func globalSources(home string) []string {
	cfg := filepath.Join(home, ".config", "opencode")
	if dir := os.Getenv("OPENCODE_CONFIG_DIR"); dir != "" {
		cfg = dir
	}
	return []string{
		filepath.Join(cfg, "skills"),
		filepath.Join(cfg, "skill"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
}

// discover lists the skills in srcs, the first source winning a name.
func discover(srcs []string) []Skill {
	r := resolver{sources: srcs, chosen: map[string]Skill{}}
	r.all()
	out := make([]Skill, 0, len(r.chosen))
	for _, s := range r.chosen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

const invalidNameReason = "not a valid skill name (lowercase letters and digits separated by single hyphens, at most 64 characters)"

// check validates the skill directory p, to be attached as name, and returns
// it with symlinks resolved. Its SKILL.md must be a file inside it: one that
// links outside would be dangling in the sandbox, which only gets the
// directory.
func check(p, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("folder name %q is %s", name, invalidNameReason)
	}
	fi, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("no such directory")
		}
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	dir, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	md := filepath.Join(dir, "SKILL.md")
	fi, err = os.Stat(md)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("no SKILL.md in it")
		}
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("SKILL.md is not a regular file")
	}
	real, err := filepath.EvalSymlinks(md)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(dir, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("SKILL.md links outside the skill's directory, so it wouldn't be readable in the sandbox")
	}
	return dir, nil
}
