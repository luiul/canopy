package jump

import (
	"testing"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/mycelium"
)

func TestToAlwaysUsesGhostty(t *testing.T) {
	surfaces := []ancestry.Surface{ancestry.Ghostty, ancestry.VSCode, ancestry.Unknown, ancestry.Surface("unexpected")}
	cases := []struct {
		name      string
		cwd       string
		tty       string
		result    mycelium.Result
		want      Result
		wantCalls int
	}{
		{
			name:      "focus",
			cwd:       "/Users/x/dotfiles",
			tty:       "s017",
			result:    mycelium.Result{OK: true, Message: "Focused Ghostty window."},
			want:      Result{OK: true, Message: "Focused Ghostty window."},
			wantCalls: 1,
		},
		{
			name:      "open subdirectory unmodified",
			cwd:       "/x/worktrees/feature/global-ops/pipelines",
			tty:       "s009",
			result:    mycelium.Result{OK: true, Message: "Opened Ghostty window."},
			want:      Result{OK: true, Message: "Opened Ghostty window."},
			wantCalls: 1,
		},
		{
			name:      "no tty",
			cwd:       "/Users/x/dotfiles",
			result:    mycelium.Result{OK: true, Message: "Focused Ghostty by path."},
			want:      Result{OK: true, Message: "Focused Ghostty by path."},
			wantCalls: 1,
		},
		{
			name:      "failure",
			cwd:       "/Users/x/dotfiles",
			tty:       "s017",
			result:    mycelium.Result{OK: false, Message: "couldn't open Ghostty"},
			want:      Result{OK: false, Message: "couldn't open Ghostty"},
			wantCalls: 1,
		},
		{
			name:   "no cwd",
			tty:    "s017",
			result: mycelium.Result{OK: true, Message: "must not be called"},
			want:   Result{OK: false, Message: "no known working directory to open"},
		},
	}

	for _, surface := range surfaces {
		for _, tc := range cases {
			t.Run(string(surface)+"/"+tc.name, func(t *testing.T) {
				original := openGhostty
				t.Cleanup(func() { openGhostty = original })
				calls := 0
				var gotTty, gotPath string
				openGhostty = func(tty, path string) mycelium.Result {
					calls++
					gotTty, gotPath = tty, path
					return tc.result
				}

				got := To(registry.RegistryEntry{Surface: surface, Cwd: tc.cwd, Tty: tc.tty})
				if got != tc.want {
					t.Fatalf("To() = %+v, want %+v", got, tc.want)
				}
				if calls != tc.wantCalls {
					t.Fatalf("OpenGhostty calls = %d, want %d", calls, tc.wantCalls)
				}
				if calls > 0 && (gotTty != tc.tty || gotPath != tc.cwd) {
					t.Fatalf("OpenGhostty(%q, %q), want (%q, %q)", gotTty, gotPath, tc.tty, tc.cwd)
				}
			})
		}
	}
}
