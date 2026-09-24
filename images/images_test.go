package images

import (
	"reflect"
	"strings"
	"testing"
)

type instruction struct {
	name string // upper-cased
	args []string
}

// parseInstructions returns a Dockerfile's instructions with comments dropped
// and backslash continuations joined. It only needs to find FROM, ARG and USER.
func parseInstructions(src string) []instruction {
	var out []instruction
	var cur strings.Builder
	flush := func() {
		if f := strings.Fields(cur.String()); len(f) > 0 {
			out = append(out, instruction{name: strings.ToUpper(f[0]), args: f[1:]})
		}
		cur.Reset()
	}
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || (t == "" && cur.Len() == 0) {
			continue
		}
		if strings.HasSuffix(t, `\`) {
			cur.WriteString(strings.TrimSuffix(t, `\`) + " ")
			continue
		}
		cur.WriteString(t)
		flush()
	}
	flush()
	return out
}

func TestNamesListsOnlySelectableImages(t *testing.T) {
	got := Names()
	if !reflect.DeepEqual(got, []string{"default"}) {
		t.Errorf("Names() = %v, want [default]: the base image is internal", got)
	}
}

func TestReadRejectsUnknownNamesPathsAndTheBase(t *testing.T) {
	for _, name := range []string{"nope", "", Base, "../images/default", "default.Dockerfile", "sub/default"} {
		if _, err := Read(name); err == nil {
			t.Errorf("Read(%q) = nil error, want one", name)
		}
	}
	if _, err := Read(Default); err != nil {
		t.Errorf("Read(%q): %v", Default, err)
	}
}

func TestReadBase(t *testing.T) {
	raw, err := ReadBase()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "FROM node:") {
		t.Error("base must be a standalone image")
	}
}

// TestBundledImagesFollowTheContract checks every shipped Dockerfile, since
// oc builds them as-is with no runtime rewriting: the base is standalone; every
// selectable image is a layer that declares ARG OC_BASE, starts its final stage
// FROM ${OC_BASE} (which oc passes as a build arg) and hands back to user oc.
func TestBundledImagesFollowTheContract(t *testing.T) {
	all := append([]string{Base}, Names()...)
	for _, name := range all {
		t.Run(name, func(t *testing.T) {
			var raw []byte
			var err error
			if name == Base {
				raw, err = ReadBase()
			} else {
				raw, err = Read(name)
			}
			if err != nil {
				t.Fatal(err)
			}
			src := string(raw)
			if name == Base {
				if strings.Contains(src, "OC_BASE") {
					t.Error("the base must not depend on OC_BASE")
				}
				if !strings.Contains(src, "npm install -g opencode-ai") {
					t.Error("the base must install opencode")
				}
				if !strings.Contains(src, `ENTRYPOINT ["opencode"]`) {
					t.Error("the base must run opencode")
				}
			}

			var (
				sawFrom, argBeforeFrom bool
				finalFrom, lastUser    string
			)
			for _, in := range parseInstructions(src) {
				switch in.name {
				case "FROM":
					sawFrom = true
					finalFrom = ""
					for _, a := range in.args {
						if !strings.HasPrefix(a, "--") {
							finalFrom = a
							break
						}
					}
					lastUser = "" // each stage starts from its base's user
				case "ARG":
					for _, a := range in.args {
						if n, _, _ := strings.Cut(a, "="); n == "OC_BASE" && !sawFrom {
							argBeforeFrom = true
						}
					}
				case "USER":
					if len(in.args) > 0 {
						lastUser = in.args[0]
					}
				}
			}
			if !sawFrom {
				t.Fatal("no FROM instruction")
			}
			if name == Base {
				if lastUser != "oc" {
					t.Errorf("the base must end as USER oc, got %q", lastUser)
				}
				return
			}
			if !argBeforeFrom {
				t.Error("a layer must declare ARG OC_BASE before its first FROM")
			}
			if finalFrom != "${OC_BASE}" && finalFrom != "$OC_BASE" {
				t.Errorf("a layer's final stage must be FROM ${OC_BASE}, got FROM %s", finalFrom)
			}
			if lastUser != "oc" {
				t.Errorf("a layer must end as USER oc (after any USER root), got %q", lastUser)
			}
		})
	}
}
