package downloader

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// Snapshot is one progress sample handed to a UI or other observer.
type Snapshot struct {
	Done    int64
	Total   int64
	Speed   float64 // bytes per second
	Elapsed time.Duration
	Left    time.Duration // -1 when unknown
	Conns   int
}

// Progress renders a two-line live block (TTY) and/or reports snapshots.
//
//	file.bin 12.0 MiB/64.0 MiB  18.8% [===       ] 8.0 MiB/s c:16
//	elapsed 1m02s   left 6m30s
//
// Both lines are kept shorter than the terminal so they never wrap.
type Progress struct {
	label  string
	total  int64
	done   *atomic.Int64
	conns  int
	quiet  bool
	tty    bool
	width  int
	notify func(Snapshot)

	mu       sync.Mutex
	start    time.Time
	lastT    time.Time
	lastB    int64
	speed    float64
	success  bool
	drawn    bool
	finished chan struct{}
}

func NewProgress(label string, total int64, done *atomic.Int64, conns int, quiet bool, notify func(Snapshot)) *Progress {
	tty := term.IsTerminal(int(os.Stdout.Fd()))
	width := 0
	if tty {
		width = terminalWidth()
	}
	return &Progress{
		label:    label,
		total:    total,
		done:     done,
		conns:    conns,
		quiet:    quiet,
		tty:      tty,
		width:    width,
		notify:   notify,
		start:    time.Now(),
		lastT:    time.Now(),
		finished: make(chan struct{}),
	}
}

func (p *Progress) Done() <-chan struct{} { return p.finished }

// MarkSuccess makes the final render erase the block instead of leaving it.
func (p *Progress) MarkSuccess() {
	p.mu.Lock()
	p.success = true
	p.mu.Unlock()
}

func (p *Progress) Run(ctx context.Context) {
	defer close(p.finished)
	render := p.tty && !p.quiet
	if !render && p.notify == nil {
		<-ctx.Done()
		return
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if render {
				p.render(true)
			}
			p.emit()
			return
		case now := <-tick.C:
			p.update(now)
			if render {
				p.render(false)
			}
			p.emit()
		}
	}
}

func (p *Progress) emit() {
	if p.notify == nil {
		return
	}
	cur := p.done.Load()
	if p.total > 0 && cur > p.total {
		cur = p.total
	}
	s := Snapshot{
		Done:    cur,
		Total:   p.total,
		Speed:   p.speed,
		Elapsed: time.Since(p.start),
		Left:    -1,
		Conns:   p.conns,
	}
	if p.total > 0 {
		switch {
		case cur >= p.total:
			s.Left = 0
		case p.speed > 1:
			s.Left = time.Duration(float64(p.total-cur) / p.speed * float64(time.Second))
		}
	}
	p.notify(s)
}

func (p *Progress) update(now time.Time) {
	cur := p.done.Load()
	dt := now.Sub(p.lastT).Seconds()
	if dt <= 0 {
		return
	}
	inst := float64(cur-p.lastB) / dt
	if p.speed == 0 {
		p.speed = inst
	} else {
		p.speed = 0.35*inst + 0.65*p.speed
	}
	p.lastT = now
	p.lastB = cur
}

func (p *Progress) render(final bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.tty {
		return
	}
	if final && p.success {
		p.eraseLocked()
		return
	}
	// Re-read the width so resizing the terminal mid-download is handled.
	if w := terminalWidth(); w > 0 {
		p.width = w
	}

	cur := p.done.Load()
	if cur > p.total && p.total > 0 {
		cur = p.total
	}
	main := p.formatLine(cur)
	sub := p.formatTimeLine(cur)

	if p.drawn {
		// Cursor sits at the end of the time line: clear it, then go up.
		fmt.Print("\r\x1b[2K\x1b[1A")
	} else {
		fmt.Print("\r\x1b[2K")
	}
	fmt.Printf("%s\n", main)
	fmt.Printf("\r\x1b[2K%s", sub)
	p.drawn = true

	if final {
		fmt.Print("\n")
		p.drawn = false
	}
}

// eraseLocked clears both lines and leaves the cursor on the first one.
func (p *Progress) eraseLocked() {
	if !p.drawn {
		return
	}
	fmt.Print("\r\x1b[2K\x1b[1A\r\x1b[2K")
	p.drawn = false
}

func (p *Progress) avail() int {
	width := p.width
	if width <= 0 {
		width = 100
	}
	avail := width - 1
	if avail < 20 {
		avail = 20
	}
	return avail
}

func (p *Progress) formatLine(cur int64) string {
	avail := p.avail()

	if p.total <= 0 {
		base := fmt.Sprintf("%s %s %s c:%d", p.label, HumanBytes(cur), HumanSpeed(p.speed), p.conns)
		return truncateRunes(base, avail)
	}

	frac := float64(cur) / float64(p.total)
	if frac > 1 {
		frac = 1
	}
	sizes := HumanBytes(cur) + "/" + HumanBytes(p.total)

	for _, labelMax := range []int{26, 18, 12, 8} {
		label := truncateRunes(p.label, labelMax)
		for _, bar := range []bool{true, false} {
			var b strings.Builder
			fmt.Fprintf(&b, "%s %s %5.1f%%", label, sizes, frac*100)
			if bar {
				bw := 20
				if avail < 70 {
					bw = 10
				}
				filled := int(frac * float64(bw))
				fmt.Fprintf(&b, " [%s%s]", strings.Repeat("=", filled), strings.Repeat(" ", bw-filled))
			}
			fmt.Fprintf(&b, " %s c:%d", HumanSpeed(p.speed), p.conns)
			line := b.String()
			if len([]rune(line)) <= avail {
				return line
			}
		}
	}
	fallback := fmt.Sprintf("%s %s %5.1f%% c:%d", truncateRunes(p.label, 8), sizes, frac*100, p.conns)
	return truncateRunes(fallback, avail)
}

// formatTimeLine is the second line: wall-clock spent and estimated remaining.
func (p *Progress) formatTimeLine(cur int64) string {
	left := "--"
	if p.total > 0 && cur >= p.total {
		left = "0s"
	} else if p.total > 0 && p.speed > 1 {
		left = HumanETA(time.Duration(float64(p.total-cur) / p.speed * float64(time.Second)))
	}
	line := fmt.Sprintf("elapsed %s   left %s", HumanETA(time.Since(p.start)), left)
	return truncateRunes(line, p.avail())
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// terminalWidth reports the terminal's column count, 0 if unknown.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 0
}
