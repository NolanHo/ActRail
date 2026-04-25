package ws

import (
	"fmt"
	"sort"
	"sync"
)

type BufferedFrame struct {
	Cursor int64
	Frame  Frame
}

type ResetRequiredError struct {
	Stream     StreamName
	ResumeFrom int64
	Oldest     int64
	Latest     int64
}

func (e ResetRequiredError) Error() string {
	return fmt.Sprintf("resume cursor %d for %q expired: buffer range [%d,%d]", e.ResumeFrom, e.Stream, e.Oldest, e.Latest)
}

type ReplayBuffer struct {
	mu      sync.RWMutex
	limit   int
	streams map[StreamName][]BufferedFrame
}

func NewReplayBuffer(limit int) (*ReplayBuffer, error) {
	if limit < 1 {
		return nil, fmt.Errorf("replay buffer limit must be at least 1")
	}
	return &ReplayBuffer{limit: limit, streams: make(map[StreamName][]BufferedFrame)}, nil
}

func (b *ReplayBuffer) Append(stream StreamName, cursor int64, frame Frame) error {
	if err := stream.Validate(); err != nil {
		return err
	}
	if cursor < 1 {
		return fmt.Errorf("replay cursor must be at least 1")
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	if frame.Stream != stream.String() {
		return fmt.Errorf("frame stream %q does not match replay stream %q", frame.Stream, stream)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	items := b.streams[stream]
	if n := len(items); n > 0 && cursor <= items[n-1].Cursor {
		return fmt.Errorf("replay cursor %d for %q must increase beyond %d", cursor, stream, items[n-1].Cursor)
	}
	items = append(items, BufferedFrame{Cursor: cursor, Frame: frame})
	if len(items) > b.limit {
		items = append([]BufferedFrame(nil), items[len(items)-b.limit:]...)
	}
	b.streams[stream] = items
	return nil
}

func (b *ReplayBuffer) Replay(stream StreamName, resumeFrom int64) ([]Frame, error) {
	if err := stream.Validate(); err != nil {
		return nil, err
	}
	if resumeFrom < 0 {
		return nil, fmt.Errorf("resume cursor must be non-negative")
	}

	b.mu.RLock()
	items := append([]BufferedFrame(nil), b.streams[stream]...)
	b.mu.RUnlock()

	if len(items) == 0 {
		if resumeFrom == 0 {
			return nil, nil
		}
		return nil, ResetRequiredError{Stream: stream, ResumeFrom: resumeFrom}
	}

	oldest := items[0].Cursor
	latest := items[len(items)-1].Cursor
	if resumeFrom < oldest-1 {
		return nil, ResetRequiredError{Stream: stream, ResumeFrom: resumeFrom, Oldest: oldest, Latest: latest}
	}
	start := sort.Search(len(items), func(i int) bool {
		return items[i].Cursor > resumeFrom
	})
	frames := make([]Frame, 0, len(items)-start)
	for _, item := range items[start:] {
		frames = append(frames, item.Frame)
	}
	return frames, nil
}

func (b *ReplayBuffer) LatestCursor(stream StreamName) (int64, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	items := b.streams[stream]
	if len(items) == 0 {
		return 0, false
	}
	return items[len(items)-1].Cursor, true
}
