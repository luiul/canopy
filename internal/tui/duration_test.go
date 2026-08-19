package tui

import (
	"testing"
	"time"
)

func TestHumanizeSince(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{3 * time.Second, "3s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{45 * time.Minute, "45m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{time.Hour + 15*time.Minute, "1h15m"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		{24 * time.Hour, "1d"},
		{3 * 24 * time.Hour, "3d"},
	}
	for _, c := range cases {
		if got := humanizeSince(c.d); got != c.want {
			t.Errorf("humanizeSince(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
