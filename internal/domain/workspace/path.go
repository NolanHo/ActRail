package workspace

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Root is an absolute workspace root path.
type Root struct {
	abs string
}

func NewRoot(raw string) (Root, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return Root{}, fmt.Errorf("workspace root is required")
	}
	if !filepath.IsAbs(value) {
		return Root{}, fmt.Errorf("workspace root %q must be absolute", raw)
	}
	return Root{abs: filepath.Clean(value)}, nil
}

func (r Root) Validate() error {
	_, err := NewRoot(r.abs)
	return err
}

func (r Root) Path() string {
	return r.abs
}

func (r Root) Resolve(rel RelPath) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	if err := rel.ValidateAllowRoot(); err != nil {
		return "", err
	}
	target := r.abs
	if rel.value != "" {
		target = filepath.Join(r.abs, filepath.FromSlash(rel.value))
	}
	clean := filepath.Clean(target)
	return clean, r.ensureWithin(clean, rel.value)
}

func (r Root) Relative(abs string) (RelPath, error) {
	if err := r.Validate(); err != nil {
		return RelPath{}, err
	}
	value := strings.TrimSpace(abs)
	if value == "" {
		return RelPath{}, fmt.Errorf("absolute path is required")
	}
	clean := filepath.Clean(value)
	if err := r.ensureWithin(clean, clean); err != nil {
		return RelPath{}, err
	}
	rel, err := filepath.Rel(r.abs, clean)
	if err != nil {
		return RelPath{}, fmt.Errorf("relative path from %q to %q: %w", r.abs, clean, err)
	}
	if rel == "." {
		return RelPath{}, nil
	}
	return ParseRelPath(filepath.ToSlash(rel))
}

func (r Root) ensureWithin(target, label string) error {
	rel, err := filepath.Rel(r.abs, target)
	if err != nil {
		return fmt.Errorf("relative path from %q to %q: %w", r.abs, target, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes workspace root %q", label, r.abs)
	}
	return nil
}

// RelPath is a slash-delimited relative path inside one workspace root.
type RelPath struct {
	value string
}

func ParseRelPath(raw string) (RelPath, error) {
	return parseRelPath(raw, false)
}

func ParseRelPathAllowRoot(raw string) (RelPath, error) {
	return parseRelPath(raw, true)
}

func (p RelPath) Validate() error {
	_, err := ParseRelPath(p.value)
	return err
}

func (p RelPath) ValidateAllowRoot() error {
	_, err := ParseRelPathAllowRoot(p.value)
	return err
}

func (p RelPath) String() string {
	return p.value
}

func (p RelPath) IsRoot() bool {
	return p.value == ""
}

func (p RelPath) Name() string {
	if p.value == "" {
		return ""
	}
	return path.Base(p.value)
}

func parseRelPath(raw string, allowRoot bool) (RelPath, error) {
	value := strings.TrimSpace(raw)
	if strings.ContainsRune(value, '\x00') {
		return RelPath{}, fmt.Errorf("workspace path contains NUL")
	}
	if strings.Contains(value, "\\") {
		return RelPath{}, fmt.Errorf("workspace path %q must use forward slashes", raw)
	}
	if value == "" {
		if allowRoot {
			return RelPath{}, nil
		}
		return RelPath{}, fmt.Errorf("workspace path is required")
	}
	if strings.HasPrefix(value, "/") {
		return RelPath{}, fmt.Errorf("workspace path %q must be relative", raw)
	}
	clean := path.Clean(value)
	if clean == "." {
		if allowRoot {
			return RelPath{}, nil
		}
		return RelPath{}, fmt.Errorf("workspace path %q must identify a file or directory", raw)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return RelPath{}, fmt.Errorf("workspace path %q escapes workspace root", raw)
	}
	return RelPath{value: strings.TrimPrefix(clean, "./")}, nil
}
