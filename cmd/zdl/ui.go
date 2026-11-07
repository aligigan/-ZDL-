package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"zdl/internal/config"
	"zdl/internal/downloader"
	"zdl/internal/tui"
)

// cmdUI runs the full-screen dashboard.
func cmdUI(args []string) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var common commonFlags
	common.register(fs)
	common.quietWarn = true

	var globalRate string
	fs.StringVar(&globalRate, "global-rate", "", "global speed cap across all jobs, e.g. 8M")

	if err := fs.Parse(reorderArgs(args, boolFlags)); err != nil {
		return 2
	}
	if err := common.validate(); err != nil {
		return fail(err)
	}
	if !bothTTY() {
		fmt.Fprintln(os.Stderr, "zdl ui: an interactive terminal is required")
		return 2
	}

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "zdl: config:", cfgErr)
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["d"] || set["dir"] {
		cfg.Dir = expandHome(common.dir)
	}
	if set["x"] || set["connections"] {
		cfg.Conns = common.conns
	}
	if set["rate"] {
		cfg.Rate = common.rate
	}
	if globalRate != "" {
		cfg.GlobalRate = globalRate
	}
	if common.cookiesFromZen {
		cfg.CookiesFromZen = true
	}
	cfg.Normalize()
	// The config file can also enable Zen cookies; headersFor reads this field.
	common.cookiesFromZen = cfg.CookiesFromZen

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	historyPath, pathErr := config.HistoryPath()
	if pathErr != nil {
		fmt.Fprintln(os.Stderr, "zdl: history:", pathErr)
	}
	releaseLock, owner := acquireHistoryLock(historyPath)
	defer releaseLock()
	readOnly := !owner

	engine := downloader.NewEngine(ctx, cfg.MaxConcurrent)
	engine.SetGlobalRate(cfg.GlobalRateBytes())
	defer engine.Close()

	restored := 0
	if !readOnly && historyPath != "" {
		records, err := downloader.LoadHistory(historyPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "zdl: history:", err)
		} else {
			engine.Restore(records)
			restored = len(records)
		}
	}

	headers := func(rawURL string) (downloader.Headers, error) {
		return common.headersFor(rawURL)
	}
	model := tui.New(engine, cfg, headers, tui.Defaults{
		Insecure:    common.insecure,
		Retries:     common.retries,
		HistoryPath: historyPath,
		Restored:    restored,
		ReadOnly:    readOnly,
	})
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		return fail(err)
	}
	return 0
}
