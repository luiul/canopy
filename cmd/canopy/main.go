// Command canopy is an interactive dashboard for every agent CLI session
// (pi, claude, codex, ...) running on this machine, wherever it actually
// is: a VS Code integrated terminal, or a bare Ghostty tab, with its live
// state and jump-to-window on Enter.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
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

Arrow keys to move, Enter to jump to a window (and mark it as seen), c to
mark a done row as seen without jumping, x/X to terminate/kill the selected
session (with a confirmation prompt), p to pause/resume it, D to terminate
every done session at once, q to quit, r to refresh. Press ? in the app for
the full keybinding list.

Usage:
  canopy [flags]

Flags:
  --interval <seconds>  Poll interval in seconds (default 2).
  --no-color            Disable color output (also respects NO_COLOR).
  --no-bell             Disable the terminal bell on new done rows.
  --version             Show the version and exit.
  -h, --help            Show this help and exit.
`

// config is parseFlags' validated result: exactly what main needs to
// decide what to do next, split out from flag.FlagSet's own parsing so
// that decision is unit-testable without exec'ing the real binary (see
// main_test.go).
type config struct {
	interval    time.Duration
	noColor     bool
	noBell      bool
	showVersion bool
}

// parseFlags parses and validates args (os.Args[1:] in production),
// writing usage/error text to out. It returns the same errors flag.Parse
// itself would (including flag.ErrHelp for -h/--help, which callers should
// treat as "exit 0, not an error") plus canopy's own validation
// (--interval must be positive).
func parseFlags(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("canopy", flag.ContinueOnError)
	fs.SetOutput(out)
	interval := fs.Float64("interval", tui.DefaultInterval.Seconds(), "Poll interval in seconds.")
	noColor := fs.Bool("no-color", false, "Disable color output (also respects NO_COLOR).")
	noBell := fs.Bool("no-bell", false, "Disable the terminal bell on new done rows.")
	showVersion := fs.Bool("version", false, "Show the version and exit.")
	fs.Usage = func() { _, _ = fmt.Fprint(out, helpText) }

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	if *showVersion {
		return config{showVersion: true}, nil
	}

	if *interval <= 0 {
		err := errors.New("canopy: --interval must be positive")
		_, _ = fmt.Fprintln(out, err)
		return config{}, err
	}

	return config{
		interval: time.Duration(*interval * float64(time.Second)),
		noColor:  *noColor,
		noBell:   *noBell,
	}, nil
}

// checkPlatform reports an error if goos isn't "darwin". canopy's process
// discovery (ps/lsof output parsing in internal/scan), ancestry
// classification, and jump-to-window (AppleScript for Ghostty, `code
// --reuse-window` for VS Code) are all macOS-specific — documented in
// README.md's Limitations section, not an oversight. Without this check,
// running canopy on another OS wouldn't fail outright; BSD ps's column
// output differs from GNU ps's in ways that mostly just fail to parse
// silently, so canopy would sit there reporting "no known agent-kind
// processes found on this machine" with no indication that it's actually
// running on an unsupported platform, indistinguishable from the
// genuinely-idle case. goos is a parameter (rather than checking
// runtime.GOOS directly) so this is testable on every platform this is
// itself built and tested on, not just macOS.
func checkPlatform(goos string) error {
	if goos != "darwin" {
		return fmt.Errorf("canopy requires macOS (its process discovery relies on ps/lsof output and AppleScript specific to it); detected %s", goos)
	}
	return nil
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		// parseFlags (validation errors) and the flag package itself (parse
		// errors, via FlagSet.Output) have already written the error/usage
		// text to os.Stderr by this point; nothing left to print here.
		os.Exit(2)
	}

	if cfg.showVersion {
		fmt.Printf("canopy %s\n", version)
		return
	}

	if err := checkPlatform(runtime.GOOS); err != nil {
		fmt.Fprintln(os.Stderr, "canopy:", err)
		os.Exit(1)
	}

	if cfg.noColor || os.Getenv("NO_COLOR") != "" {
		lipgloss.SetColorProfile(termenv.Ascii)
	}

	if err := tui.Run(cfg.interval, !cfg.noBell); err != nil {
		fmt.Fprintln(os.Stderr, "canopy:", err)
		os.Exit(1)
	}
}
