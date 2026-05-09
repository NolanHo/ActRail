package workspace

import "time"

type EntryKind string

const (
	EntryKindDirectory EntryKind = "directory"
	EntryKindFile      EntryKind = "file"
)

type ContentKind string

const (
	ContentKindText        ContentKind = "text"
	ContentKindImage       ContentKind = "image"
	ContentKindUnsupported ContentKind = "unsupported"
)

type FileListOptions struct {
	Path   RelPath
	Search string
	Limit  int
}

type FileList struct {
	RootPath  string
	Path      RelPath
	Items     []FileEntry
	Truncated bool
}

type FileEntry struct {
	Path      RelPath
	Name      string
	Kind      EntryKind
	SizeBytes int64
	Modified  time.Time
}

type FileRead struct {
	Path              string
	Kind              ContentKind
	MIMEType          string
	Encoding          string
	SizeBytes         int64
	Text              string
	DownloadName      string
	UnsupportedReason string
}

type FileVersionList struct {
	Path           RelPath
	Items          []FileVersion
	FallbackReason string
}

type FileVersion struct {
	VersionID  string
	Label      string
	CommitHash string
	Author     string
	CommitAt   time.Time
	Message    string
	Current    bool
}
