package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"actrail/internal/domain/workspace"
)

const (
	defaultHistoryLimit = 20
	maxHistoryLimit     = 200
	defaultTimeout      = 3 * time.Second
	fallbackVersionID   = "workspace"
)

type Options struct {
	HistoryLimit int
	Timeout      time.Duration
	Command      string
}

type Service struct {
	historyLimit int
	timeout      time.Duration
	command      string
}

func New(opts Options) (*Service, error) {
	historyLimit := defaultHistoryLimit
	if opts.HistoryLimit < 0 {
		return nil, fmt.Errorf("git history limit must be at least 1")
	}
	if opts.HistoryLimit > 0 {
		historyLimit = opts.HistoryLimit
	}
	if historyLimit < 1 {
		return nil, fmt.Errorf("git history limit must be at least 1")
	}
	if historyLimit > maxHistoryLimit {
		historyLimit = maxHistoryLimit
	}
	timeout := defaultTimeout
	if opts.Timeout < 0 {
		return nil, fmt.Errorf("git timeout must be non-negative")
	}
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		command = "git"
	}
	return &Service{historyLimit: historyLimit, timeout: timeout, command: command}, nil
}

func (s Service) FileVersions(ctx context.Context, root workspace.Root, rel workspace.RelPath) (workspace.FileVersionList, error) {
	if err := ctx.Err(); err != nil {
		return workspace.FileVersionList{}, err
	}
	abs, err := root.Resolve(rel)
	if err != nil {
		return workspace.FileVersionList{}, err
	}
	repoRoot, err := s.repoRoot(ctx, root.Path())
	if err != nil {
		return s.fallback(rel, err.Error()), nil
	}
	repoRel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return workspace.FileVersionList{}, fmt.Errorf("path relative to repo root: %w", err)
	}
	repoRel = filepath.ToSlash(repoRel)
	if repoRel == ".." || strings.HasPrefix(repoRel, "../") {
		return s.fallback(rel, "workspace path is outside git repository"), nil
	}
	out, err := s.runGit(ctx, repoRoot,
		"log",
		"--follow",
		"--max-count", strconv.Itoa(s.historyLimit),
		"--format=%H%x00%ct%x00%an%x00%s%x1e",
		"--",
		repoRel,
	)
	if err != nil {
		return s.fallback(rel, err.Error()), nil
	}
	items, err := parseLog(out)
	if err != nil {
		return workspace.FileVersionList{}, err
	}
	if len(items) == 0 {
		return s.fallback(rel, "git history unavailable"), nil
	}
	items[0].Current = true
	return workspace.FileVersionList{Path: rel, Items: items}, nil
}

func (s Service) fallback(rel workspace.RelPath, reason string) workspace.FileVersionList {
	return workspace.FileVersionList{
		Path:           rel,
		FallbackReason: reason,
		Items: []workspace.FileVersion{{
			VersionID: fallbackVersionID,
			Label:     "Workspace",
			Current:   true,
		}},
	}
}

func (s Service) repoRoot(ctx context.Context, dir string) (string, error) {
	out, err := s.runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("git repository unavailable: %w", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("git repository unavailable: empty root")
	}
	return filepath.Clean(root), nil
}

func (s Service) runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	runCtx := ctx
	cancel := func() {}
	if s.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, s.timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.command, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("git command timed out after %s", s.timeout)
	}
	if errors.Is(runCtx.Err(), context.Canceled) {
		return nil, runCtx.Err()
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return nil, fmt.Errorf("git command unavailable")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr == "" {
			stderr = strings.TrimSpace(string(bytes.TrimSpace(out)))
		}
		if stderr == "" {
			stderr = exitErr.Error()
		}
		return nil, fmt.Errorf("git command failed: %s", stderr)
	}
	return nil, err
}

func parseLog(out []byte) ([]workspace.FileVersion, error) {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return nil, nil
	}
	records := strings.Split(text, "\x1e")
	items := make([]workspace.FileVersion, 0, len(records))
	for _, record := range records {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x00")
		if len(fields) != 4 {
			return nil, fmt.Errorf("git log record %q malformed", record)
		}
		ts, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("git commit timestamp %q invalid: %w", fields[1], err)
		}
		hash := fields[0]
		label := shortHash(hash)
		items = append(items, workspace.FileVersion{
			VersionID:  hash,
			Label:      label,
			CommitHash: hash,
			Author:     fields[2],
			CommitAt:   time.Unix(ts, 0).UTC(),
			Message:    fields[3],
		})
	}
	return items, nil
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
