package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"zdl/internal/downloader"
)

func (m *Model) View() string {
	if m.width < 40 || m.height < 15 {
		msg := "terminal too small\n" +
			fmt.Sprintf("%dx%d — need 40x15", m.width, m.height)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, stDim.Render(msg))
	}
	switch m.mode {
	case ModeAdd:
		return m.dialog(m.addView())
	case ModeProbe:
		return m.dialog(m.probeView())
	case ModeDelete:
		return m.dialog(m.deleteView())
	case ModeEditURL:
		return m.dialog(m.editURLView())
	case ModeFilter:
		return m.dialog(m.filterView())
	case ModeHelp:
		return m.dialog(m.helpView())
	}
	body := m.listView() + "\n" + m.detailsView()
	return m.headerView() + "\n" + body + "\n" + m.footerView()
}

// dialog centers a box on the alt screen without the dashboard header/footer.
func (m *Model) dialog(box string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// plural returns "download" or "downloads" for n.
func plural(n int) string {
	if n == 1 {
		return "download"
	}
	return "downloads"
}

func (m *Model) headerView() string {
	var active, done, paused, failed int
	for _, j := range m.jobs {
		switch j.State {
		case downloader.JobActive:
			active++
		case downloader.JobDone:
			done++
		case downloader.JobPaused:
			paused++
		case downloader.JobFailed:
			failed++
		}
	}

	parts := []string{stDim.Render(fmt.Sprintf("%d %s", len(m.jobs), plural(len(m.jobs))))}
	if m.readOnly {
		parts = append(parts, stWarn.Render("view only"))
	}
	if active > 0 {
		parts = append(parts,
			stActive.Render(spinner(m.tick)+" "+fmt.Sprintf("%d active", active)),
			stSpeed.Render(downloader.HumanSpeed(m.totalSpeed())))
	} else if paused > 0 {
		parts = append(parts, stWarn.Render(fmt.Sprintf("%d paused", paused)))
	}
	if done > 0 {
		parts = append(parts, stOK.Render(fmt.Sprintf("%d done", done)))
	}
	if failed > 0 {
		parts = append(parts, stError.Render(fmt.Sprintf("%d failed", failed)))
	}
	if active == 0 && paused == 0 && len(m.jobs) > 0 {
		parts = append(parts, stDim.Render("idle"))
	}
	if m.filterText != "" {
		parts = append(parts, stWarn.Render("filter: "+m.filterText))
	}

	line := stLogo.Render(" ⬇ zdl") + "  " + strings.Join(parts, stFaint.Render(" · "))
	line = ansi.Truncate(line, m.width, "…")
	rule := stRule.Render(strings.Repeat("─", max(0, m.width-1)))
	return line + "\n" + rule
}

func (m *Model) footerView() string {
	hints := [][2]string{
		{"a", "add"}, {"p/r", "pause/resume"}, {"P/R", "pause/resume all"},
		{"u", "new URL"}, {"D", "delete finished"}, {"/", "filter"},
		{"t", "theme"}, {"o", "folder"}, {"c", "clipboard"},
		{"?", "help"}, {"q", "quit"},
	}
	if m.readOnly {
		hints = [][2]string{{"?", "help"}, {"q", "quit"}}
	}
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, stKey.Render(h[0])+" "+stDim.Render(h[1]))
	}
	keys := " " + strings.Join(parts, stFaint.Render("  ·  "))

	var status string
	if m.status != "" {
		style := stDim
		if m.statusErr {
			style = stError
		}
		status = style.Render(truncate(" "+m.status, m.width))
	} else {
		status = " "
	}
	return status + "\n" + ansi.Truncate(keys, m.width, "…")
}

func (m *Model) listHeight() int {
	h := m.height - 12
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) listView() string {
	h := m.listHeight()
	lines := make([]string, 0, h)
	if len(m.jobs) == 0 {
		lines = append(lines, "",
			stDim.Render(truncate("   ⬇  nothing queued yet", max(10, m.width-2))),
			stFaint.Render(truncate("      press a to add a URL", max(10, m.width-2))))
	} else {
		for i := m.offset; i < len(m.jobs) && len(lines) < h; i++ {
			lines = append(lines, m.rowView(i))
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) rowView(i int) string {
	j := m.jobs[i]
	w := max(20, m.width-2)
	selected := i == m.cursor

	nameW := w - 61
	if nameW < 8 {
		nameW = 8
	}
	size := "—"
	if j.Total > 0 {
		size = fmt.Sprintf("%9s/%9s", downloader.HumanBytes(j.Done), downloader.HumanBytes(j.Total))
	} else {
		size = fmt.Sprintf("%19s", size)
	}
	pct := fmt.Sprintf("%3.0f%%", j.Percent()*100)
	bar := progressBar(j.Percent(), 12, m.tick, j.State == downloader.JobActive)
	speed := ""
	eta := ""
	if j.State == downloader.JobActive {
		speed = downloader.HumanSpeed(j.Speed)
		eta = downloader.HumanETA(j.Left)
	} else if j.State == downloader.JobDone {
		speed = "done"
	}
	mark := "  "
	if selected {
		mark = "▌ "
	}
	icon := stateIcon(j.State)
	if j.State == downloader.JobActive {
		icon = spinner(m.tick)
	}
	line := fmt.Sprintf("%s%s %-*s %s %4s %s %9s %6s",
		mark, icon, nameW, truncate(j.Title, nameW), size, pct, bar, speed, eta)
	line = truncate(line, w)
	if selected {
		head := stateStyle(j.State).Background(colSelBg).Render(mark + icon)
		return head + stSel.Width(w-lipgloss.Width(mark+icon)).Render(line[len(mark)+len(icon):])
	}
	return stateStyle(j.State).Render(icon) + truncate(line[len(mark)+len(icon):], w-2)
}

func (m *Model) detailsView() string {
	w := max(30, m.width-2)
	lines := []string{" " + strings.Repeat("─", w-1)}
	j := m.selected()
	if j == nil {
		lines = append(lines, stDim.Render(" no selection"))
		for len(lines) < 7 {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	row := func(k, v string) string {
		return stDim.Render(fmt.Sprintf(" %-7s", k)) + truncate(v, w-10)
	}

	lines = append(lines, row("URL", j.URL))
	lines = append(lines, row("Save", j.Output))
	server := hostOf(j.URL)
	if server == "" {
		server = "—"
	}
	lines = append(lines, row("Server", fmt.Sprintf("%s · %d connections", server, j.Conns)))
	if j.Total > 0 {
		lines = append(lines, row("Done", fmt.Sprintf("%s of %s · %.0f%%",
			downloader.HumanBytes(j.Done), downloader.HumanBytes(j.Total), j.Percent()*100)))
	}

	switch j.State {
	case downloader.JobActive:
		if w < 58 {
			lines = append(lines, row("State", fmt.Sprintf("active · %s · %s/%s",
				downloader.HumanSpeed(j.Speed), downloader.HumanETA(j.Elapsed), downloader.HumanETA(j.Left))))
		} else {
			lines = append(lines, row("State", fmt.Sprintf("active · %s · elapsed %s · left %s",
				downloader.HumanSpeed(j.Speed), downloader.HumanETA(j.Elapsed), downloader.HumanETA(j.Left))))
		}
	case downloader.JobDone:
		lines = append(lines, row("State", fmt.Sprintf("done · %s · %s",
			downloader.HumanBytes(j.Total), downloader.HumanETA(j.Elapsed))))
	case downloader.JobFailed:
		lines = append(lines, stDim.Render(fmt.Sprintf(" %-7s", "State"))+stError.Render(truncate("failed", w-10)))
		if j.Err != nil {
			lines = append(lines, stDim.Render(fmt.Sprintf(" %-7s", "Error"))+stError.Render(truncate(j.Err.Error(), w-10)))
		}
	case downloader.JobPaused:
		lines = append(lines, row("State", fmt.Sprintf("paused · %s of %s",
			downloader.HumanBytes(j.Done), downloader.HumanBytes(j.Total))))
	default:
		lines = append(lines, row("State", "queued · waiting for a slot"))
	}
	for len(lines) < 7 {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) filterView() string {
	boxW := min(76, m.width-4)
	inner := boxW - 2
	var b strings.Builder
	b.WriteString(stTitle.Render("Filter downloads") + "\n\n")
	b.WriteString(stDim.Render("Filter") + "\n")
	b.WriteString(m.filterInput.View() + "\n\n")
	if m.filterText != "" {
		b.WriteString(stDim.Render(fmt.Sprintf("matching: %s", truncate(m.filterText, inner))) + "\n\n")
	}
	b.WriteString(stDim.Render("enter apply · esc cancel"))
	return stBox.Width(boxW).Render(b.String())
}

func (m *Model) editURLView() string {
	boxW := min(76, m.width-4)
	inner := boxW - 2
	var b strings.Builder
	b.WriteString(stTitle.Render("Resume with new URL") + "\n\n")
	b.WriteString(stDim.Render("URL") + "\n")
	b.WriteString(m.input.View() + "\n\n")
	if m.status != "" {
		style := stDim
		if m.statusErr {
			style = stError
		}
		b.WriteString(style.Render(truncate(m.status, inner)) + "\n\n")
	}
	b.WriteString(stDim.Render("enter resume · esc cancel"))
	return stBox.Width(boxW).Render(b.String())
}

func (m *Model) addView() string {
	boxW := min(76, m.width-4)
	inner := boxW - 2 // lipgloss Width includes the horizontal padding
	var b strings.Builder
	b.WriteString(stTitle.Render("Add download") + "\n\n")
	b.WriteString(stDim.Render("URL") + "\n")
	b.WriteString(m.input.View() + "\n\n")
	b.WriteString(stDim.Render("Connections (1-64)") + "\n")
	b.WriteString(m.connsInput.View() + "\n\n")
	b.WriteString(stDim.Render(truncate("Save to "+m.cfg.Dir, inner)) + "\n")
	b.WriteString(stDim.Render(truncate(fmt.Sprintf("rate %s", rateLabel(m.cfg.Rate)), inner)) + "\n\n")
	if m.probing {
		b.WriteString(stWarn.Render("probing"+strings.Repeat(".", m.tick%4+1)) + "\n")
	} else if m.status != "" {
		style := stDim
		if m.statusErr {
			style = stError
		}
		maxLines := 3
		if m.height <= 15 {
			maxLines = 2
		}
		for _, line := range wrapWords(m.status, inner, maxLines) {
			b.WriteString(style.Render(truncate(line, inner)) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(stDim.Render("tab switch field · enter probe · esc cancel"))
	return stBox.Width(boxW).Render(b.String())
}

func (m *Model) probeView() string {
	p := m.probe
	if p == nil {
		return stDim.Render(" no probe data")
	}
	boxW := min(84, m.width-4)
	row := func(k, v string) string {
		return stDim.Render(fmt.Sprintf("%-9s", k)) + truncate(v, boxW-12)
	}
	size := "unknown"
	if p.Size >= 0 {
		size = fmt.Sprintf("%s (%d bytes)", downloader.HumanBytes(p.Size), p.Size)
	}
	save := p.Output
	if save == "" {
		save = filepath.Join(m.cfg.Dir, m.probeTitle)
	}

	var b strings.Builder
	b.WriteString(stTitle.Render("Ready to download") + "\n\n")
	b.WriteString(row("File", m.probeTitle) + "\n")
	b.WriteString(row("Size", size) + "\n")
	if p.ContentType != "" {
		b.WriteString(row("Type", p.ContentType) + "\n")
	}
	server := hostOf(p.URL)
	if server == "" {
		server = "—"
	}
	b.WriteString(row("Server", server) + "\n")
	b.WriteString(row("Save to", save) + "\n")
	b.WriteString(row("URL", p.URL) + "\n")
	ranges := "no — single stream"
	if p.RangeOK {
		ranges = fmt.Sprintf("yes — %d connections", p.Conns)
	}
	b.WriteString(row("Ranges", ranges) + "\n")
	if p.ETag != "" {
		b.WriteString(row("ETag", p.ETag) + "\n")
	}
	if p.LastModified != "" {
		b.WriteString(row("Modified", p.LastModified) + "\n")
	}
	if m.height > 15 {
		b.WriteString("\n")
	}
	b.WriteString(stDim.Render("enter start · esc cancel"))
	return stBox.Width(boxW).Render(b.String())
}

func (m *Model) deleteView() string {
	boxW := min(70, m.width-4)
	name := "this download"
	if j := m.selected(); j != nil {
		name = j.Title
	}
	body := stTitle.Render("Delete download?") + "\n\n" +
		truncate(name, boxW-4) + "\n" +
		stDim.Render("The file, .part, and saved state will be removed.") + "\n\n" +
		stDim.Render("y delete · n cancel")
	return stBox.Width(boxW).Render(body)
}

func (m *Model) helpView() string {
	rows := [][2]string{
		{"a", "add a URL"},
		{"p / r", "pause / resume"},
		{"P / R", "pause / resume all"},
		{"u", "resume with new URL"},
		{"d", "delete (removes files)"},
		{"D", "delete all finished"},
		{"/", "filter list"},
		{"t", "theme (dark/light/auto)"},
		{"o", "open download folder"},
		{"c", "clipboard watcher"},
		{"g / G", "first / last"},
		{"?", "this help"},
		{"q", "quit (saves list)"},
	}
	note := ""
	if m.readOnly {
		rows = [][2]string{
			{"g / G", "first / last"},
			{"?", "this help"},
			{"q", "quit"},
		}
		note = "\n" + stWarn.Render("another zdl owns this list; showing it live.")
	}
	boxW := min(78, m.width-4)
	var b strings.Builder
	b.WriteString(stTitle.Render("Keys") + "\n\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %-8s %s\n", r[0], r[1]))
	}
	if note != "" {
		b.WriteString(note + "\n")
	}
	b.WriteString("\n" + stDim.Render("press any key to close"))
	return stBox.Width(boxW).Render(b.String())
}

func stateIcon(s downloader.JobState) string {
	switch s {
	case downloader.JobActive:
		return "▼"
	case downloader.JobDone:
		return "✓"
	case downloader.JobPaused:
		return "‖"
	case downloader.JobFailed:
		return "✗"
	default:
		return "·"
	}
}

func stateStyle(s downloader.JobState) lipgloss.Style {
	switch s {
	case downloader.JobActive:
		return stActive
	case downloader.JobDone:
		return stOK
	case downloader.JobPaused:
		return stWarn
	case downloader.JobFailed:
		return stError
	default:
		return stDim
	}
}

func progressBar(frac float64, width, frame int, active bool) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	// Eighth-block resolution for a smoother, more alive bar.
	partial := []rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉'}
	units := int(frac * float64(width) * 8)
	if units > width*8 {
		units = width * 8
	}
	full, rem := units/8, units%8
	cells := make([]rune, 0, width)
	for i := 0; i < full; i++ {
		cells = append(cells, '█')
	}
	if full < width && rem > 0 {
		cells = append(cells, partial[rem-1])
	}
	for len(cells) < width {
		cells = append(cells, '░')
	}
	if active {
		start := full
		if full < width && rem > 0 {
			start = full + 1
		}
		if span := width - start; span > 0 {
			cells[start+frame%span] = '▒'
		}
	}
	return "[" + string(cells) + "]"
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}

// wrapWords splits s into at most max lines of roughly width runes. A single
// overlong word is left intact; the caller truncates it to fit.
func wrapWords(s string, width, max int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	for _, word := range strings.Fields(s) {
		if len(lines) == 0 {
			lines = append(lines, word)
			continue
		}
		last := len(lines) - 1
		if len([]rune(lines[last]))+1+len([]rune(word)) <= width {
			lines[last] += " " + word
		} else {
			lines = append(lines, word)
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	if len(lines) > max {
		lines = append(lines[:max-1], truncate(strings.Join(lines[max-1:], " "), width))
	}
	return lines
}

func rateLabel(rate string) string {
	if strings.TrimSpace(rate) == "" {
		return "unlimited"
	}
	return rate
}

func hostOf(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return ""
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}
