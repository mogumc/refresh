package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/atterpac/refresh/process"
)

// newWatchTestEngine builds a minimal engine whose watcher can run against a
// real temp directory, without starting any processes.
func newWatchTestEngine(t *testing.T, root string, debounceMS int) *Engine {
	t.Helper()
	e := &Engine{Config: Config{
		RootPath: root,
		Debounce: debounceMS,
		Ignore:   Ignore{WatchedExten: []string{"*.txt"}},
	}}
	e.ProcessManager = process.NewProcessManager()
	if err := e.ProcessManager.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}
	return e
}

// userDevModeConfig is the exact dev_mode block a user reported (issue: runaway
// hot reload on alpha2.105). watched_extension is commented out entirely, which
// must mean "watch nothing" — leaving it blank is how users disable reloads.
// Mapped under refresh's `config:` key (wails extracts the dev_mode subtree).
const userDevModeConfig = `
config:
  root_path: .
  log_level: warn
  debounce: 1000
  ignore:
    dir:
      - .git
      - node_modules
      - frontend
      - bin
    file:
      - .DS_Store
      - .gitignore
      - .gitkeep
      - "*_test.go"
    watched_extension:
    git: true
`

// TestWatcherUserConfigWatchesNothing loads the user's exact config and asserts
// that, with watched_extension blank, editing a .go file triggers no reloads.
func TestWatcherUserConfigWatchesNothing(t *testing.T) {
	e := &Engine{}
	if err := e.StringtoConfigYAML(userDevModeConfig); err != nil {
		t.Fatalf("StringtoConfigYAML: %v", err)
	}
	if len(e.Config.Ignore.WatchedExten) != 0 {
		t.Fatalf("WatchedExten = %v, want empty", e.Config.Ignore.WatchedExten)
	}

	// Point the watcher at a temp dir instead of the parsed "." root.
	root := t.TempDir()
	e.Config.RootPath = root
	e.Config.Debounce = 100
	e.ProcessManager = process.NewProcessManager()
	if err := e.ProcessManager.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}

	reload := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.startWatcher(ctx, reload); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	cancel()

	if got := len(reload); got != 0 {
		t.Errorf("blank watched_extension produced %d reloads for a .go edit, want 0", got)
	}
}

func TestWatcherCoalescesBurstIntoSingleReload(t *testing.T) {
	root := t.TempDir()
	e := newWatchTestEngine(t, root, 200)

	reload := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.startWatcher(ctx, reload); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	// A burst of writes well within the debounce window should collapse to one
	// reload fired after the quiet interval.
	file := filepath.Join(root, "a.txt")
	for i := range 5 {
		if err := os.WriteFile(file, []byte(strconv.Itoa(i)), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Wait comfortably past the debounce for the trailing-edge fire.
	time.Sleep(500 * time.Millisecond)
	cancel()

	if got := len(reload); got != 1 {
		t.Errorf("expected exactly 1 coalesced reload, got %d", got)
	}
}

func TestWatcherDetectsNestedSubdirectoryChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := newWatchTestEngine(t, root, 150)

	reload := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.startWatcher(ctx, reload); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	// The recursive watch must catch writes in nested directories.
	if err := os.WriteFile(filepath.Join(root, "app", "main.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	cancel()

	if got := len(reload); got != 1 {
		t.Errorf("nested change produced %d reloads, want 1", got)
	}
}

// TestWatcherEmptyFilterWatchesNothing guards the off-switch: an empty
// WatchedExten (blank/commented-out watched_extension in a loaded config) must
// watch nothing, so leaving the filter empty disables reloads. A regression
// here previously flipped empty to watch-all and caused runaway reloads.
func TestWatcherEmptyFilterWatchesNothing(t *testing.T) {
	root := t.TempDir()
	e := &Engine{Config: Config{
		RootPath: root,
		Debounce: 100,
		Ignore:   Ignore{}, // zero value: empty filter, watch nothing
	}}
	e.ProcessManager = process.NewProcessManager()
	if err := e.ProcessManager.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}

	reload := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.startWatcher(ctx, reload); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	cancel()

	if got := len(reload); got != 0 {
		t.Errorf("empty filter produced %d reloads, want 0", got)
	}
}

// TestWatcherStarWatchesAnyFile guards the explicit watch-all token: with
// WatchedExten ["*"] (DefaultEngineConfig and the bare CLI default), every
// change must trigger a reload.
func TestWatcherStarWatchesAnyFile(t *testing.T) {
	root := t.TempDir()
	e := &Engine{Config: Config{
		RootPath: root,
		Debounce: 100,
		Ignore:   Ignore{WatchedExten: []string{"*"}},
	}}
	e.ProcessManager = process.NewProcessManager()
	if err := e.ProcessManager.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}

	reload := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.startWatcher(ctx, reload); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	cancel()

	if got := len(reload); got != 1 {
		t.Errorf("star filter produced %d reloads for a .go edit, want 1", got)
	}
}

func TestWatcherIgnoresUnwatchedExtensions(t *testing.T) {
	root := t.TempDir()
	e := newWatchTestEngine(t, root, 100)

	reload := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.startWatcher(ctx, reload); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	// Only *.txt is watched; a *.log write must not trigger a reload.
	if err := os.WriteFile(filepath.Join(root, "ignore.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	cancel()

	if got := len(reload); got != 0 {
		t.Errorf("unwatched extension triggered %d reloads, want 0", got)
	}
}
