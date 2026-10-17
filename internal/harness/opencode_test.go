package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeConfigureWritesAllDiscoveredModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	o := Opencode{configPath: path}

	if err := o.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"model-a", "model-b"}); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	providers := got["providers"].(map[string]interface{})
	block := providers["llama-cpp"].(map[string]interface{})
	models := block["models"].(map[string]interface{})

	for _, id := range []string{"model-a", "model-b"} {
		if _, ok := models[id]; !ok {
			t.Errorf("expected model %q to be configured, got %v", id, models)
		}
	}
	if block["package"] != "aisdk:@ai-sdk/openai-compatible" {
		t.Errorf("package = %v, want aisdk:@ai-sdk/openai-compatible", block["package"])
	}
	if got := block["settings"].(map[string]interface{})["baseURL"]; got != "http://127.0.0.1:8080/v1" {
		t.Errorf("baseURL = %v, want http://127.0.0.1:8080/v1", got)
	}
}

func TestNewOpencodeUsesHomeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	h, err := newOpencode(Options{})
	if err != nil {
		t.Fatalf("newOpencode: %v", err)
	}
	o := h.(Opencode)
	want := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	if o.configPath != want {
		t.Errorf("configPath = %q, want %q", o.configPath, want)
	}
}

// hostSession is a fake host for launch: HOME, an opencode that has the
// given global config path, and none of the variables launch reads, so a
// developer's own environment can't change what the tests see.
func hostSession(t *testing.T) (home string, o Opencode) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	unsetenv(t, "OPENCODE_CONFIG_CONTENT")
	unsetenv(t, "OPENCODE_CONFIG_DIR")
	return home, Opencode{configPath: filepath.Join(home, ".config", "opencode", "opencode.jsonc")}
}

// unsetenv removes key for the test and restores it afterwards. t.Setenv can
// only set: an empty value would still be an entry in os.Environ.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

// mkSkill creates dir/name/SKILL.md.
func mkSkill(t *testing.T, dir, name string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name, "SKILL.md"), "x")
}

// envValue is the value of key in env, and how many entries name it.
func envValue(env []string, key string) (string, int) {
	var val string
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			val, n = strings.TrimPrefix(e, key+"="), n+1
		}
	}
	return val, n
}

func TestOpencodeLaunchWithSkillCommands(t *testing.T) {
	home, o := hostSession(t)

	// No skills in the default folders: the plain launch, no environment.
	args, env := o.launch(t.TempDir())
	if len(args) != 1 || args[0] != "." {
		t.Errorf("args = %q, want [.] (the launch must be unchanged)", args)
	}
	if !hasEnv(env, "OPENCODE_CONFIG="+o.configPath) {
		t.Errorf("env = %q, want OPENCODE_CONFIG=%s", env, o.configPath)
	}
	if hasEnvPrefix(env, "OPENCODE_CONFIG_CONTENT=") {
		t.Errorf("env = %q, want no OPENCODE_CONFIG_CONTENT", env)
	}

	// Two skills, one shadowed by a project command: the session runs on its
	// own server (--standalone), with the commands in OPENCODE_CONFIG_CONTENT.
	skillsDir := filepath.Join(home, ".config", "opencode", "skills")
	mkSkill(t, skillsDir, "alpha")
	mkSkill(t, skillsDir, "beta")
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".opencode", "commands", "beta.md"), "project")
	args, env = o.launch(cwd)
	if len(args) != 2 || args[0] != "--standalone" || args[1] != "." {
		t.Errorf("args = %q, want [--standalone .]", args)
	}
	want := "OPENCODE_CONFIG_CONTENT=" + configContent([]string{"alpha"})
	if !hasEnv(env, want) {
		t.Errorf("env = %q, want %q (beta is the project's own command)", env, want)
	}
}

func TestOpencodeLaunchGeneratesCommandsForProjectSkills(t *testing.T) {
	_, o := hostSession(t)
	// The project's skills come from the repository root down to the working
	// directory, as opencode loads them.
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main")
	mkSkill(t, filepath.Join(repo, ".opencode", "skills"), "from-root")
	cwd := filepath.Join(repo, "packages", "web")
	mkSkill(t, filepath.Join(cwd, ".claude", "skills"), "from-here")

	_, env := o.launch(cwd)
	want := "OPENCODE_CONFIG_CONTENT=" + configContent([]string{"from-here", "from-root"})
	if !hasEnv(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
}

func TestOpencodeLaunchChecksGlobalCommandsInOpencodeConfigDir(t *testing.T) {
	home, o := hostSession(t)
	mkSkill(t, filepath.Join(home, ".agents", "skills"), "alpha")
	mkSkill(t, filepath.Join(home, ".agents", "skills"), "beta")

	// The user's global commands live in the default directory...
	writeFile(t, filepath.Join(home, ".config", "opencode", "commands", "alpha.md"), "mine")
	// ...unless OPENCODE_CONFIG_DIR replaces it.
	custom := t.TempDir()
	writeFile(t, filepath.Join(custom, "commands", "beta.md"), "mine")
	t.Setenv("OPENCODE_CONFIG_DIR", custom)

	_, env := o.launch(t.TempDir())
	want := "OPENCODE_CONFIG_CONTENT=" + configContent([]string{"alpha"})
	if !hasEnv(env, want) {
		t.Errorf("env = %q, want %q (beta is a global command in OPENCODE_CONFIG_DIR)", env, want)
	}
}

func TestOpencodeLaunchMergesIntoTheUsersInlineConfig(t *testing.T) {
	home, o := hostSession(t)
	skillsDir := filepath.Join(home, ".config", "opencode", "skills")
	mkSkill(t, skillsDir, "alpha")
	mkSkill(t, skillsDir, "beta")
	mine := `{"model":"a/b","permissions":[{"action":"shell","resource":"rm *","effect":"deny"}],"commands":{"beta":{"template":"mine"}}}`
	t.Setenv("OPENCODE_CONFIG_CONTENT", mine)

	args, env := o.launch(t.TempDir())
	if len(args) != 2 || args[0] != "--standalone" {
		t.Errorf("args = %q, want [--standalone .]", args)
	}
	got, n := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if n != 1 {
		t.Fatalf("env has %d OPENCODE_CONFIG_CONTENT entries, want exactly one", n)
	}
	var cfg struct {
		Model       string
		Permissions []map[string]string
		Commands    map[string]commandDef
	}
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatalf("merged content isn't JSON: %v\n%s", err, got)
	}
	if cfg.Model != "a/b" || len(cfg.Permissions) != 1 || cfg.Permissions[0]["effect"] != "deny" {
		t.Errorf("the user's own settings were lost: %s", got)
	}
	if cfg.Commands["beta"].Template != "mine" {
		t.Errorf("the user's own /beta was replaced: %s", got)
	}
	if cfg.Commands["alpha"] != newCommandDef("alpha") {
		t.Errorf("/alpha wasn't added: %s", got)
	}
}

func TestOpencodeLaunchLeavesAnUnreadableInlineConfigAlone(t *testing.T) {
	home, o := hostSession(t)
	mkSkill(t, filepath.Join(home, ".config", "opencode", "skills"), "alpha")
	for _, bad := range []string{"// comment\n{}", `["not","an","object"]`, `{"commands":"nope"}`} {
		t.Setenv("OPENCODE_CONFIG_CONTENT", bad)
		args, env := o.launch(t.TempDir())
		if len(args) != 1 || args[0] != "." {
			t.Errorf("%q: args = %q, want the plain launch", bad, args)
		}
		if got, n := envValue(env, "OPENCODE_CONFIG_CONTENT"); got != bad || n != 1 {
			t.Errorf("%q: env value = %q (%d entries), want the user's value untouched", bad, got, n)
		}
	}
}

// hasEnv reports whether env contains an entry equal to want.
func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// hasEnvPrefix reports whether env contains an entry with the prefix.
func hasEnvPrefix(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func TestOpencodeIsV2(t *testing.T) {
	for in, want := range map[string]bool{
		"opencode v2.0.32\n":            true,
		"2.0.32":                        true,
		"opencode 2.0.0":                true,
		"opencode v2.0.32 2026-10-17\n": true,
		"v2.0.32 (build 2026)":          true,
		"opencode version 2.0.32":       true,
		"opencode v2.0.32-beta.1":       true,
		"opencode v1.19.3":              false,
		"1.18.31":                       false,
		"opencode v12.0.0":              false,
		"opencode v1.19.3 2026-10-17":   false,
		"":                              false,
		"not a version":                 false,
	} {
		if err := opencodeIsV2(in); (err == nil) != want {
			t.Errorf("opencodeIsV2(%q) error = %v, want v2 accepted = %v", in, err, want)
		}
	}
}
