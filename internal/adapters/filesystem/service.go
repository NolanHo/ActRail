package filesystem

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"actrail/internal/domain/workspace"
)

const (
	defaultListLimit = 200
	maxListLimit     = 1000
	defaultTextBytes = 1 << 20
	sniffBytes       = 512
)

type Options struct {
	ListLimit    int
	MaxTextBytes int64
}

type Service struct {
	listLimit    int
	maxTextBytes int64
}

func New(opts Options) (*Service, error) {
	listLimit := defaultListLimit
	if opts.ListLimit < 0 {
		return nil, fmt.Errorf("filesystem list limit must be at least 1")
	}
	if opts.ListLimit > 0 {
		listLimit = opts.ListLimit
	}
	if listLimit < 1 {
		return nil, fmt.Errorf("filesystem list limit must be at least 1")
	}
	if listLimit > maxListLimit {
		listLimit = maxListLimit
	}
	maxTextBytes := int64(defaultTextBytes)
	if opts.MaxTextBytes < 0 {
		return nil, fmt.Errorf("filesystem text read limit must be at least 1 byte")
	}
	if opts.MaxTextBytes > 0 {
		maxTextBytes = opts.MaxTextBytes
	}
	if maxTextBytes < 1 {
		return nil, fmt.Errorf("filesystem text read limit must be at least 1 byte")
	}
	return &Service{listLimit: listLimit, maxTextBytes: maxTextBytes}, nil
}

func (s Service) List(ctx context.Context, root workspace.Root, opts workspace.FileListOptions) (workspace.FileList, error) {
	if err := ctx.Err(); err != nil {
		return workspace.FileList{}, err
	}
	abs, err := root.Resolve(opts.Path)
	if err != nil {
		return workspace.FileList{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return workspace.FileList{}, err
	}
	if !info.IsDir() {
		return workspace.FileList{}, fmt.Errorf("workspace path %q is not a directory", opts.Path.String())
	}
	limit := s.effectiveLimit(opts.Limit)
	query := strings.ToLower(strings.TrimSpace(opts.Search))
	result := workspace.FileList{RootPath: root.Path(), Path: opts.Path}
	if query == "" {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return workspace.FileList{}, err
		}
		items := make([]workspace.FileEntry, 0, min(len(entries), limit))
		for i, entry := range entries {
			if err := ctx.Err(); err != nil {
				return workspace.FileList{}, err
			}
			if i >= limit {
				result.Truncated = true
				break
			}
			item, err := s.readEntry(root, abs, entry)
			if err != nil {
				return workspace.FileList{}, err
			}
			items = append(items, item)
		}
		result.Items = items
		return result, nil
	}
	items := make([]workspace.FileEntry, 0, limit)
	err = filepath.WalkDir(abs, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == abs {
			return nil
		}
		rel, err := root.Relative(current)
		if err != nil {
			return err
		}
		match := strings.Contains(strings.ToLower(rel.String()), query) || strings.Contains(strings.ToLower(entry.Name()), query)
		if !match {
			return nil
		}
		if len(items) >= limit {
			result.Truncated = true
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		item, err := s.readEntry(root, filepath.Dir(current), entry)
		if err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	if err != nil {
		return workspace.FileList{}, err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Path.String() < items[j].Path.String()
	})
	result.Items = items
	return result, nil
}

func (s Service) Read(ctx context.Context, root workspace.Root, rel workspace.RelPath) (workspace.FileRead, error) {
	if err := ctx.Err(); err != nil {
		return workspace.FileRead{}, err
	}
	abs, err := root.Resolve(rel)
	if err != nil {
		return workspace.FileRead{}, err
	}
	return s.readPath(ctx, abs, rel.String(), rel.Name(), "workspace path")
}

func (s Service) ReadAbsolute(ctx context.Context, rawPath string) (workspace.FileRead, error) {
	if err := ctx.Err(); err != nil {
		return workspace.FileRead{}, err
	}
	value := strings.TrimSpace(rawPath)
	if value == "" {
		return workspace.FileRead{}, fmt.Errorf("absolute file path is required")
	}
	if strings.ContainsRune(value, '\x00') {
		return workspace.FileRead{}, fmt.Errorf("absolute file path contains NUL")
	}
	if !filepath.IsAbs(value) {
		return workspace.FileRead{}, fmt.Errorf("file path %q must be absolute", rawPath)
	}
	abs := filepath.Clean(value)
	return s.readPath(ctx, abs, abs, filepath.Base(abs), "file path")
}

func (s Service) readPath(ctx context.Context, abs string, displayPath string, downloadName string, errorLabel string) (workspace.FileRead, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return workspace.FileRead{}, err
	}
	if info.IsDir() {
		return workspace.FileRead{}, fmt.Errorf("%s %q is a directory", errorLabel, displayPath)
	}
	f, err := os.Open(abs)
	if err != nil {
		return workspace.FileRead{}, err
	}
	defer f.Close()
	sample, err := readSample(f)
	if err != nil {
		return workspace.FileRead{}, err
	}
	if err := ctx.Err(); err != nil {
		return workspace.FileRead{}, err
	}
	mimeType := detectMIME(abs, sample)
	result := workspace.FileRead{
		Path:         displayPath,
		MIMEType:     mimeType,
		SizeBytes:    info.Size(),
		DownloadName: downloadName,
	}
	if isImageMime(mimeType) {
		result.Kind = workspace.ContentKindImage
		return result, nil
	}
	if info.Size() > s.maxTextBytes {
		result.Kind = workspace.ContentKindUnsupported
		result.UnsupportedReason = fmt.Sprintf("file exceeds %d byte text limit", s.maxTextBytes)
		return result, nil
	}
	if !looksLikeText(sample, mimeType) {
		result.Kind = workspace.ContentKindUnsupported
		result.UnsupportedReason = "binary content not supported"
		return result, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return workspace.FileRead{}, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return workspace.FileRead{}, err
	}
	if err := ctx.Err(); err != nil {
		return workspace.FileRead{}, err
	}
	if !looksLikeText(data, mimeType) {
		result.Kind = workspace.ContentKindUnsupported
		result.UnsupportedReason = "binary content not supported"
		return result, nil
	}
	result.Kind = workspace.ContentKindText
	result.Encoding = "utf-8"
	result.Text = browserText(data)
	return result, nil
}

func (s Service) effectiveLimit(requested int) int {
	if requested <= 0 {
		return s.listLimit
	}
	if requested > s.listLimit {
		return s.listLimit
	}
	return requested
}

func (s Service) readEntry(root workspace.Root, parentAbs string, entry fs.DirEntry) (workspace.FileEntry, error) {
	abs := filepath.Join(parentAbs, entry.Name())
	rel, err := root.Relative(abs)
	if err != nil {
		return workspace.FileEntry{}, err
	}
	info, err := entry.Info()
	if err != nil {
		return workspace.FileEntry{}, err
	}
	kind := workspace.EntryKindFile
	if info.IsDir() {
		kind = workspace.EntryKindDirectory
	}
	item := workspace.FileEntry{
		Path:     rel,
		Name:     entry.Name(),
		Kind:     kind,
		Modified: info.ModTime(),
	}
	if !info.IsDir() {
		item.SizeBytes = info.Size()
	}
	return item, nil
}

func readSample(r io.Reader) ([]byte, error) {
	buf := make([]byte, sniffBytes)
	n, err := io.ReadFull(r, buf)
	if err == nil {
		return buf[:n], nil
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return buf[:n], nil
	}
	return nil, err
}

func detectMIME(name string, data []byte) string {
	sample := data
	if len(sample) > sniffBytes {
		sample = sample[:sniffBytes]
	}
	mimeType := http.DetectContentType(sample)
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExt != "" {
		extType := strings.TrimSpace(strings.SplitN(byExt, ";", 2)[0])
		if isImageMime(extType) && !isImageMime(mimeType) {
			return extType
		}
		if mimeType == "application/octet-stream" {
			return extType
		}
	}
	return mimeType
}

func isImageMime(mimeType string) bool {
	return strings.HasPrefix(strings.ToLower(mimeType), "image/")
}

func looksLikeText(data []byte, mimeType string) bool {
	typeRoot := strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	if isTextMime(typeRoot) {
		return !bytesContainNUL(data)
	}
	sample := data
	if len(sample) > sniffBytes {
		sample = sample[:sniffBytes]
	}
	if bytesContainNUL(sample) {
		return false
	}
	return utf8.Valid(sample)
}

func isTextMime(typeRoot string) bool {
	if strings.HasPrefix(typeRoot, "text/") {
		return true
	}
	switch typeRoot {
	case "application/json", "application/xml", "application/javascript", "application/x-yaml", "image/svg+xml":
		return true
	default:
		return false
	}
}

func browserText(data []byte) string {
	value := string(data)
	if utf8.ValidString(value) {
		return value
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}

func bytesContainNUL(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
