package pistatus

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeModelFile(t *testing.T, dir string, pid int, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".model.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write model fixture: %v", err)
	}
}

func TestReadModelDirReturnsFreshSelectedModel(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-60 * time.Second) // idle longer than the state's 10s MaxAge
	writeModelFile(t, dir, 123, `{"pid":123,"model":"GPT-6 Sol","provider":"ai-model-router","updatedAt":"`+updatedAt.Format(time.RFC3339Nano)+`"}`)

	got, ok := ReadModelDir(dir, 123, now)
	if !ok || got != (Model{Pid: 123, Name: "GPT-6 Sol", Provider: "ai-model-router", UpdatedAt: updatedAt}) {
		t.Fatalf("got %+v, ok=%v, want the selected model", got, ok)
	}
}

func TestReadModelDirRejectsInvalidReports(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		body string
	}{
		{"malformed", `not json`},
		{"wrong pid", `{"pid":999,"model":"GPT-6 Sol","provider":"ai-model-router","updatedAt":"2026-01-01T12:00:00Z"}`},
		{"no model", `{"pid":123,"model":"  ","provider":"ai-model-router","updatedAt":"2026-01-01T12:00:00Z"}`},
		{"no provider", `{"pid":123,"model":"GPT-6 Sol","provider":"","updatedAt":"2026-01-01T12:00:00Z"}`},
		{"no timestamp", `{"pid":123,"model":"GPT-6 Sol","provider":"ai-model-router"}`},
		{"stale", `{"pid":123,"model":"GPT-6 Sol","provider":"ai-model-router","updatedAt":"2026-01-01T11:58:29Z"}`},
		{"future", `{"pid":123,"model":"GPT-6 Sol","provider":"ai-model-router","updatedAt":"2026-01-01T12:00:01Z"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeModelFile(t, dir, 123, tc.body)
			if got, ok := ReadModelDir(dir, 123, now); ok {
				t.Fatalf("got %+v, want no selected model", got)
			}
		})
	}
	for _, dir := range []string{"", t.TempDir()} {
		if got, ok := ReadModelDir(dir, 123, now); ok {
			t.Fatalf("dir %q: got %+v, want no selected model", dir, got)
		}
	}
}
