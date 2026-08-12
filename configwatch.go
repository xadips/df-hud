package main

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Config hot-reload. Three details do all the work here:
//
//  1. We watch the config file's DIRECTORY, not the file. Every serious editor
//     saves by writing a temporary file and renaming it over the target
//     (vim with backupcopy=no, helix, VS Code). A watch on the inode is then
//     watching a file nobody will ever write to again, so exactly one reload
//     fires and every later save is silently missed.
//  2. Registration is synchronous and separate from the loop, so a watch that
//     cannot be established is a startup error rather than a goroutine that
//     quietly never fires. Same discipline as startBridge. It also removes the
//     race where a caller edits the file before the watch exists.
//  3. Saves arrive as bursts (CREATE, WRITE, CHMOD, RENAME), and an editor may
//     briefly leave a truncated file. A short debounce collapses the burst and
//     lets the write settle, so we never parse a half-written config.
//
// A reload that fails validation keeps the running config and logs why. The
// alternative - exiting - would mean a typo while the game is running takes the
// HUD down.
const configDebounce = 250 * time.Millisecond

// reloadFrom loads a config and carries the restart-only fields over from the
// running one, reporting which of them were ignored.
//
// One implementation shared by the file watcher and the tray menu's "Reload
// config", so an on-demand reload and an automatic one cannot come to mean
// different things.
func reloadFrom(path string, current *Config) (*Config, []string, error) {
	next, err := loadConfig(path)
	if err != nil {
		return nil, nil, err
	}
	frozen := next.reloadableFrom(current)
	if len(frozen) > 0 {
		log.Printf("config: %v need a restart to take effect; the running values are kept", frozen)
	}
	return next, frozen, nil
}

type configWatcher struct {
	w    *fsnotify.Watcher
	path string
	base string
}

// newConfigWatcher registers the watch. The directory must exist; the config
// file itself need not, so a config created after startup still takes effect.
func newConfigWatcher(path string) (*configWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(filepath.Dir(path)); err != nil {
		w.Close()
		return nil, err
	}
	return &configWatcher{w: w, path: path, base: filepath.Base(path)}, nil
}

// Run calls onReload with a validated config each time the file changes, and
// returns when ctx is done. current supplies the running config, which is what
// restart-only fields are carried over from.
func (cw *configWatcher) Run(ctx context.Context, current func() *Config, onReload func(*Config, []string)) {
	defer cw.w.Close()

	var timer *time.Timer
	fire := make(chan struct{}, 1)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-cw.w.Events:
			if !ok {
				return
			}
			// Only our file, and ignore pure-CHMOD noise: a chmod cannot
			// change the contents, and some editors emit one per save.
			if filepath.Base(event.Name) != cw.base || event.Op == fsnotify.Chmod {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(configDebounce, func() {
				select {
				case fire <- struct{}{}:
				default: // a reload is already pending; it will read the latest file
				}
			})

		case err, ok := <-cw.w.Errors:
			if !ok {
				return
			}
			log.Printf("config: watch error: %v", err)

		case <-fire:
			next, frozen, err := reloadFrom(cw.path, current())
			if err != nil {
				log.Printf("config: reload rejected, keeping the running config: %v", err)
				continue
			}
			onReload(next, frozen)
		}
	}
}
