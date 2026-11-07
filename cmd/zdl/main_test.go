package main

import (
	"flag"
	"testing"
)

// TestReorderArgsFlagsAfterURL guards the CLI convenience of putting flags
// after the URL. Bool aliases such as -y must be in boolFlags, otherwise they
// swallow the following flag and its value (e.g. "-y --rate 40M").
func TestReorderArgsFlagsAfterURL(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{
			name: "rate after bool alias",
			args: []string{"https://example.test/file.bin", "-y", "--rate", "40M", "-d", "/tmp/dl"},
		},
		{
			name: "rate equals form",
			args: []string{"https://example.test/file.bin", "--rate=40M", "-y", "-d", "/tmp/dl"},
		},
		{
			name: "bool before value flag",
			args: []string{"-x", "8", "https://example.test/file.bin", "-q", "-y", "-d", "/tmp/dl"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			rate := fs.String("rate", "", "")
			dir := fs.String("d", "", "")
			yes := fs.Bool("y", false, "")
			quiet := fs.Bool("q", false, "")
			conns := fs.Int("x", 16, "")

			if err := fs.Parse(reorderArgs(tc.args, boolFlags)); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if tc.name == "bool before value flag" {
				if *rate != "" {
					t.Errorf("rate = %q, want empty", *rate)
				}
				if !*quiet || *conns != 8 {
					t.Errorf("quiet=%v conns=%d, want true/8", *quiet, *conns)
				}
			} else if *rate != "40M" {
				t.Errorf("rate = %q, want 40M", *rate)
			}
			if *dir != "/tmp/dl" {
				t.Errorf("dir = %q, want /tmp/dl", *dir)
			}
			if !*yes {
				t.Error("-y not set")
			}
			if tc.name == "bool before value flag" {
				if !*quiet || *conns != 8 {
					t.Errorf("quiet=%v conns=%d, want true/8", *quiet, *conns)
				}
			}
			if fs.NArg() != 1 || fs.Arg(0) != "https://example.test/file.bin" {
				t.Errorf("positional args = %v, want just the URL", fs.Args())
			}
		})
	}
}
