package config

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		bad  bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1024", 1024, false},
		{"1K", 1024, false},
		{"1KB", 1000, false},
		{"1KiB", 1024, false},
		{"1.5M", 1572864, false},
		{"8M", 8 << 20, false},
		{"500K", 500 << 10, false},
		{"2G", 2 << 30, false},
		{"1GB", 1000 * 1000 * 1000, false},
		{"-1", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("ParseSize(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	c := &Config{Conns: 0, MaxConcurrent: 99}
	c.Normalize()
	if c.Conns != 16 {
		t.Errorf("Conns = %d, want 16", c.Conns)
	}
	if c.MaxConcurrent != 16 {
		t.Errorf("MaxConcurrent = %d, want 16", c.MaxConcurrent)
	}
	if c.Dir == "" {
		t.Error("Dir should have a default")
	}
}
