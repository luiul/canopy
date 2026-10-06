package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeConfig writes content to a config.toml inside a fresh temp dir and
// returns the file's path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing the test config: %v", err)
	}
	return path
}

func TestPathPrefersXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	p, err := Path()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != "/xdg/canopy/config.toml" {
		t.Fatalf("got %q, want /xdg/canopy/config.toml", p)
	}
}

func TestPathFallsBackToHomeDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := Path()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(home, ".config", "canopy", "config.toml")
	if p != want {
		t.Fatalf("got %q, want %q", p, want)
	}
}

func TestPathIgnoresARelativeXDGConfigHome(t *testing.T) {
	// The XDG Base Directory spec mandates absolute paths and declares
	// relative ones invalid: a relative XDG_CONFIG_HOME must be ignored,
	// not silently joined into a cwd-relative config path.
	t.Setenv("XDG_CONFIG_HOME", "relative/dir")
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := Path()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(home, ".config", "canopy", "config.toml")
	if p != want {
		t.Fatalf("got %q, want %q (relative XDG_CONFIG_HOME ignored)", p, want)
	}
}

func TestLoadMissingFileYieldsTheDefaults(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.Agents, DefaultAgents) {
		t.Fatalf("got %v, want the defaults %v", got.Agents, DefaultAgents)
	}
	// The defaults must come out as a copy: mutating a loaded Config must
	// not corrupt DefaultAgents for the next load.
	got.Agents[0] = "mutated"
	again, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(again.Agents, DefaultAgents) {
		t.Fatalf("got %v after mutating the first load, want the untouched defaults %v", again.Agents, DefaultAgents)
	}
}

func TestLoadValidFileReplacesTheDefaults(t *testing.T) {
	got, err := Load(writeConfig(t, "agents = [\"pi\", \"pig\"]\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.Agents, []string{"pi", "pig"}) {
		t.Fatalf("got %v, want [pi pig]: a config file replaces the defaults, no merge", got.Agents)
	}
}

func TestLoadDeduplicatesNames(t *testing.T) {
	got, err := Load(writeConfig(t, "agents = [\"pi\", \"pi\", \"pig\", \"pi\"]\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.Agents, []string{"pi", "pig"}) {
		t.Fatalf("got %v, want [pi pig] (deduped, first occurrence wins)", got.Agents)
	}
}

func TestLoadAllowsCommentsAndACustomKind(t *testing.T) {
	content := "# the agents I actually run\nagents = [\"myagent\"] # tracked\n"
	got, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.Agents, []string{"myagent"}) {
		t.Fatalf("got %v, want [myagent]", got.Agents)
	}
}

func TestLoadRejectsMalformedTOML(t *testing.T) {
	path := writeConfig(t, "agents = [\"pi\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatalf("want an error for malformed TOML, got none")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("got error %q, want it to name the config path", err)
	}
}

func TestLoadRejectsUnknownKeysByName(t *testing.T) {
	path := writeConfig(t, "agents = [\"pi\"]\nagent = [\"pig\"]\n")
	_, err := Load(path)
	if err == nil {
		t.Fatalf("want an error for an unknown key, got none")
	}
	if !strings.Contains(err.Error(), `"agent"`) || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("got error %q, want it to name the unknown key", err)
	}
}

func TestLoadRejectsAMissingOrEmptyAgentsList(t *testing.T) {
	for _, content := range []string{"", "# only a comment\n", "agents = []\n"} {
		path := writeConfig(t, content)
		_, err := Load(path)
		if err == nil {
			t.Fatalf("content %q: want an error for a missing/empty agents list, got none", content)
		}
		if !strings.Contains(err.Error(), "'agents' must be a non-empty list") {
			t.Fatalf("content %q: got error %q, want it to explain the missing/empty list", content, err)
		}
	}
}

func TestLoadRejectsAnAgentsValueThatIsNotAListOfStrings(t *testing.T) {
	for _, content := range []string{"agents = \"pi\"\n", "agents = [1, 2]\n"} {
		path := writeConfig(t, content)
		if _, err := Load(path); err == nil {
			t.Fatalf("content %q: want a decode error, got none", content)
		}
	}
}

func TestLoadRejectsImplausibleNames(t *testing.T) {
	for _, name := range []string{"", "a/b", "/usr/local/bin/pi", "my agent", "pi\t", " pi"} {
		content := "agents = [" + `"` + name + `"]` + "\n"
		path := writeConfig(t, content)
		if _, err := Load(path); err == nil {
			t.Fatalf("name %q: want a validation error, got none", name)
		}
	}
}

func TestLoadWrapsAnUnreadableFile(t *testing.T) {
	// A directory at the config path: it exists, so this is not the
	// defaults case, but it can't be read as a file.
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatalf("want an error for an unreadable config path, got none")
	}
}

func TestSetBuildsALookupMap(t *testing.T) {
	set := Config{Agents: []string{"pi", "pig"}}.Set()
	if !set["pi"] || !set["pig"] || set["claude"] {
		t.Fatalf("got %v, want pi and pig only", set)
	}
}
