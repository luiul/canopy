// Command canopy is an interactive dashboard for every agent CLI session
// (pi, claude, codex, ...) running on this machine, wherever it actually
// is: a VS Code integrated terminal, or a bare Ghostty tab, with its live
// state and jump-to-window on Enter.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/luiul/canopy/internal/tui"
)

// version is overridden at build time via:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "0.1.0-dev"

const helpText = `canopy: interactive dashboard for every pi/claude/codex/... session on
this machine: VS Code integrated terminals and bare Ghostty tabs alike.

Arrow keys to move, Enter to jump to a window, q to quit, r to refresh.

Usage:
  canopy [flags]

Flags:
  --interval <seconds>  Poll interval in seconds (default 2).
  --no-color             Disable color output (also respects NO_COLOR).
  --no-bell              Disable the terminal bell on new blocked/done rows.
  --version             Show the version and exit.
  -h, --help            Show this help and exit.
`

func main() {
	fs := flag.NewFlagSet("canopy", flag.ExitOnError)
	interval := fs.Float64("interval", tui.DefaultInterval.Seconds(), "Poll interval in seconds.")
	noColor := fs.Bool("no-color", false, "Disable color output (also respects NO_COLOR).")
	noBell := fs.Bool("no-bell", false, "Disable the terminal bell on new blocked/done rows.")
	showVersion := fs.Bool("version", false, "Show the version and exit.")
	fs.Usage = func() { fmt.Fprint(os.Stderr, helpText) }

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	if *showVersion {
		fmt.Printf("canopy %s\n", version)
		return
	}

	if *interval <= 0 {
		fmt.Fprintln(os.Stderr, "canopy: --interval must be positive")
		os.Exit(2)
	}

	if *noColor || os.Getenv("NO_COLOR") != "" {
		lipgloss.SetColorProfile(termenv.Ascii)
	}

	if err := tui.Run(time.Duration(*interval*float64(time.Second)), !*noBell); err != nil {
		fmt.Fprintln(os.Stderr, "canopy:", err)
		os.Exit(1)
	}
}
