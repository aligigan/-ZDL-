package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"zdl/internal/downloader"
	"zdl/internal/zen"
)

var version = "0.2.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		if bothTTY() {
			return cmdUI(nil)
		}
		usage()
		return 2
	}
	switch args[0] {
	case "ui":
		return cmdUI(args[1:])
	case "get":
		return cmdGet(args[1:])
	case "resume":
		return cmdResume(args[1:])
	case "clip":
		return cmdClip(args[1:])
	case "list":
		return cmdList(args[1:])
	case "info":
		return cmdInfo(args[1:])
	case "clean":
		return cmdClean(args[1:])
	case "version", "--version", "-V":
		fmt.Println("zdl", version)
		return 0
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "zdl: unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Print(`zdl — parallel, resumable downloader with Zen Browser cookies

usage:
  zdl [ui]                              open the full-screen dashboard (TTY)
  zdl get URL [URL...] [flags]          download (auto-resumes existing .part)
  zdl resume FILE [--url NEWURL] [flags] resume, optionally with a fresh link
  zdl clip [--auto] [flags]             watch the clipboard for file links
  zdl list [-d DIR]                     show resumable downloads
  zdl info FILE                         show saved state for a download
  zdl clean FILE                        delete a .part and its state
  zdl version

download flags:
  -o, --output FILE        final filename/path
  -d, --dir DIR            download directory (default ~/Downloads)
  -x, --connections N      parallel connections, 1-64 (default 16)
      --retries N          retries per chunk (default 5)
      --cookie STR         Cookie header value
      --cookies-from-zen   read cookies from the current Zen profile
      --zen-profile DIR    override the Zen profile path
      --referer URL        Referer header
      --user-agent STR     User-Agent header
      --header "K: V"      extra header (repeatable)
      --rate SIZE          global speed limit, e.g. 8M, 500K
      --sha256 HEX         verify the finished file
      --insecure           skip TLS verification
  -y, --yes                skip the start/cancel confirmation
      --force              overwrite an existing final file
      --restart            discard saved progress and start over
  -q, --quiet              no live progress
      --verbose            print probe details
      --notify             desktop notification when finished
`)
}

// ---------- shared flags ----------

type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ", ") }
func (h *headerFlags) Set(v string) error {
	*h = append(*h, v)
	return nil
}

type commonFlags struct {
	dir            string
	conns          int
	retries        int
	cookie         string
	cookiesFromZen bool
	zenProfile     string
	referer        string
	userAgent      string
	headers        headerFlags
	insecure       bool
	yes            bool
	rate           string
	quiet          bool
	quietWarn      bool
	verbose        bool
	notify         bool
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.dir, "dir", "", "download directory")
	fs.StringVar(&c.dir, "d", "", "download directory")
	fs.IntVar(&c.conns, "connections", 16, "parallel connections (1-64)")
	fs.IntVar(&c.conns, "x", 16, "parallel connections (1-64)")
	fs.IntVar(&c.retries, "retries", 5, "retries per chunk")
	fs.StringVar(&c.cookie, "cookie", "", "Cookie header value")
	fs.BoolVar(&c.cookiesFromZen, "cookies-from-zen", false, "read cookies from Zen")
	fs.StringVar(&c.zenProfile, "zen-profile", "", "Zen profile directory")
	fs.StringVar(&c.referer, "referer", "", "Referer header")
	fs.StringVar(&c.userAgent, "user-agent", "", "User-Agent header")
	fs.Var(&c.headers, "header", `extra header, e.g. "X-Foo: bar"`)
	fs.BoolVar(&c.insecure, "insecure", false, "skip TLS verification")
	fs.BoolVar(&c.yes, "yes", false, "skip the start/cancel prompt")
	fs.BoolVar(&c.yes, "y", false, "skip the start/cancel prompt")
	fs.StringVar(&c.rate, "rate", "", "speed limit, e.g. 8M")
	fs.BoolVar(&c.quiet, "quiet", false, "no live progress")
	fs.BoolVar(&c.quiet, "q", false, "no live progress")
	fs.BoolVar(&c.verbose, "verbose", false, "print probe details")
	fs.BoolVar(&c.notify, "notify", false, "desktop notification when finished")
}

func (c *commonFlags) headersFor(rawURL string) (downloader.Headers, error) {
	h := downloader.Headers{
		UserAgent: c.userAgent,
		Referer:   c.referer,
		Cookie:    c.cookie,
		Extra:     map[string]string{},
	}
	for _, kv := range c.headers {
		i := strings.IndexByte(kv, ':')
		if i <= 0 {
			return h, fmt.Errorf("bad --header %q (want \"Name: value\")", kv)
		}
		name := strings.TrimSpace(kv[:i])
		value := strings.TrimSpace(kv[i+1:])
		if name == "" {
			return h, fmt.Errorf("bad --header %q", kv)
		}
		h.Extra[name] = value
	}
	if h.Cookie == "" && c.cookiesFromZen {
		profile := c.zenProfile
		if profile == "" {
			p, err := zen.DefaultProfile()
			if err != nil {
				c.warnf("zdl: warning: could not find Zen cookies: %v\n", err)
			} else {
				profile = p
			}
		}
		if profile != "" {
			cookie, err := zen.CookieHeader(profile, rawURL)
			if err != nil {
				c.warnf("zdl: warning: Zen cookies: %v\n", err)
			} else {
				h.Cookie = cookie
			}
		}
	}
	return h, nil
}

// warnf suppresses warnings that would corrupt the TUI's alt screen.
func (c *commonFlags) warnf(format string, args ...any) {
	if c.quietWarn {
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
}

func (c *commonFlags) rateBytes() (int64, error) {
	if strings.TrimSpace(c.rate) == "" {
		return 0, nil
	}
	return parseHumanSize(c.rate)
}

func (c *commonFlags) validate() error {
	if c.conns < 1 || c.conns > 64 {
		return fmt.Errorf("--connections must be between 1 and 64")
	}
	if c.retries < 0 {
		return fmt.Errorf("--retries cannot be negative")
	}
	if _, err := c.rateBytes(); err != nil {
		return fmt.Errorf("--rate: %w", err)
	}
	return nil
}

// ---------- get ----------

func cmdGet(args []string) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var common commonFlags
	common.register(fs)
	output := fs.String("output", "", "final filename/path")
	fs.StringVar(output, "o", "", "final filename/path")
	sha := fs.String("sha256", "", "expected sha256")
	force := fs.Bool("force", false, "overwrite existing file")
	restart := fs.Bool("restart", false, "discard saved progress")

	if err := fs.Parse(reorderArgs(args, boolFlags)); err != nil {
		return 2
	}
	if err := common.validate(); err != nil {
		return fail(err)
	}
	urls := fs.Args()
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "zdl get: missing URL")
		return 2
	}
	if len(urls) > 1 && *output != "" {
		fmt.Fprintln(os.Stderr, "zdl get: -o only works with a single URL")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rate, _ := common.rateBytes()
	code := 0
	for _, rawURL := range urls {
		headers, err := common.headersFor(rawURL)
		if err != nil {
			code = fail(err)
			continue
		}
		opts := downloader.Options{
			URL:      rawURL,
			Output:   *output,
			Confirm:  confirmHook(&common),
			Dir:      expandHome(common.dir),
			Conns:    common.conns,
			Retries:  common.retries,
			Headers:  headers,
			Insecure: common.insecure,
			Rate:     rate,
			SHA256:   *sha,
			Force:    *force,
			Restart:  *restart,
			Quiet:    common.quiet,
			Verbose:  common.verbose,
			Notify:   common.notify,
		}
		if err := downloader.Run(ctx, opts); err != nil {
			if errors.Is(err, downloader.ErrCancelled) {
				fmt.Fprintln(os.Stderr, "cancelled")
				continue
			}
			code = fail(err)
			if ctx.Err() != nil {
				return code
			}
		}
	}
	return code
}

// ---------- resume ----------

func cmdResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var common commonFlags
	common.register(fs)
	newURL := fs.String("url", "", "fresh URL for the same file")
	sha := fs.String("sha256", "", "expected sha256")
	force := fs.Bool("force", false, "accept changed validators")
	restart := fs.Bool("restart", false, "start over")

	if err := fs.Parse(reorderArgs(args, boolFlags)); err != nil {
		return 2
	}
	if err := common.validate(); err != nil {
		return fail(err)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "zdl resume: give exactly one FILE")
		return 2
	}
	out, statePath := resolveStateFile(fs.Arg(0))
	if !fileExists(statePath) {
		fmt.Fprintf(os.Stderr, "zdl resume: no saved state at %s\n", statePath)
		return 1
	}

	effectiveURL := *newURL
	if effectiveURL == "" {
		if st, lerr := downloader.LoadState(statePath); lerr == nil {
			effectiveURL = st.FinalURL
		}
	}
	headers, err := common.headersFor(effectiveURL)
	if err != nil {
		return fail(err)
	}
	rate, _ := common.rateBytes()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := downloader.Options{
		URL:      *newURL,
		Output:   out,
		Confirm:  confirmHook(&common),
		Dir:      expandHome(common.dir),
		Conns:    common.conns,
		Retries:  common.retries,
		Headers:  headers,
		Insecure: common.insecure,
		Rate:     rate,
		SHA256:   *sha,
		Force:    *force,
		Restart:  *restart,
		Quiet:    common.quiet,
		Verbose:  common.verbose,
		Notify:   common.notify,
		Resume:   true,
	}
	if err := downloader.Run(ctx, opts); err != nil {
		if errors.Is(err, downloader.ErrCancelled) {
			fmt.Fprintln(os.Stderr, "cancelled")
			return 0
		}
		return fail(err)
	}
	return 0
}

// ---------- list / info / clean ----------

func cmdList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "directory to scan")
	fs.StringVar(dir, "d", "", "directory to scan")
	if err := fs.Parse(reorderArgs(args, boolFlags)); err != nil {
		return 2
	}
	scanDir := expandHome(*dir)
	if scanDir == "" {
		scanDir = downloaderDefaultDir()
	}

	var states []*downloader.State
	_ = filepath.WalkDir(scanDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".zdl.json") {
			return nil
		}
		st, err := downloader.LoadState(path)
		if err != nil {
			return nil
		}
		states = append(states, st)
		return nil
	})
	if len(states) == 0 {
		fmt.Println("no saved downloads in", scanDir)
		return 0
	}
	fmt.Printf("%-28s %10s %8s  %-8s %s\n", "NAME", "SIZE", "DONE", "STATUS", "URL")
	for _, st := range states {
		name := filepath.Base(st.Output)
		done := st.ProgressBytes()
		pct := 0.0
		if st.Size > 0 {
			pct = float64(done) / float64(st.Size) * 100
		}
		status := "partial"
		if st.Complete() {
			status = "ready"
		}
		if st.VerifyFailed {
			status = "failed"
		}
		fmt.Printf("%-28s %10s %7.1f%%  %-8s %s\n",
			truncate(name, 28), downloader.HumanBytes(st.Size), pct, status, st.FinalURL)
	}
	return 0
}

func cmdInfo(args []string) int {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderArgs(args, boolFlags)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "zdl info: give exactly one FILE")
		return 2
	}
	_, statePath := resolveStateFile(fs.Arg(0))
	st, err := downloader.LoadState(statePath)
	if err != nil {
		return fail(err)
	}
	done := st.ProgressBytes()
	pct := 0.0
	if st.Size > 0 {
		pct = float64(done) / float64(st.Size) * 100
	}
	fmt.Printf("file:      %s\n", st.Output)
	fmt.Printf("url:       %s\n", st.FinalURL)
	fmt.Printf("size:      %s (%d bytes)\n", downloader.HumanBytes(st.Size), st.Size)
	fmt.Printf("done:      %s (%.1f%%)\n", downloader.HumanBytes(done), pct)
	if st.RangeOK {
		fmt.Printf("chunks:    %d/%d x %s\n", doneChunks(st), len(st.Done), downloader.HumanBytes(st.ChunkSize))
	} else {
		fmt.Printf("mode:      single stream\n")
	}
	fmt.Printf("range:     %v\n", st.RangeOK)
	fmt.Printf("etag:      %s\n", st.ETag)
	fmt.Printf("modified:  %s\n", st.LastModified)
	if st.SHA256 != "" {
		fmt.Printf("sha256:    %s\n", st.SHA256)
	}
	if st.VerifyFailed {
		fmt.Printf("verify:    FAILED\n")
	}
	fmt.Printf("updated:   %s\n", timeString(st.UpdatedAt))
	return 0
}

func cmdClean(args []string) int {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderArgs(args, boolFlags)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "zdl clean: give exactly one FILE")
		return 2
	}
	out, statePath := resolveStateFile(fs.Arg(0))
	part := out + ".part"
	removed := 0
	for _, p := range []string{part, statePath, statePath + ".tmp"} {
		if err := os.Remove(p); err == nil {
			removed++
			fmt.Println("removed", p)
		}
	}
	if removed == 0 {
		fmt.Println("nothing to remove for", out)
	}
	return 0
}

// ---------- helpers ----------

func fail(err error) int {
	var re *downloader.ResumableError
	if errors.As(err, &re) {
		fmt.Fprintf(os.Stderr, "zdl: %v\n", re.Err)
		extra := ""
		if re.SHA256 != "" {
			extra = " --sha256 " + re.SHA256
		}
		fmt.Fprintf(os.Stderr, "resume with:\n  zdl resume %s --url %s%s\n", shq(re.Output), shq(re.URL), extra)
		if errors.Is(re.Err, context.Canceled) {
			return 130
		}
		return 1
	}
	fmt.Fprintln(os.Stderr, "zdl:", err)
	return 1
}

// confirmHook returns nil when the prompt should be skipped, so scripts,
// quiet mode, and piped runs proceed without blocking on stdin.
func confirmHook(c *commonFlags) func(downloader.Plan) (bool, error) {
	if c.yes || c.quiet || !bothTTY() {
		return nil
	}
	return confirmPlan
}

func bothTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func confirmPlan(p downloader.Plan) (bool, error) {
	printPlan(p)
	reader := bufio.NewReader(os.Stdin)
	for i := 0; i < 3; i++ {
		fmt.Print("start download? [Y/n] ")
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			fmt.Println()
			return false, nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes", "s", "start":
			return true, nil
		case "n", "no", "c", "cancel", "q":
			return false, nil
		}
		fmt.Println("please answer y (start) or n (cancel)")
	}
	return false, nil
}

func printPlan(p downloader.Plan) {
	row := func(k, v string) { fmt.Printf("    %-10s %s\n", k, v) }
	size := "unknown"
	if p.Size >= 0 {
		size = fmt.Sprintf("%s (%d bytes)", downloader.HumanBytes(p.Size), p.Size)
	}

	fmt.Println()
	fmt.Println("  File")
	row("name", filepath.Base(p.Output))
	row("size", size)
	if p.ContentType != "" {
		row("type", p.ContentType)
	}
	row("save to", p.Output)
	if p.SHA256 != "" {
		row("sha256", strings.ToLower(p.SHA256))
	}

	fmt.Println("  Server")
	row("url", p.URL)
	if host := urlHost(p.URL); host != "" {
		row("host", host)
	}
	if p.RangeOK && p.Size > 0 {
		row("ranges", fmt.Sprintf("yes — %d chunks x %s, %d connections",
			p.Chunks, downloader.HumanBytes(p.ChunkSize), p.Conns))
	} else {
		row("ranges", "no — single stream")
	}
	if p.ETag != "" {
		row("etag", p.ETag)
	}
	if p.LastModified != "" {
		row("modified", p.LastModified)
	}

	if p.Resume {
		fmt.Println("  Resume")
		pct := 0.0
		if p.Size > 0 {
			pct = float64(p.DoneBytes) / float64(p.Size) * 100
		}
		row("already", fmt.Sprintf("%s (%.1f%%)", downloader.HumanBytes(p.DoneBytes), pct))
	}
	fmt.Println()
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func resolveStateFile(file string) (out, statePath string) {
	file = expandHome(file)
	abs, err := filepath.Abs(file)
	if err != nil {
		abs = file
	}
	if strings.HasSuffix(abs, ".zdl.json") {
		statePath = abs
		part := strings.TrimSuffix(abs, ".zdl.json")
		out = strings.TrimSuffix(part, ".part")
		return out, statePath
	}
	if strings.HasSuffix(abs, ".part") {
		out = strings.TrimSuffix(abs, ".part")
		return out, abs + ".zdl.json"
	}
	return abs, abs + ".part.zdl.json"
}

func expandHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}

func parseHumanSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	suffixes := []struct {
		suf string
		m   int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"B", 1},
	}
	for _, x := range suffixes {
		if strings.HasSuffix(s, x.suf) {
			mult = x.m
			s = strings.TrimSpace(strings.TrimSuffix(s, x.suf))
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(v * float64(mult)), nil
}

var boolFlags = map[string]bool{
	"cookies-from-zen": true,
	"insecure":         true,
	"quiet":            true,
	"q":                true,
	"verbose":          true,
	"notify":           true,
	"force":            true,
	"restart":          true,
	"yes":              true,
	"y":                true,
	"auto":             true,
	"help":             true,
	"h":                true,
}

// reorderArgs lets flags appear before or after the URL, which the standard
// flag package does not do on its own.
func reorderArgs(args []string, bools map[string]bool) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.ContainsRune(name, '=') || bools[name] {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func downloaderDefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads")
}

func doneChunks(st *downloader.State) int {
	n := 0
	for _, d := range st.Done {
		if d {
			n++
		}
	}
	return n
}

func timeString(ts int64) string {
	if ts == 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}
