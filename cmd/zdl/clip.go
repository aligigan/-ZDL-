package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"zdl/internal/clipboard"
	"zdl/internal/downloader"
)

func cmdClip(args []string) int {
	fs := flag.NewFlagSet("clip", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var common commonFlags
	common.register(fs)
	auto := fs.Bool("auto", false, "download detected files automatically")

	if err := fs.Parse(reorderArgs(args, boolFlags)); err != nil {
		return 2
	}
	if err := common.validate(); err != nil {
		return fail(err)
	}
	if clipboard.Tool() == "" {
		fmt.Fprintln(os.Stderr, "zdl clip: no clipboard tool found (install wl-clipboard, xclip, or xsel)")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := downloader.NewClient(common.conns, common.insecure)
	defer client.CloseIdleConnections()
	rate, _ := common.rateBytes()

	jobs := make(chan clipJob, 16)
	var wg sync.WaitGroup
	if *auto {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				opts := downloader.Options{
					URL:      j.url,
					Dir:      expandHome(common.dir),
					Conns:    common.conns,
					Retries:  common.retries,
					Headers:  j.headers,
					Insecure: common.insecure,
					Rate:     rate,
					Quiet:    common.quiet,
					Verbose:  common.verbose,
					Notify:   common.notify,
				}
				if err := downloader.Run(ctx, opts); err != nil {
					fail(err)
					if ctx.Err() != nil {
						return
					}
				}
			}
		}()
	}

	mode := "print only"
	if *auto {
		mode = "auto-download"
	}
	fmt.Printf("[clip] watching text clipboard (%s) — Ctrl+C to stop\n", mode)

	seen := map[string]time.Time{}
	textCh := make(chan string, 8)
	go clipboard.Watch(ctx, textCh)

	for {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return 130
		case text := <-textCh:
			for _, rawURL := range clipboard.ExtractURLs(text) {
				if t, ok := seen[rawURL]; ok && time.Since(t) < 10*time.Minute {
					continue
				}
				seen[rawURL] = time.Now()
				handleClipURL(ctx, client, &common, rawURL, *auto, jobs)
			}
		}
	}
}

type clipJob struct {
	url     string
	headers downloader.Headers
}

func handleClipURL(ctx context.Context, client *http.Client, common *commonFlags,
	rawURL string, auto bool, jobs chan<- clipJob) {

	headers, err := common.headersFor(rawURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[clip]", err)
		return
	}

	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pr, err := downloader.Probe(pctx, client, rawURL, headers)
	if err != nil {
		fmt.Printf("[clip] skip %s (%v)\n", rawURL, err)
		return
	}

	ct := strings.ToLower(pr.ContentType)
	isHTML := strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
	attachment := strings.Contains(strings.ToLower(pr.Disposition), "attachment")
	if isHTML && !attachment {
		fmt.Printf("[clip] ignored web page: %s\n", rawURL)
		return
	}

	size := downloader.HumanBytes(pr.Size)
	fmt.Printf("[clip] %s  %s  %s\n", pr.Filename, size, rawURL)
	if !auto {
		fmt.Printf("       zdl get %s\n", shq(rawURL))
		if common.notify {
			notify("File link detected", pr.Filename+" · "+size)
		}
		return
	}
	select {
	case jobs <- clipJob{url: rawURL, headers: headers}:
	case <-ctx.Done():
	}
}

func notify(title, body string) {
	_ = exec.Command("notify-send", "-a", "zdl", title, body).Run()
}
