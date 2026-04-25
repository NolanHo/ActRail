package ws

import (
	"reflect"
	"testing"
	"time"
)

func TestReplayBufferReplaysFramesAfterCursor(t *testing.T) {
	buffer, err := NewReplayBuffer(4)
	if err != nil {
		t.Fatalf("NewReplayBuffer() error = %v", err)
	}
	stream := StreamName("session:s_123")
	now := time.Unix(1760000000, 0)
	for i := int64(41); i <= 43; i++ {
		if err := buffer.Append(stream, i, NewAckFrame(now, "evt", stream, "req", FrameTypeSend)); err != nil {
			t.Fatalf("Append(%d) error = %v", i, err)
		}
	}

	frames, err := buffer.Replay(stream, 41)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("len(Replay()) = %d, want 2", len(frames))
	}
}

func TestReplayBufferRequiresResetWhenCursorFallsOff(t *testing.T) {
	buffer, err := NewReplayBuffer(2)
	if err != nil {
		t.Fatalf("NewReplayBuffer() error = %v", err)
	}
	stream := StreamName("session:s_123")
	now := time.Unix(1760000000, 0)
	for i := int64(41); i <= 43; i++ {
		if err := buffer.Append(stream, i, NewAckFrame(now, "evt", stream, "req", FrameTypeSend)); err != nil {
			t.Fatalf("Append(%d) error = %v", i, err)
		}
	}

	_, err = buffer.Replay(stream, 40)
	resetErr, ok := err.(ResetRequiredError)
	if !ok {
		t.Fatalf("Replay() error = %T, want ResetRequiredError", err)
	}
	if resetErr.Oldest != 42 || resetErr.Latest != 43 {
		t.Fatalf("ResetRequiredError = %#v", resetErr)
	}
}

func TestReplayBufferTracksLatestCursor(t *testing.T) {
	buffer, err := NewReplayBuffer(2)
	if err != nil {
		t.Fatalf("NewReplayBuffer() error = %v", err)
	}
	stream := StreamName("session:s_123")
	now := time.Unix(1760000000, 0)
	if err := buffer.Append(stream, 7, NewAckFrame(now, "evt", stream, "req", FrameTypeSend)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	latest, ok := buffer.LatestCursor(stream)
	if !ok || latest != 7 {
		t.Fatalf("LatestCursor() = (%d, %v), want (7, true)", latest, ok)
	}
}

func TestSubscriptionSetSnapshotSorted(t *testing.T) {
	set := NewSubscriptionSet()
	cursor := int64(9)
	if err := set.ApplySubscribe(SubscribePayload{Streams: []Subscription{{Name: StreamName("session:s_123:ui"), ResumeFrom: &cursor}, {Name: SessionsStream}}}); err != nil {
		t.Fatalf("ApplySubscribe() error = %v", err)
	}
	got := set.Snapshot()
	want := []Subscription{{Name: SessionsStream}, {Name: StreamName("session:s_123:ui"), ResumeFrom: &cursor}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %#v, want %#v", got, want)
	}
}
