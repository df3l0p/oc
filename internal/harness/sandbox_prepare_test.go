package harness

import (
	"strings"
	"testing"
)

func TestSandboxPrepare(t *testing.T) {
	tests := []struct {
		name      string
		opts      Options
		exit      map[string]int
		wantCalls []string // argv prefixes, in order
		wantImage string   // prefix of the image sessions will run
		wantErr   string
	}{
		{
			name: "base and image present locally: nothing to do",
			opts: Options{Sandbox: true},
			wantCalls: []string{
				"image inspect oc-sandbox-base:",
				"image inspect oc-sandbox-default:",
			},
			wantImage: "oc-sandbox-default:",
		},
		{
			name: "image missing: built on the base with OC_BASE, never pulled",
			opts: Options{Sandbox: true},
			exit: map[string]int{"image inspect oc-sandbox-default:": 1},
			wantCalls: []string{
				"image inspect oc-sandbox-base:",
				"image inspect oc-sandbox-default:",
				"build --build-arg OC_BASE=oc-sandbox-base:",
			},
			wantImage: "oc-sandbox-default:",
		},
		{
			name: "both missing: base built first, then the image",
			opts: Options{Sandbox: true},
			exit: map[string]int{"image inspect": 1},
			wantCalls: []string{
				"image inspect oc-sandbox-base:",
				"build -t oc-sandbox-base:",
				"image inspect oc-sandbox-default:",
				"build --build-arg OC_BASE=oc-sandbox-base:",
			},
			wantImage: "oc-sandbox-default:",
		},
		{
			name: "-build rebuilds base and image without inspecting or using the layer cache",
			opts: Options{Sandbox: true, Build: true},
			wantCalls: []string{
				"build --no-cache -t oc-sandbox-base:",
				"build --no-cache --build-arg OC_BASE=oc-sandbox-base:",
			},
			wantImage: "oc-sandbox-default:",
		},
		{
			name:      "base build failure is an error and stops there",
			opts:      Options{Sandbox: true},
			exit:      map[string]int{"image inspect": 1, "build": 1},
			wantCalls: []string{"image inspect", "build"},
			wantErr:   "building oc-sandbox-base:",
		},
		{
			name: "image build failure is an error",
			opts: Options{Sandbox: true},
			exit: map[string]int{"image inspect oc-sandbox-default:": 1, "build": 1},
			wantCalls: []string{
				"image inspect oc-sandbox-base:",
				"image inspect oc-sandbox-default:",
				"build",
			},
			wantErr: "building oc-sandbox-default:",
		},
		{
			name:    "unknown image fails before docker is touched",
			opts:    Options{Sandbox: true, Image: "nope"},
			wantErr: "available: default",
		},
		{
			name:    "the internal base is not selectable",
			opts:    Options{Sandbox: true, Image: "base"},
			wantErr: "unknown sandbox image",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := fakeDocker(t, tt.exit)
			s := newSandbox(tt.opts, "")
			err := s.Prepare()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Prepare error = %v, want it to contain %q", err, tt.wantErr)
			}
			got := calls(t, logPath)
			if len(got) != len(tt.wantCalls) {
				t.Fatalf("docker calls = %q, want prefixes %q", got, tt.wantCalls)
			}
			for i, c := range got {
				if !strings.HasPrefix(c, tt.wantCalls[i]) {
					t.Errorf("call %d = %q, want prefix %q", i, c, tt.wantCalls[i])
				}
			}
			if tt.wantImage != "" && !strings.HasPrefix(s.image, tt.wantImage) {
				t.Errorf("run image = %q, want prefix %q", s.image, tt.wantImage)
			}
		})
	}
}

func TestImageTag(t *testing.T) {
	a := imageTag("default", []byte("FROM x"), "oc-sandbox-base:1")
	if a != imageTag("default", []byte("FROM x"), "oc-sandbox-base:1") {
		t.Error("tag must be stable for identical inputs")
	}
	if a == imageTag("default", []byte("FROM y"), "oc-sandbox-base:1") {
		t.Error("tag must change with the Dockerfile content")
	}
	if a == imageTag("default", []byte("FROM x"), "oc-sandbox-base:2") {
		t.Error("a layer's tag must change when its base's tag does")
	}
	if !strings.HasPrefix(a, "oc-sandbox-default:") {
		t.Errorf("tag %q must be oc-sandbox-<name>:<hash>", a)
	}
}

func TestLayerBuildArgs(t *testing.T) {
	got := strings.Join(layerBuildArgs("oc-sandbox-default:h", "oc-sandbox-base:b"), " ")
	want := "build --build-arg OC_BASE=oc-sandbox-base:b -t oc-sandbox-default:h -"
	if got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}
