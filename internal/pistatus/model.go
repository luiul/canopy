package pistatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ModelMaxAge allows idle pi sessions to keep showing their selected model
// between the extension's 30s model heartbeats. A crashed extension's last
// report stops being trusted once the heartbeat has been missing for 90s.
const ModelMaxAge = 90 * time.Second

// Model is pi's selected model and provider, not a routed physical model.
type Model struct {
	Pid       int
	Name      string
	Provider  string
	UpdatedAt time.Time
}

type wireModel struct {
	Pid       int       `json:"pid"`
	Name      string    `json:"model"`
	Provider  string    `json:"provider"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ReadModel returns the selected model for pid, if a recent model report
// exists. Model freshness is independent of Read's agent-state freshness:
// a model selection must not look like a new done event.
func ReadModel(pid int) (Model, bool) {
	return ReadModelDir(Dir(), pid, time.Now())
}

// ReadModelDir is ReadModel with an explicit directory and clock for tests.
func ReadModelDir(dir string, pid int, now time.Time) (Model, bool) {
	if dir == "" {
		return Model{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+".model.json"))
	if err != nil {
		return Model{}, false
	}
	var w wireModel
	if err := json.Unmarshal(data, &w); err != nil || w.Pid != pid ||
		strings.TrimSpace(w.Name) == "" || strings.TrimSpace(w.Provider) == "" ||
		w.UpdatedAt.IsZero() || now.Before(w.UpdatedAt) || now.Sub(w.UpdatedAt) > ModelMaxAge {
		return Model{}, false
	}
	return Model{Pid: w.Pid, Name: w.Name, Provider: w.Provider, UpdatedAt: w.UpdatedAt}, true
}
