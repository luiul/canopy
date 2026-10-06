// Package config loads canopy's optional user configuration: the set of
// agent CLI kinds to track, from $XDG_CONFIG_HOME/canopy/config.toml
// (default ~/.config/canopy/config.toml), the same XDG convention
// worktrunk's ~/.config/worktrunk/config.toml uses.
//
// The file holds one key, `agents`, a list of argv0 basenames:
//
//	agents = ["pi", "pig", "claude"]
//
// When the file exists, that list is the complete tracked set: replace
// semantics, no silent merge with hidden defaults. When the file is
// missing, DefaultAgents applies, no error. Anything wrong with an
// existing file (unreadable, malformed TOML, an unknown key, a missing
// or empty `agents` list, an implausible name) is a startup error, the
// same fail-fast posture as cmd/canopy's checkPlatform: a hand-edited
// file that doesn't say what its author meant should complain loudly at
// startup rather than silently track the wrong thing.
//
// Matching rules are unchanged by where the set came from: a process
// shows up when its executable basename in `ps` output equals a listed
// name exactly, it has a controlling terminal, and its second token
// isn't denylisted (see internal/scan.ParsePsOutput).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

// DefaultAgents is the tracked set when no config file exists: just
// `pi`. Deliberately minimal: anything beyond it belongs in the config
// file, and the README's Configuration section keeps the longer list
// the binary used to hardcode as a copy-paste example.
var DefaultAgents = []string{"pi"}

// Config is a loaded and validated configuration.
type Config struct {
	// Agents is the complete tracked set of agent CLI kinds: validated
	// (every name is a plausible argv0 basename) and deduplicated, in
	// file order.
	Agents []string
}

// Set returns Agents as a lookup set, the shape internal/scan's
// matchers take.
func (c Config) Set() map[string]bool {
	set := make(map[string]bool, len(c.Agents))
	for _, name := range c.Agents {
		set[name] = true
	}
	return set
}

// Path resolves the config file's location:
// $XDG_CONFIG_HOME/canopy/config.toml when XDG_CONFIG_HOME is set to an
// absolute path, ~/.config/canopy/config.toml otherwise. A relative
// XDG_CONFIG_HOME is ignored rather than honored, as the XDG Base
// Directory spec requires: it mandates absolute paths and declares
// relative ones invalid.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "canopy", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving the config file path: %w", err)
	}
	return filepath.Join(home, ".config", "canopy", "config.toml"), nil
}

// Load reads and validates the config file at path. A missing file is
// not an error: it yields DefaultAgents. Any other failure (unreadable,
// malformed, invalid) returns an error that names the path, since a
// config the user wrote but canopy can't honor must stop the startup,
// not silently fall back to the defaults the user meant to replace.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{Agents: append([]string(nil), DefaultAgents...)}, nil
		}
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}

	// DisallowUnknownFields so a typo'd key ("agent = [...]") is a
	// startup error rather than silently decoding to an empty list. The
	// strict-mode error's own message ("fields in the document are
	// missing in the target struct") never names the key, so reword it
	// with the offending names from the per-field decode errors.
	var raw struct {
		Agents []string `toml:"agents"`
	}
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			names := make([]string, 0, len(strict.Errors))
			for _, de := range strict.Errors {
				names = append(names, strconv.Quote(strings.Join(de.Key(), ".")))
			}
			return Config{}, fmt.Errorf("%s: unknown %s %s: the only supported key is 'agents'", path, plural(len(names), "key", "keys"), strings.Join(names, ", "))
		}
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}

	if len(raw.Agents) == 0 {
		return Config{}, fmt.Errorf("%s: 'agents' must be a non-empty list of agent CLI names (delete the file to use the built-in default: %s)", path, strings.Join(DefaultAgents, ", "))
	}

	seen := make(map[string]bool, len(raw.Agents))
	agents := make([]string, 0, len(raw.Agents))
	for _, name := range raw.Agents {
		if err := validateName(name); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		if !seen[name] {
			seen[name] = true
			agents = append(agents, name)
		}
	}
	return Config{Agents: agents}, nil
}

// plural picks the noun form for a count, for error messages that list
// a variable number of offending items.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// validateName checks that name is a plausible argv0 basename:
// non-empty, without slashes or whitespace. Names are matched exactly
// against the executable basename in `ps` output (see
// internal/scan.ParsePsOutput), so anything that couldn't be one would
// silently never match; better to say so at startup.
func validateName(name string) error {
	switch {
	case name == "":
		return errors.New(`agents: empty name: entries must be executable basenames like "pi"`)
	case strings.Contains(name, "/"):
		return fmt.Errorf("agents: %q: names are matched against the executable basename, so they must not contain a slash", name)
	case strings.IndexFunc(name, unicode.IsSpace) != -1:
		return fmt.Errorf("agents: %q: names are matched against a single whitespace-delimited token, so they must not contain whitespace", name)
	}
	return nil
}
