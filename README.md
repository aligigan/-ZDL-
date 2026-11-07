# zdl

Terminal, IDM-style downloader for files you grab from Zen Browser. Up to 64
parallel connections, resumable transfers (even when the link has expired and
you have a fresh one), a full-screen dashboard, and a clipboard watcher.

Written in Go. Ships as one self-contained Linux binary (amd64 and arm64);
the only optional external tools are a clipboard reader and `sqlite3` for Zen
cookies.

## Install (Arch Linux / AUR)

```sh
yay -S zdl-bin        # or: paru -S zdl-bin
```

The `zdl-bin` package installs the prebuilt static binary, bash/zsh
completions, and documentation. Update it with your normal `yay -Syu` /
`paru -Syu`.

## Build

Requires Go 1.27+ and `make` (or run the `go build` command below). The
dependencies are vendored, so a fresh checkout builds offline.

```sh
make build                          # ./zdl
make install                        # ~/.local/bin/zdl
make release                        # dist/ archives + SHA256SUMS
```

`make release` cross-compiles with `CGO_ENABLED=0` and `-trimpath` for
linux/amd64 and linux/arm64, then writes `.tar.gz` archives plus
`dist/SHA256SUMS`. Pass a version explicitly if needed:
`make release VERSION=0.2.0`.

Without make:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=0.2.0" \
  -o ~/.local/bin/zdl ./cmd/zdl
```

## Dashboard

Run `zdl` with no arguments in a terminal, or `zdl ui`, to open the dashboard:

```sh
zdl                                  # open the dashboard
zdl ui -d ~/Downloads -x 32 --rate 8M
zdl ui --cookies-from-zen
```

The list shows every download with state, downloaded/total size, percent,
progress bar, speed, and time left. The selected job's details (URL,
destination, server, connections, elapsed, ETA) sit below the list. Active rows
use a spinner and an animated progress bar.

The list is persistent: it is stored in `~/.config/zdl/history.json`
(`$XDG_CONFIG_HOME/zdl/history.json` if set) and reloaded by every new
dashboard, so finished and failed downloads stay visible across sessions.
Anything that was still running or queued when a session ended comes back as
paused, ready to resume.

Open the dashboard in a second terminal while one is already running and it
attaches as a **live view**: it shows the same list, refreshing about once a
second, but only the terminal that owns the list can add, pause, resume, or
delete. Live-view keys are limited to movement, help, and quit; closing it
leaves the owning terminal running. Quit the owner (or start in another
terminal after it exits) to get an editable dashboard again.

| Key | Action |
| --- | --- |
| `a` | add a URL; choose connections (1-64), then probe and start/cancel |
| `up` / `down`, `j` / `k` | move the selection |
| `g` / `G` | first / last download |
| `p` / `r` | pause / resume the selected download |
| `P` / `R` | pause / resume all downloads |
| `u` | resume the selected paused/failed download with a new (refreshed) URL |
| `d`, then `y` | delete the download, final file, `.part`, and saved state |
| `D`, then `y` | delete all finished downloads |
| `/` | filter the list by name, URL, path, or state |
| `t` | cycle theme: dark / light / auto |
| `o` | open the download folder |
| `c` | toggle the clipboard watcher |
| `?` | help |
| `q` | quit (running downloads are saved for resume) |

Adding a URL runs the same probe/validation as the CLI and shows filename,
size, type, server, destination, range support, ETag, and Last-Modified before
any bytes are written. Pausing keeps the `.part` and state; resuming continues
from the saved byte offsets (including partially filled chunks), so the
downloaded amount and progress bar carry on instead of restarting. Quitting
saves running downloads too, so a later `zdl get` (or `resume`) continues them.

### Rate limits

`--rate` caps each download individually. `zdl ui --global-rate 8M` caps the
combined bandwidth of all active dashboard downloads instead.

### History

Finished and failed downloads are pruned once the history reaches
`history_max` (default 200) entries; active, queued, and paused entries are
always retained. Set `history_max` in `~/.config/zdl/config.json`, or delete
`history.json` to clear the list entirely.

### Theme

The dashboard theme can be cycled with `t` between `auto` (follows terminal),
`dark`, and `light`. The choice is saved to `~/.config/zdl/config.json` under
the `theme` key.

### Performance

Transfers use pooled 128 KiB buffers and, on Linux, preallocate the result file
with `fallocate` before the first byte arrives. That keeps parallel range writes
contiguous and reports a full disk before a long transfer starts. Saved chunk
state is compact and flushed at most once a second, but always immediately on
pause, failure, and completion, so resume stays exact without burning CPU on
fast links. The dashboard refreshes at 5 Hz while jobs are running and drops to
1 Hz when idle.

Downloads run concurrently (default at most 3 jobs at a time). The clipboard
watcher can auto-queue file links copied from Zen; plain web pages are
ignored, and links are deduplicated for 10 minutes. The dashboard needs an
interactive terminal of at least 40x15; below that it shows a resize message.

### Config

Settings are read from `~/.config/zdl/config.json`
(`$XDG_CONFIG_HOME/zdl/config.json` if set).

```json
{
  "dir": "/home/you/Downloads",
  "connections": 16,
  "rate": "8M",
  "global_rate": "20M",
  "max_concurrent": 3,
  "history_max": 200,
  "theme": "auto",
  "cookies_from_zen": false,
  "clipboard_auto": false
}
```

A missing file means defaults: `~/Downloads`, 16 connections, unlimited rate,
3 concurrent jobs, 200 history entries, `auto` theme, clipboard watcher off.
Flags such as `-d`, `-x`, `--rate`, and `--cookies-from-zen` override the file
for that run. `rate` accepts `8M`, `500K`, and similar; leave it empty for
unlimited.

The dashboard history lives next to it as `history.json`. Delete that file to
clear the list; `d` in the dashboard removes one entry and its files.

## Download

```sh
zdl get https://example.com/big.iso
zdl get https://example.com/big.iso -x 32          # 32 connections (1-64)
zdl get URL -o ~/Downloads/name.iso               # explicit path
zdl get URL -d ~/Downloads                        # directory only
zdl get URL --sha256 <hex>                        # verify before finalizing
zdl get URL --rate 8M                             # global speed cap
zdl get URL1 URL2 URL3                            # one after another
zdl get URL -y                                    # no start/cancel prompt
```

Defaults: 16 connections, five retries per chunk, download directory
`~/Downloads`. Flags may come before or after URLs.

In a terminal, zdl probes the URL first and prints a short summary — filename,
size, content type, destination, host, range support/chunking, ETag,
Last-Modified, and any existing progress for a resume — then asks
`start download? [Y/n]`. Answer `n` (or `c`) to cancel without touching the
disk. Use `-y/--yes` to skip the prompt. It is skipped automatically when
stdin/stdout is not a terminal, when `--quiet` is set, and for `zdl clip
--auto`, so scripts never hang.

Data is written to `<output>.part` while `HTTP Range` requests fill a sparse
file at their own offsets. State lives in `<output>.part.zdl.json`. On success
the part file is fsynced and renamed to the final name, and the state is
removed.

Progress is a two-line block rewritten in place, wget-style, and truncated to
the terminal width so it never wraps:

```
file.bin 12.0 MiB/64.0 MiB  18.8% [===       ] 8.0 MiB/s c:16
elapsed 1m02s   left 6m30s
```

Fields drop out on narrow terminals. When the download succeeds the block is
erased and replaced by one summary line with total time and average speed:

```
done: /home/ali/Downloads/file.bin (64.0 MiB in 8.0s, 8.0 MiB/s)
```

When stdout is not a terminal, the live block is skipped and the final
summary is printed instead (unless `--quiet` is set).

Without range support the download falls back to a single stream and still
resumes with `Range: bytes=N-`.

## Resume

Interrupting with Ctrl+C leaves everything in place and prints the exact
command to continue:

```sh
zdl resume ~/Downloads/big.iso
zdl resume ~/Downloads/big.iso --url 'https://example.com/big.iso?token=NEW'
```

`resume` accepts the final path, the `.part` file, or the `.zdl.json` state
file. If you omit `--url` the last known URL is reused. The dashboard uses the
same saved state when you resume after quitting.

Before continuing, zdl checks the remote file against the saved state:

1. size must match;
2. stored ETag / Last-Modified must match;
3. the first and last 64 KiB are re-hashed and compared to what was saved.

A refreshed URL for the same bytes passes. A changed file is refused. Use
`--restart` to discard progress and download again, or `--force` to accept
changed validators anyway (the result can be a mix of two versions).

Ranged requests send `If-Range`, so a server that swapped the file mid-flight
answers `200 OK` instead of a partial body. That is treated as fatal rather
than silently stitching mismatched bytes.

Other commands:

```sh
zdl list -d ~/Downloads     # resumable downloads (partial / ready / failed)
zdl info big.iso            # size, progress, chunks, validators
zdl clean big.iso           # delete the .part and its state
```

## Zen Browser cookies

```sh
zdl get URL --cookies-from-zen
zdl get URL --cookies-from-zen --zen-profile '/home/you/.config/zen/xxxx.Default (release)'
```

zdl finds the profile with the most recently modified `cookies.sqlite` and
copies it with `sqlite3 .backup` into a temp dir, so a running browser is not a
problem. Only cookies matching the URL's host (including `.domain` cookies)
are sent, and `Secure` cookies only over HTTPS.

Automatic discovery looks under `~/.config/zen`; pass `--zen-profile` or set
`ZDL_ZEN_PROFILE` to use a different profile directory. `sqlite3` must be on
`PATH`.

`--cookie 'session=...'` and `--header 'Cookie: ...'` work too. Set
`"cookies_from_zen": true` in the config file to enable it for the dashboard.

## Clipboard watcher

```sh
zdl clip            # print detected file links + a ready-to-run zdl command
zdl clip --auto     # download each detected file into ~/Downloads
```

It polls the system clipboard for text changes every 700 ms using `wl-paste`,
`xclip`, or `xsel`. Install one of those tools; the dashboard reports when the
feature is unavailable. Plain web pages (`text/html` without
`Content-Disposition: attachment`) are ignored. URLs are deduplicated for 10
minutes. `--auto` downloads sequentially in one worker; stop with Ctrl+C.

In the dashboard, press `c` to toggle the watcher, or set
`"clipboard_auto": true` to start it with the UI.

Run it in a terminal, a systemd user unit, or from a Hyprland keybind.

## Releases and portability

`make release` produces self-contained archives:

```
zdl_0.2.0_linux_amd64.tar.gz
zdl_0.2.0_linux_arm64.tar.gz
SHA256SUMS
```

Every binary is self-contained: CGO is disabled, so no external runtime
libraries are needed and Linux builds are fully static. Put `zdl` on `PATH`
and run it. Verify archives with `sha256sum -c SHA256SUMS`. Dependencies are
vendored, so `make build` and `make release` also work offline. The archives
include the bash and zsh completions.

## Notes and limits

- Direct `http://` / `https://` files only. HLS/DASH and site-specific
  extractors are out of scope; use `yt-dlp` for those.
- HTTP/2 is disabled on purpose: over h2, ranged requests would multiplex onto
  one TCP connection and defeat the point of `-x`.
- Files larger than 2 GiB are supported (64-bit offsets throughout).
- The dashboard needs an interactive terminal. `zdl get` and the other
  commands remain scriptable and never require a TTY.
- Test server: `go run ./testserver 127.0.0.1:8099` provides ranged,
  no-range, expiring-token, cookie-auth, flaky, and content-change endpoints.

## License

Copyright (C) 2026 Ali Gigan.

zdl is free software: you can redistribute it and/or modify it under the
terms of the GNU General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version.

zdl is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR
A PARTICULAR PURPOSE. See the GNU General Public License for more details.

You should have received a copy of the GNU General Public License along with
zdl. If not, see <https://www.gnu.org/licenses/>.
