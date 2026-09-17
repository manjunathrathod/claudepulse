package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce is how long the watcher waits after the last filesystem event
// before kicking a scan; Claude Code appends many lines in quick succession.
const debounce = 500 * time.Millisecond

// Kick requests a scan as soon as the current one (if any) finishes. Safe to
// call from any goroutine; extra kicks while one is pending are coalesced.
func (ix *Indexer) Kick() {
	select {
	case ix.kick <- struct{}{}:
	default:
	}
}

// startWatcher creates the fsnotify watcher. It never fails the indexer: if
// watching is unavailable the periodic ticker still keeps data fresh.
func (ix *Indexer) startWatcher() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		ix.log.Warn("file watching unavailable; relying on periodic rescans", "err", err)
		return
	}
	ix.watcher = w
}

// watch drains fsnotify events until ctx is done, kicking a debounced scan.
// It does not close the watcher; Run does, after this returns.
func (ix *Indexer) watch(ctx context.Context) {
	w := ix.watcher
	if w == nil {
		return
	}
	ix.watching.Store(true)
	defer ix.watching.Store(false)

	var timer *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ix.ignoreEvent(ev) {
				continue
			}
			// A new directory (project, session, subagents) needs its own watch.
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					ix.addWatchTree(ev.Name)
				}
			}
			if timer == nil {
				timer = time.NewTimer(debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
			}
			fire = timer.C
		case <-fire:
			fire = nil
			timer = nil
			ix.Kick()
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				ix.log.Debug("watch overflow; forcing rescan")
				ix.Kick()
				continue
			}
			ix.log.Warn("watch error", "err", err)
		}
	}
}

// ignoreEvent filters noise: SQLite/editor temp files and, above all, the
// secrets we never read (their existence is not our business either).
func (ix *Indexer) ignoreEvent(ev fsnotify.Event) bool {
	if ev.Op == fsnotify.Chmod || ev.Op == 0 {
		return true
	}
	if ix.dir.IsDenied(ev.Name) {
		return true
	}
	base := filepath.Base(ev.Name)
	return strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, "~") || strings.HasPrefix(base, ".#")
}

// syncWatches (re)registers every directory that can produce interesting
// events. Called after each scan so directories created while no watch was
// in place are still picked up.
func (ix *Indexer) syncWatches() {
	if ix.watcher == nil {
		return
	}
	root := ix.dir.Root
	ix.addWatch(root) // settings.json, history.jsonl, stats-cache.json, .last-*
	ix.addWatch(ix.dir.SessionsDir())
	ix.addWatch(ix.dir.PlansDir())
	ix.addWatch(ix.dir.ProjectsDir())
	projects, err := ix.dir.ListProjects()
	if err != nil {
		return
	}
	for _, p := range projects {
		ix.addWatch(p.Path)
		for _, tp := range p.Transcripts {
			sub := filepath.Join(p.Path, strings.TrimSuffix(filepath.Base(tp), ".jsonl"), "subagents")
			if info, err := os.Stat(sub); err == nil && info.IsDir() {
				ix.addWatch(sub)
			}
		}
	}
}

// addWatchTree watches dir and every directory beneath it (bounded depth:
// projects/<p>/<session>/subagents is the deepest structure we care about).
func (ix *Indexer) addWatchTree(dir string) {
	ix.addWatch(dir)
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == dir {
			return nil
		}
		if rel, _ := filepath.Rel(dir, p); strings.Count(filepath.ToSlash(rel), "/") > 2 {
			return filepath.SkipDir
		}
		ix.addWatch(p)
		return nil
	})
}

func (ix *Indexer) addWatch(dir string) {
	if ix.watcher == nil || dir == "" {
		return
	}
	if err := ix.watcher.Add(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		ix.log.Debug("watch add failed", "dir", dir, "err", err)
	}
}
