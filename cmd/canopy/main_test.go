package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"
)

func TestParseFlagsDefaults(t *testing.T) {
	var out bytes.Buffer
	cfg, err := parseFlags(nil, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.showVersion || cfg.noColor || cfg.noBell {
		t.Fatalf("got %+v, want all flags false by default", cfg)
	}
	if cfg.interval != 2*time.Second {
		t.Fatalf("got interval %v, want the tui package's DefaultInterval (2s)", cfg.interval)
	}
}

func TestParseFlagsInterval(t *testing.T) {
	var out bytes.Buffer
	cfg, err := parseFlags([]string{"--interval", "0.5"}, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.interval != 500*time.Millisecond {
		t.Fatalf("got interval %v, want 500ms", cfg.interval)
	}
}

func TestParseFlagsRejectsANonPositiveInterval(t *testing.T) {
	for _, bad := range []string{"0", "-1"} {
		var out bytes.Buffer
		_, err := parseFlags([]string{"--interval", bad}, &out)
		if err == nil {
			t.Fatalf("--interval %s: want an error, got none", bad)
		}
		if !strings.Contains(out.String(), "--interval must be positive") {
			t.Fatalf("--interval %s: got stderr %q, want it to explain why", bad, out.String())
		}
	}
}

func TestParseFlagsNoColorAndNoBell(t *testing.T) {
	var out bytes.Buffer
	cfg, err := parseFlags([]string{"--no-color", "--no-bell"}, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.noColor || !cfg.noBell {
		t.Fatalf("got %+v, want both no-color and no-bell true", cfg)
	}
}

func TestParseFlagsVersion(t *testing.T) {
	var out bytes.Buffer
	cfg, err := parseFlags([]string{"--version"}, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.showVersion {
		t.Fatalf("got %+v, want showVersion true", cfg)
	}
}

func TestParseFlagsHelpReturnsErrHelpAndPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	_, err := parseFlags([]string{"--help"}, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got err %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("got stderr %q, want the help text", out.String())
	}
}

func TestParseFlagsUnknownFlagPrintsUsageAndReturnsAnError(t *testing.T) {
	var out bytes.Buffer
	_, err := parseFlags([]string{"--bogus"}, &out)
	if err == nil {
		t.Fatalf("want an error for an unrecognized flag, got none")
	}
	if errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got flag.ErrHelp, want a plain parse error")
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("got stderr %q, want the help text on a parse error too", out.String())
	}
}

func TestCheckPlatformAcceptsDarwinOnly(t *testing.T) {
	if err := checkPlatform("darwin"); err != nil {
		t.Fatalf("checkPlatform(darwin) = %v, want nil", err)
	}
	for _, goos := range []string{"linux", "windows", "freebsd"} {
		if err := checkPlatform(goos); err == nil {
			t.Fatalf("checkPlatform(%s) = nil, want an error", goos)
		}
	}
}
