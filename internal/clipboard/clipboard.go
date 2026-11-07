package clipboard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Tool returns the command used to read the clipboard, or "" if none exists.
func Tool() string {
	switch runtime.GOOS {
	case "darwin":
		if look("pbpaste") {
			return "pbpaste"
		}
	case "windows":
		if look("powershell") {
			return "powershell"
		}
		if look("pwsh") {
			return "pwsh"
		}
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" && look("wl-paste") {
			return "wl-paste"
		}
		for _, t := range []string{"wl-paste", "xclip", "xsel"} {
			if look(t) {
				return t
			}
		}
	}
	return ""
}

func look(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func read(tool string) (string, error) {
	var cmd *exec.Cmd
	switch tool {
	case "wl-paste":
		cmd = exec.Command(tool, "--no-newline", "--type", "text")
	case "xclip":
		cmd = exec.Command(tool, "-selection", "clipboard", "-o")
	case "xsel":
		cmd = exec.Command(tool, "--clipboard", "--output")
	case "pbpaste":
		cmd = exec.Command(tool)
	case "powershell", "pwsh":
		cmd = exec.Command(tool, "-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw")
	default:
		return "", fmt.Errorf("no clipboard tool")
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// Watch polls the clipboard and sends changed non-empty text. It returns when
// ctx is cancelled or no clipboard tool is available.
func Watch(ctx context.Context, out chan<- string) {
	tool := Tool()
	if tool == "" {
		return
	}
	var last string
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s, err := read(tool)
		if err != nil || s == "" || s == last {
			continue
		}
		last = s
		select {
		case out <- s:
		case <-ctx.Done():
			return
		}
	}
}

var urlPattern = regexp.MustCompile("https?://[^[:space:]\"'<>\\\\^`]+")

// ExtractURLs pulls http(s) links out of arbitrary clipboard text.
func ExtractURLs(s string) []string {
	matches := urlPattern.FindAllString(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		m = strings.TrimRight(m, ".,;:!?)]}'\"")
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}
