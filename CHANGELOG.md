# Changelog

All notable changes to zdl are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **Global rate limit**: `--global-rate` caps total bandwidth across all
  concurrent dashboard downloads (per-job `--rate` still applies to `get`).
- **Bulk actions** in the dashboard: `P` pauses all active downloads,
  `R` resumes all paused/failed downloads, `D` deletes all finished
  downloads (with confirmation).
- **Filter/search**: `/` opens a filter prompt; the list is filtered live
  by name, URL, output path, or state. `Esc` clears the filter.
- **History pruning**: `history_max` (default 200) caps the number of
  finished/failed entries kept in `history.json`; active, queued, and
  paused entries are always retained.
- **Resume with new URL**: `u` on a paused/failed job opens a dialog to
  replace the URL while preserving saved `.part` progress.
- **Theme toggle**: `T` cycles the dashboard theme between `auto`,
  `dark`, and `light`. The choice is saved to `config.json`.
- The dashboard now slows its refresh to 1 Hz when nothing is transferring,
  keeping an open window close to zero CPU.

### Changed

- **Faster large transfers**: chunk state is written in compact JSON and
  throttled to once per second (it is still flushed immediately on pause,
  failure, and completion), so fast links no longer spend CPU rewriting a
  large state file for every finished chunk. Measured ~25% less total CPU on
  a 1 GiB / 64-connection transfer.
- Transfer workers share a pooled 128 KiB buffer instead of allocating one
  per chunk attempt.
- The result file is preallocated with `fallocate` on Linux before the
  transfer starts, reducing fragmentation and surfacing disk-full early.
- Resume validation fetches the head and tail checksums concurrently,
  halving that step on high-latency links.

### Fixed

- **`D` delete-all freeze**: re-fetches current job state before deleting
  finished entries, so it no longer acts on stale data or freezes.
- `-y` / `--yes` placed before another flag (e.g. `-y --rate 40M`) no
  longer swallows that flag during argument reordering.
- Resume no longer resets the displayed `done` counter to zero.
- Resume reuses the same output path (no spurious uniquified files).
- In-flight chunk bytes are saved per-chunk, so pause/resume loses nothing.

## [0.2.0] - 2026-11-07

### Added

- Full-screen TUI dashboard (`zdl` / `zdl ui`) with neon theme, animated
  progress bar, spinner, and header counters.
- Live view-only second terminals via `flock` single-writer election.
- Persistent history at `$XDG_CONFIG_HOME/zdl/history.json`; finished and
  failed entries survive restarts.
- Clipboard watcher for Zen links (no browser extension).
- Pause/resume that genuinely continues partial data, including resumed
  with a refreshed URL.
- Per-URL connection count (1–64) chosen when adding a download.
- Linux-only portable releases (amd64 + arm64, `.tar.gz` + `SHA256SUMS`).

### Fixed

- Resume no longer resets the displayed `done` counter to zero.
- Resume reuses the same output path (no spurious uniquified files).
- In-flight chunk bytes are saved per-chunk, so pause/resume loses nothing.

## [0.1.0] - 2026-11-01

### Added

- Initial release: parallel chunked downloads, resume, CLI (`get`/`resume`/
  `clip`/`list`/`info`/`clean`), confirmation prompt, progress with
  elapsed/left.
