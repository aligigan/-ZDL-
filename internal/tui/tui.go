package tui

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"zdl/internal/clipboard"
	"zdl/internal/config"
	"zdl/internal/downloader"
)

type Mode int

const (
	ModeList Mode = iota
	ModeAdd
	ModeProbe
	ModeDelete
	ModeEditURL
	ModeFilter
	ModeHelp
)

// HeadersFunc resolves per-URL headers (Zen cookies, referer, ...).
type HeadersFunc func(rawURL string) (downloader.Headers, error)

// Defaults carries the per-job settings shared by every TUI download.
type Defaults struct {
	Insecure    bool
	Retries     int
	HistoryPath string
	Restored    int
	ReadOnly    bool
}

type Model struct {
	engine   *downloader.Engine
	cfg      *config.Config
	headers  HeadersFunc
	defaults Defaults

	jobs   []downloader.JobStatus
	cursor int
	offset int

	width, height int
	mode          Mode
	status        string
	statusErr     bool

	input      textinput.Model
	connsInput textinput.Model
	addFocus   int // 0 = URL field, 1 = connections field
	editURL    string

	probing    bool
	probeSeq   int
	probe      *downloader.Plan
	probeURL   string
	probeTitle string
	probeHdr   downloader.Headers
	probeDisp  string
	probeConns int

	clipboardOn bool
	clipCh      chan string
	clipCancel  context.CancelFunc
	clipSeen    map[string]time.Time

	historyPath string
	lastSave    time.Time
	lastLoad    time.Time
	readOnly    bool
	themeMode   string

	filterText  string
	filterInput textinput.Model
	bulkConfirm bool

	tick     int
	quitting bool
}

func New(engine *downloader.Engine, cfg *config.Config, headers HeadersFunc, defaults Defaults) *Model {
	in := textinput.New()
	in.Placeholder = "https://example.com/file.iso"
	in.CharLimit = 4096
	in.Width = 60

	cIn := textinput.New()
	cIn.Placeholder = "16"
	cIn.CharLimit = 4
	cIn.Width = 10

	fIn := textinput.New()
	fIn.Placeholder = "filter by name or URL…"
	fIn.CharLimit = 256
	fIn.Width = 60

	conns := cfg.Conns
	if conns < 1 {
		conns = 16
	}
	if conns > 64 {
		conns = 64
	}

	m := &Model{
		engine:      engine,
		cfg:         cfg,
		headers:     headers,
		defaults:    defaults,
		input:       in,
		connsInput:  cIn,
		filterInput: fIn,
		addFocus:    0,
		probeConns:  conns,
		themeMode:   cfg.ThemeMode(),
		clipSeen:    make(map[string]time.Time),
		historyPath: defaults.HistoryPath,
		readOnly:    defaults.ReadOnly,
		width:       100,
		height:      30,
	}
	if defaults.ReadOnly {
		m.status = "another zdl is running — live view only"
	} else if defaults.Restored > 0 {
		m.status = fmt.Sprintf("restored %d %s", defaults.Restored, plural(defaults.Restored))
	}
	if cfg.ClipboardAuto && !defaults.ReadOnly {
		m.toggleClipboard()
	}
	SetThemeMode(m.themeMode)
	return m
}

type tickMsg struct{}

type probeMsg struct {
	plan    downloader.Plan
	name    string
	url     string
	disp    string
	headers downloader.Headers
	auto    bool
	seq     int
}

type probeErrMsg struct {
	url  string
	err  error
	auto bool
	seq  int
}

type statusMsg string

func tickCmd() tea.Cmd { return tickCmdIn(200 * time.Millisecond) }

func tickCmdIn(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// tickInterval keeps the dashboard snappy while transfers run, but drops to a
// slow poll when idle so leaving a dashboard open costs almost no CPU.
func (m *Model) tickInterval(all []downloader.JobStatus) time.Duration {
	d := time.Second
	if m.readOnly {
		d = 800 * time.Millisecond
	}
	if m.clipboardOn {
		d = 500 * time.Millisecond
	}
	if m.probing {
		return 200 * time.Millisecond
	}
	for _, j := range all {
		if j.State == downloader.JobActive || j.State == downloader.JobQueued {
			return 200 * time.Millisecond
		}
	}
	return d
}

func (m *Model) Init() tea.Cmd { return tickCmd() }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = min(60, max(20, m.width-12))
		return m, nil

	case tickMsg:
		m.tick++
		all := m.engine.Snapshot()
		if m.filterText != "" {
			m.jobs = filterJobs(all, m.filterText)
		} else {
			m.jobs = all
		}
		if m.readOnly {
			m.pollHistory()
		} else {
			m.saveHistory(false)
		}
		m.clampCursor()
		var cmds []tea.Cmd
		cmds = append(cmds, m.drainClipboard()...)
		cmds = append(cmds, tickCmdIn(m.tickInterval(all)))
		return m, tea.Batch(cmds...)

	case probeMsg:
		if msg.seq != 0 && msg.seq != m.probeSeq {
			return m, nil
		}
		m.probing = false
		if msg.auto {
			if isWebPage(msg.plan.ContentType, msg.disp) {
				m.status = "clipboard: ignored web page " + msg.url
				return m, nil
			}
			m.addJob(msg.url, msg.headers)
			m.status = "clipboard: queued " + msg.name
			return m, nil
		}
		m.mode = ModeProbe
		p := msg.plan
		m.probe = &p
		m.probeURL = msg.url
		m.probeTitle = msg.name
		m.probeHdr = msg.headers
		m.probeDisp = msg.disp
		return m, nil

	case probeErrMsg:
		if msg.seq != 0 && msg.seq != m.probeSeq {
			return m, nil
		}
		m.probing = false
		if msg.auto {
			m.status = "clipboard: " + msg.err.Error()
			return m, nil
		}
		m.mode = ModeAdd
		m.status = msg.err.Error()
		m.statusErr = true
		m.input.Focus()
		m.connsInput.Blur()
		m.addFocus = 0
		return m, nil

	case statusMsg:
		m.status = string(msg)
		m.statusErr = false
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m.quit()
	}
	if m.bulkConfirm {
		switch k.String() {
		case "y":
			m.bulkConfirm = false
			m.status = ""
			// Re-fetch jobs so we delete what's actually done right now.
			curr := m.engine.Snapshot()
			var removed []string
			for _, j := range curr {
				if j.State == downloader.JobDone {
					removed = append(removed, j.Title)
					m.engine.Remove(j.ID, true)
				}
			}
			m.jobs = m.engine.Snapshot()
			m.clampCursor()
			m.saveHistory(true)
			if len(removed) > 0 {
				m.status = fmt.Sprintf("deleted %d %s", len(removed), plural(len(removed)))
			} else {
				m.status = "nothing finished to delete"
			}
			return m, nil
		case "n", "esc":
			m.bulkConfirm = false
			m.status = ""
			return m, nil
		}
		return m, nil
	}
	if m.mode == ModeFilter {
		return m.keyFilter(k)
	}
	if m.readOnly && m.mode == ModeList {
		switch k.String() {
		case "a", "p", "r", "u", "d", "P", "R", "D", "/", "c", "t":
			m.status = "view only — another zdl owns the list"
			m.statusErr = true
			return m, nil
		}
	}
	switch m.mode {
	case ModeAdd:
		return m.keyAdd(k)
	case ModeProbe:
		return m.keyProbe(k)
	case ModeDelete:
		return m.keyDelete(k)
	case ModeEditURL:
		return m.keyEditURL(k)
	case ModeHelp:
		m.mode = ModeList
		return m, nil
	}

	switch k.String() {
	case "q":
		return m.quit()
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.jobs)-1 {
			m.cursor++
		}
	case "g":
		m.cursor = 0
	case "G":
		if len(m.jobs) > 0 {
			m.cursor = len(m.jobs) - 1
		}
	case "a":
		m.mode = ModeAdd
		m.probing = false
		m.status = ""
		m.statusErr = false
		m.input.SetValue("")
		m.connsInput.SetValue("")
		m.addFocus = 0
		m.input.Focus()
		m.connsInput.Blur()
		return m, textinput.Blink
	case "p":
		if j := m.selected(); j != nil {
			m.engine.Pause(j.ID)
			m.saveHistory(true)
			m.status = "paused " + j.Title
		}
	case "r":
		if j := m.selected(); j != nil {
			m.engine.ResumeWith(j.ID, m.headersFor(j.URL))
			m.saveHistory(true)
			m.status = "resuming " + j.Title
		}
	case "u":
		if j := m.selected(); j != nil && (j.State == downloader.JobPaused || j.State == downloader.JobFailed) {
			m.editURL = j.ID
			m.input.SetValue(j.URL)
			m.input.Focus()
			m.mode = ModeEditURL
			m.status = "edit URL · enter resume · esc cancel"
		}
	case "d":
		if j := m.selected(); j != nil {
			m.mode = ModeDelete
		}
	case "o":
		path := m.cfg.Dir
		if j := m.selected(); j != nil && j.Output != "" {
			path = filepath.Dir(j.Output)
		}
		m.status = "opening " + path
		return m, openPathCmd(path)
	case "c":
		m.toggleClipboard()
	case "t":
		m.themeMode = nextTheme(m.themeMode)
		m.cfg.Theme = m.themeMode
		SetThemeMode(m.themeMode)
		if err := m.cfg.Save(); err != nil {
			m.status = "theme: " + err.Error()
			m.statusErr = true
		} else {
			m.status = "theme: " + m.themeMode
		}
	case "?", "h":
		m.mode = ModeHelp
	case "/":
		if m.mode == ModeList {
			m.mode = ModeFilter
			m.filterInput.SetValue(m.filterText)
			m.filterInput.Focus()
			return m, textinput.Blink
		}
	case "P":
		if m.mode == ModeList {
			n := 0
			for _, j := range m.jobs {
				if j.State == downloader.JobActive {
					m.engine.Pause(j.ID)
					n++
				}
			}
			if n > 0 {
				m.saveHistory(true)
				m.status = fmt.Sprintf("paused %d %s", n, plural(n))
			}
		}
	case "R":
		if m.mode == ModeList {
			n := 0
			for _, j := range m.jobs {
				if j.State == downloader.JobPaused || j.State == downloader.JobFailed {
					m.engine.ResumeWith(j.ID, m.headersFor(j.URL))
					n++
				}
			}
			if n > 0 {
				m.saveHistory(true)
				m.status = fmt.Sprintf("resuming %d %s", n, plural(n))
			}
		}
	case "D":
		if m.mode == ModeList {
			// Re-fetch current jobs so we act on fresh state, not a stale snapshot.
			curr := m.engine.Snapshot()
			done := 0
			for _, j := range curr {
				if j.State == downloader.JobDone {
					done++
				}
			}
			if done > 0 {
				m.bulkConfirm = true
				m.status = fmt.Sprintf("delete all %d finished? y/n", done)
			} else {
				m.status = "nothing finished to delete"
			}
		}
	}
	m.clampCursor()
	return m, nil
}

func (m *Model) keyAdd(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = ModeList
		m.input.Blur()
		m.connsInput.Blur()
		m.probing = false
		m.probeSeq++ // ignore any probe still in flight
		m.status = ""
		return m, nil
	case "tab", "shift+tab":
		if m.addFocus == 0 {
			m.addFocus = 1
			m.input.Blur()
			m.connsInput.Focus()
		} else {
			m.addFocus = 0
			m.connsInput.Blur()
			m.input.Focus()
		}
		return m, nil
	case "enter":
		url := strings.TrimSpace(m.input.Value())
		if url == "" {
			m.status = "enter a URL first"
			m.statusErr = true
			return m, nil
		}
		conns := m.cfg.Conns
		if v, err := strconv.Atoi(strings.TrimSpace(m.connsInput.Value())); err == nil && v >= 1 && v <= 64 {
			conns = v
		}
		m.probeConns = conns
		m.probing = true
		m.status = ""
		m.statusErr = false
		return m, m.probeCmd(url, false)
	}
	var cmd tea.Cmd
	if m.addFocus == 0 {
		m.input, cmd = m.input.Update(k)
	} else {
		m.connsInput, cmd = m.connsInput.Update(k)
	}
	return m, cmd
}

func (m *Model) keyProbe(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "n":
		m.mode = ModeList
		m.probe = nil
		m.status = "cancelled"
		return m, nil
	case "enter", "y":
		url := m.probeURL
		hdr := m.probeHdr
		name := m.probeTitle
		m.mode = ModeList
		m.probe = nil
		m.addJob(url, hdr)
		m.status = "queued " + name
		return m, nil
	}
	return m, nil
}

func (m *Model) keyDelete(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "y":
		if j := m.selected(); j != nil {
			title := j.Title
			m.engine.Remove(j.ID, true)
			m.jobs = m.engine.Snapshot()
			m.clampCursor()
			m.saveHistory(true)
			m.status = "deleted " + title
		}
		m.mode = ModeList
		return m, nil
	case "n", "esc":
		m.mode = ModeList
		return m, nil
	}
	return m, nil
}

func (m *Model) keyEditURL(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = ModeList
		m.input.Blur()
		m.editURL = ""
		m.status = ""
		return m, nil
	case "enter":
		url := strings.TrimSpace(m.input.Value())
		id := m.editURL
		m.mode = ModeList
		m.input.Blur()
		m.editURL = ""
		j := m.selected()
		if id == "" || (j != nil && j.ID != id) {
			m.status = "resume cancelled"
			return m, nil
		}
		if err := m.engine.ResumeWithURL(id, url); err != nil {
			m.status = err.Error()
			m.statusErr = true
			return m, nil
		}
		m.saveHistory(true)
		m.status = "resuming with new URL"
		return m, nil
	}
	m.input, _ = m.input.Update(k)
	return m, nil
}

func (m *Model) keyFilter(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = ModeList
		m.filterInput.Blur()
		m.filterText = ""
		m.status = ""
		return m, nil
	case "enter":
		m.mode = ModeList
		m.filterInput.Blur()
		m.filterText = m.filterInput.Value()
		m.status = ""
		return m, nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(k)
	m.filterText = m.filterInput.Value()
	return m, cmd
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	if m.clipCancel != nil {
		m.clipCancel()
	}
	if !m.readOnly {
		m.engine.Close()
		m.jobs = m.engine.Snapshot()
		m.saveHistory(true)
	}
	return m, tea.Quit
}

func (m *Model) addJob(rawURL string, hdr downloader.Headers) {
	m.engine.Add(downloader.Job{
		URL:      rawURL,
		Dir:      m.cfg.Dir,
		Conns:    m.probeConns,
		Retries:  m.defaults.Retries,
		Rate:     m.cfg.RateBytes(),
		Headers:  hdr,
		Insecure: m.defaults.Insecure,
	})
	m.probeConns = m.cfg.Conns
	if m.probeConns < 1 {
		m.probeConns = 16
	}
	if m.probeConns > 64 {
		m.probeConns = 64
	}
	m.saveHistory(true)
}

func (m *Model) headersFor(rawURL string) downloader.Headers {
	if m.headers == nil {
		return downloader.Headers{}
	}
	hdr, err := m.headers(rawURL)
	if err != nil {
		return downloader.Headers{}
	}
	return hdr
}

// saveHistory writes the job list for the next session. Progress writes are
// throttled; state changes pass force.
func (m *Model) saveHistory(force bool) {
	if m.readOnly || m.historyPath == "" {
		return
	}
	if !force && time.Since(m.lastSave) < time.Second {
		return
	}
	if err := downloader.SaveHistory(m.historyPath, m.engine.Records(), m.cfg.HistoryMax); err != nil {
		m.status = "history: " + err.Error()
		m.statusErr = true
	}
	m.lastSave = time.Now()
}

// pollHistory refreshes a read-only dashboard from the owning process.
func (m *Model) pollHistory() {
	if m.historyPath == "" || time.Since(m.lastLoad) < 800*time.Millisecond {
		return
	}
	m.lastLoad = time.Now()
	records, err := downloader.LoadHistory(m.historyPath)
	if err != nil {
		m.status = "history: " + err.Error()
		m.statusErr = true
		return
	}
	m.jobs = downloader.HistoryStatuses(records)
}

func (m *Model) totalSpeed() float64 {
	if m.readOnly {
		var total float64
		for _, j := range m.jobs {
			total += j.Speed
		}
		return total
	}
	return m.engine.TotalSpeed()
}

func (m *Model) selected() *downloader.JobStatus {
	if m.cursor < 0 || m.cursor >= len(m.jobs) {
		return nil
	}
	return &m.jobs[m.cursor]
}

func (m *Model) clampCursor() {
	if len(m.jobs) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	if m.cursor >= len(m.jobs) {
		m.cursor = len(m.jobs) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *Model) probeCmd(rawURL string, auto bool) tea.Cmd {
	headers := m.headers
	conns := m.cfg.Conns
	if !auto {
		conns = m.probeConns
	}
	insecure := m.defaults.Insecure
	seq := 0
	if !auto {
		m.probeSeq++
		seq = m.probeSeq
	}
	return func() tea.Msg {
		hdr := downloader.Headers{}
		if headers != nil {
			if h, err := headers(rawURL); err == nil {
				hdr = h
			}
		}
		client := downloader.NewClient(conns, insecure)
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pr, err := downloader.Probe(ctx, client, rawURL, hdr)
		if err != nil {
			return probeErrMsg{url: rawURL, err: err, auto: auto, seq: seq}
		}
		plan := downloader.Plan{
			URL:          pr.FinalURL,
			Size:         pr.Size,
			RangeOK:      pr.RangeOK,
			ETag:         pr.ETag,
			LastModified: pr.LastModified,
			ContentType:  pr.ContentType,
			Conns:        conns,
		}
		return probeMsg{
			plan:    plan,
			name:    pr.Filename,
			url:     pr.FinalURL,
			disp:    pr.Disposition,
			headers: hdr,
			auto:    auto,
			seq:     seq,
		}
	}
}

func (m *Model) toggleClipboard() {
	if m.clipboardOn {
		if m.clipCancel != nil {
			m.clipCancel()
		}
		m.clipboardOn = false
		m.clipCh = nil
		m.status = "clipboard watcher off"
		return
	}
	if clipboard.Tool() == "" {
		m.status = "no clipboard tool found"
		m.statusErr = true
		return
	}
	ch := make(chan string, 16)
	ctx, cancel := context.WithCancel(context.Background())
	m.clipCh = ch
	m.clipCancel = cancel
	m.clipboardOn = true
	go clipboard.Watch(ctx, ch)
	m.status = "clipboard watcher on"
}

func (m *Model) drainClipboard() []tea.Cmd {
	if !m.clipboardOn || m.clipCh == nil {
		return nil
	}
	var cmds []tea.Cmd
	for {
		select {
		case text := <-m.clipCh:
			for _, u := range clipboard.ExtractURLs(text) {
				if t, ok := m.clipSeen[u]; ok && time.Since(t) < 10*time.Minute {
					continue
				}
				m.clipSeen[u] = time.Now()
				cmds = append(cmds, m.probeCmd(u, true))
			}
		default:
			return cmds
		}
	}
}

func isWebPage(contentType, disposition string) bool {
	ct := strings.ToLower(contentType)
	html := strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
	attachment := strings.Contains(strings.ToLower(disposition), "attachment")
	return html && !attachment
}

func openPathCmd(path string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", path)
		case "windows":
			cmd = exec.Command("explorer", path)
		default:
			cmd = exec.Command("xdg-open", path)
		}
		if err := cmd.Start(); err != nil {
			return statusMsg("open failed: " + err.Error())
		}
		return statusMsg("opened " + path)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func filterJobs(jobs []downloader.JobStatus, text string) []downloader.JobStatus {
	lower := strings.ToLower(text)
	out := make([]downloader.JobStatus, 0, len(jobs))
	for _, j := range jobs {
		if strings.Contains(strings.ToLower(j.Title), lower) ||
			strings.Contains(strings.ToLower(j.URL), lower) ||
			strings.Contains(strings.ToLower(j.Output), lower) ||
			strings.Contains(strings.ToLower(j.State.String()), lower) {
			out = append(out, j)
		}
	}
	return out
}

// nextTheme cycles through auto -> dark -> light -> auto.
func nextTheme(mode string) string {
	switch mode {
	case "auto":
		return "dark"
	case "dark":
		return "light"
	default:
		return "auto"
	}
}
