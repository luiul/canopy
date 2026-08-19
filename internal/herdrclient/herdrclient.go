// Package herdrclient is a thin subprocess wrapper around the `herdr`
// binary.
//
// canopy does not reimplement anything herdr already owns: pane discovery,
// process introspection, and pane/tab/workspace focus all stay herdr's job.
// This package only shells out to `herdr` and parses its JSON output.
// canopy never writes anything back into herdr (no metadata tokens, no
// config changes); herdr is a data source and a focus target here, not
// something canopy extends.
//
// Every exported function here degrades gracefully to "no herdr rows" when
// the `herdr` binary isn't on PATH, matching canopy's own documented
// behavior (herdr-tracked rows just don't show up) rather than erroring the
// whole poll.
package herdrclient

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

const defaultTimeout = 5 * time.Second

func binaryPath() (string, bool) {
	path, err := exec.LookPath("herdr")
	return path, err == nil
}

// Available reports whether the `herdr` binary is on PATH.
func Available() bool {
	_, ok := binaryPath()
	return ok
}

func run(args []string, timeout time.Duration) (stdout string, ok bool) {
	bin, found := binaryPath()
	if !found {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func runJSON(args []string, timeout time.Duration, out any) bool {
	stdout, ok := run(args, timeout)
	if !ok {
		return false
	}
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return false
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		return false
	}
	return true
}

// Pane is one entry from `herdr pane list`: agent kind, agent_status
// (herdr's own idle/working/blocked/done/unknown), cwd, and its
// workspace_id/tab_id/pane_id.
type Pane struct {
	Agent       string `json:"agent"`
	PaneID      string `json:"pane_id"`
	Cwd         string `json:"cwd"`
	AgentStatus string `json:"agent_status"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
}

type paneListResponse struct {
	Result struct {
		Panes []Pane `json:"panes"`
	} `json:"result"`
}

// PaneList returns every pane in the current session.
func PaneList() []Pane {
	var resp paneListResponse
	if !runJSON([]string{"pane", "list"}, defaultTimeout, &resp) {
		return nil
	}
	return resp.Result.Panes
}

// ProcessInfo is the shell + foreground process info for one pane.
type ProcessInfo struct {
	ForegroundProcessGroupID *int64 `json:"foreground_process_group_id"`
}

type paneProcessInfoResponse struct {
	Result struct {
		ProcessInfo *ProcessInfo `json:"process_info"`
	} `json:"result"`
}

// PaneProcessInfo returns shell + foreground process pids/cwds for one
// pane, or nil if the pane vanished mid-poll or the call failed for any
// other reason. This is what lets canopy's own `ps` scan tell "this pid is
// already inside a herdr pane" apart from an agent running anywhere else,
// since `pane list` alone doesn't expose the underlying OS pid.
func PaneProcessInfo(paneID string) *ProcessInfo {
	var resp paneProcessInfoResponse
	if !runJSON([]string{"pane", "process-info", "--pane", paneID}, defaultTimeout, &resp) {
		return nil
	}
	return resp.Result.ProcessInfo
}

func runOK(args []string) bool {
	bin, found := binaryPath()
	if !found {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	err := exec.CommandContext(ctx, bin, args...).Run()
	return err == nil
}

// FocusWorkspace asks herdr to focus a workspace by id.
func FocusWorkspace(workspaceID string) bool {
	return runOK([]string{"workspace", "focus", workspaceID})
}

// FocusTab asks herdr to focus a tab by id.
func FocusTab(tabID string) bool {
	return runOK([]string{"tab", "focus", tabID})
}

// FocusPane asks herdr to focus a pane by id.
func FocusPane(paneID string) bool {
	return runOK([]string{"pane", "focus", "--pane", paneID})
}
