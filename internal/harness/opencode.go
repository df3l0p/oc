package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/df3l0p/rnes/internal/opencodeconfig"
	"github.com/df3l0p/rnes/internal/skills"
)

// Opencode drives opencode (github.com/anomalyco/opencode) as a Harness. Its
// config path is fixed to opencode's own default global config location, so
// rnes always merges into and points opencode at the same file it would use
// on its own.
type Opencode struct {
	configPath string
}

func newOpencode(opts Options) (Harness, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	configPath := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	if opts.Sandbox {
		return newSandbox(opts, configPath), nil
	}
	return Opencode{configPath: configPath}, nil
}

// opencodeProvider builds the provider block that points opencode at a
// llama-server at baseURL, offering modelIDs. The package is an AI SDK
// package, so it carries opencode v2's "aisdk:" prefix.
func opencodeProvider(baseURL string, modelIDs []string) opencodeconfig.Provider {
	models := make(map[string]interface{}, len(modelIDs))
	for _, id := range modelIDs {
		models[id] = map[string]interface{}{"name": id + " (local)"}
	}
	return opencodeconfig.Provider{
		Package:  "aisdk:@ai-sdk/openai-compatible",
		Name:     "llama-server (local)",
		Settings: map[string]interface{}{"baseURL": baseURL + "/v1"},
		Models:   models,
	}
}

func (o Opencode) Available() error {
	if _, err := exec.LookPath("opencode"); err != nil {
		return fmt.Errorf("opencode not found on PATH: %w", err)
	}
	out, err := exec.Command("opencode", "--version").Output()
	if err != nil {
		return fmt.Errorf("running opencode --version: %w", err)
	}
	return opencodeIsV2(string(out))
}

// versionRE finds the first "major.minor" version in --version output,
// with an optional "v", so "opencode v2.0.32", "2.0.32" and a version
// followed by a build date or hash all parse the same.
var versionRE = regexp.MustCompile(`(?:^|\s)v?(\d+)\.\d+`)

// opencodeIsV2 reports an error unless out (the output of opencode
// --version) says major version 2.
func opencodeIsV2(out string) error {
	if m := versionRE.FindStringSubmatch(out); m != nil && m[1] == "2" {
		return nil
	}
	return fmt.Errorf("rnes requires opencode v2, got %q", strings.TrimSpace(out))
}

func (o Opencode) Configure(providerKey, baseURL string, modelIDs []string) error {
	return opencodeconfig.Merge(o.configPath, providerKey, opencodeProvider(baseURL, modelIDs))
}

func (o Opencode) Run(dir, providerKey, modelID string) error {
	// The full TUI takes no model flag, and rewriting the user's configured
	// default model would outlive the session. So on the host the session
	// starts on whatever model opencode would pick anyway, and the
	// llama-server models are one /models selection away. providerKey and
	// modelID are only used by the sandbox, which writes them into its
	// session-local config.
	args, env := o.launch(dir)
	cmd := exec.Command("opencode", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// launch is the opencode argv and environment for a host session. The
// session-local OPENCODE_CONFIG always: opencode only reads configPath by
// default when it's left at opencode's own default global config path, which
// is exactly what configPath is here, so this keeps working even if a future
// rnes option makes configPath configurable again. Plus the generated skill
// commands: they ride in OPENCODE_CONFIG_CONTENT (see opencode_commands.go),
// which only a server started with it in sight reads, so a session with
// commands runs opencode with --standalone (its own server) instead of the
// shared background one, which would silently ignore the variable. With no
// skills to generate commands for, the launch is unchanged.
//
// A user's own OPENCODE_CONFIG_CONTENT is never replaced: the commands are
// merged into it, and the commands it defines count as collisions. When it
// can't be read as a plain JSON object, rnes leaves it alone and generates
// nothing.
func (o Opencode) launch(dir string) ([]string, []string) {
	env := append(os.Environ(), "OPENCODE_CONFIG="+o.configPath)
	plain := []string{"."}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rnes: warning: no skill commands: resolving home directory: %v\n", err)
		return plain, env
	}
	scope := commandScope{cwd: dir, globalDir: o.globalDir()}
	existing := os.Getenv(inlineConfigEnv)
	if existing != "" {
		_, commands, err := parseInline(existing)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rnes: warning: no skill commands: %s is set but isn't a plain JSON object (%v), and rnes won't replace it\n", inlineConfigEnv, err)
			return plain, env
		}
		scope.extra = make(map[string]string, len(commands))
		for name := range commands {
			scope.extra[name] = inlineConfigEnv
		}
	}
	found := skills.DiscoverAll(home, dir)
	names := make([]string, 0, len(found))
	for _, s := range found {
		names = append(names, s.Name)
	}
	names, warns := commandNames(scope, names)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "rnes: warning: %s\n", w)
	}
	if len(names) == 0 {
		return plain, env
	}
	content := configContent(names)
	if existing != "" {
		if content, err = mergeConfigContent(existing, names); err != nil {
			fmt.Fprintf(os.Stderr, "rnes: warning: no skill commands: merging into %s: %v\n", inlineConfigEnv, err)
			return plain, env
		}
	}
	fmt.Fprintf(os.Stderr, "rnes: skill commands: /%s\n", strings.Join(names, ", /"))
	return []string{"--standalone", "."}, withEnv(env, inlineConfigEnv, content)
}

// globalDir is the opencode config directory holding the user's global
// commands. OPENCODE_CONFIG_DIR replaces opencode's default one (the CLI
// points its global config directory at it), as it does for global skills.
func (o Opencode) globalDir() string {
	if d := os.Getenv("OPENCODE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Dir(o.configPath)
}
