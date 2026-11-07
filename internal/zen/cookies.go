package zen

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultProfile finds the Zen profile that owns the freshest cookies.sqlite.
func DefaultProfile() (string, error) {
	if p := os.Getenv("ZDL_ZEN_PROFILE"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".config", "zen")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("no Zen profiles under %s: %w", base, err)
	}

	type candidate struct {
		path string
		mod  time.Time
	}
	var all []candidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		db := filepath.Join(base, e.Name(), "cookies.sqlite")
		info, err := os.Stat(db)
		if err != nil {
			continue
		}
		all = append(all, candidate{path: filepath.Join(base, e.Name()), mod: info.ModTime()})
	}
	if len(all) == 0 {
		return "", fmt.Errorf("no cookies.sqlite found in %s", base)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	return all[0].path, nil
}

// CookieHeader reads Zen's cookies for the URL's host and returns a Cookie
// header value. It works on a temporary SQLite backup, so a running browser
// is fine.
func CookieHeader(profile, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("URL has no host")
	}

	src := filepath.Join(profile, "cookies.sqlite")
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("cannot read %s: %w", src, err)
	}

	tmpDir, err := os.MkdirTemp("", "zdl-cookies-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	db := filepath.Join(tmpDir, "cookies.sqlite")

	backup := exec.Command("sqlite3", src, ".backup '"+strings.ReplaceAll(db, "'", "''")+"'")
	if out, err := backup.CombinedOutput(); err != nil {
		return "", fmt.Errorf("copying cookie database: %v: %s", err, strings.TrimSpace(string(out)))
	}

	query := `SELECT host,name,value,secure,expiry FROM moz_cookies WHERE expiry = 0 OR expiry > strftime('%s','now')`
	cmd := exec.Command("sqlite3", "-separator", "\t", db, query)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("reading cookies: %w", err)
	}

	secureOnly := u.Scheme == "https"
	var parts []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 5)
		if len(f) != 5 {
			continue
		}
		cookieHost, name, value := f[0], f[1], f[2]
		secure, _ := strconv.Atoi(f[3])
		if secure == 1 && !secureOnly {
			continue
		}
		if !hostMatch(cookieHost, host) {
			continue
		}
		parts = append(parts, name+"="+value)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no stored cookies for %s", host)
	}
	return strings.Join(parts, "; "), nil
}

func hostMatch(cookieHost, reqHost string) bool {
	cookieHost = strings.TrimSpace(cookieHost)
	if cookieHost == "" {
		return false
	}
	if strings.HasPrefix(cookieHost, ".") {
		base := strings.TrimPrefix(cookieHost, ".")
		return reqHost == base || strings.HasSuffix(reqHost, "."+base)
	}
	return reqHost == cookieHost
}
