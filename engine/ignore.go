package engine

import (
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
)

type Ignore struct {
	Dir          []string `toml:"dir"               yaml:"dir"`
	File         []string `toml:"file"              yaml:"file"`
	WatchedExten []string `toml:"watched_extension" yaml:"watched_extension"`
	IgnoreGit    bool     `toml:"git"               yaml:"git"`

	// gitPatterns holds globs read from the root .gitignore when IgnoreGit is
	// set. Populated by the engine at startup; not user-configured.
	gitPatterns []string
}

// UnmarshalYAML accepts both Refresh's current `git` key and the historical
// `git_ignore` key emitted by Wails project templates.
func (i *Ignore) UnmarshalYAML(unmarshal func(any) error) error {
	var decoded struct {
		Dir          []string `yaml:"dir"`
		File         []string `yaml:"file"`
		WatchedExten []string `yaml:"watched_extension"`
		Git          *bool    `yaml:"git"`
		GitIgnore    *bool    `yaml:"git_ignore"`
	}
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	i.Dir = decoded.Dir
	i.File = decoded.File
	i.WatchedExten = decoded.WatchedExten
	i.IgnoreGit = false
	if decoded.Git != nil {
		i.IgnoreGit = *decoded.Git
	}
	if decoded.GitIgnore != nil {
		i.IgnoreGit = *decoded.GitIgnore
	}
	return nil
}

// shouldIgnore reports whether a change to path should be skipped. A path is
// considered only if it matches a watched extension; it is then ignored if it
// sits in an ignored directory or matches an ignore-file or .gitignore pattern.
func (i *Ignore) shouldIgnore(path string) bool {
	if !i.isWatchedExtension(path) {
		return true
	}
	return isIgnoreDir(path, i.Dir) ||
		patternMatch(path, i.Dir) ||
		patternMatch(path, i.File) ||
		patternMatch(path, i.gitPatterns)
}

func (i *Ignore) isWatchedExtension(path string) bool {
	// An empty filter means watch nothing: it is the off-switch users reach for
	// by leaving watched_extension blank/commented out. Watch-all is opt-in via
	// an explicit "*" entry (matched through patternMatch below), which the
	// bare-CLI and default-config cases set so they still reload.
	if len(i.WatchedExten) == 0 {
		return false
	}

	// "*" is the explicit watch-all token; match every path, including those
	// without an extension.
	if slices.Contains(i.WatchedExten, "*") {
		return true
	}

	ext := filepath.Ext(path)
	if ext == "" {
		return false
	}

	// First check for direct extension matches (e.g., ".go")
	for _, watchedExt := range i.WatchedExten {
		if watchedExt == ext || watchedExt == "*"+ext {
			return true
		}
	}

	// Then try pattern matching for more complex patterns
	return patternMatch(path, i.WatchedExten)
}

// isIgnoreDir reports whether any path component exactly matches an ignore rule.
func isIgnoreDir(path string, rules []string) bool {
	for dir := range strings.SplitSeq(path, string(filepath.Separator)) {
		if slices.Contains(rules, dir) {
			slog.Debug("ignoring directory", "dir", dir)
			return true
		}
	}
	return false
}
