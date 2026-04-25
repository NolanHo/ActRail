package ws

import (
	"reflect"
	"testing"
	"time"
)

type recordingFrameWriter struct {
	frames []Frame
	err    error
}

func (w *recordingFrameWriter) WriteFrames(frames ...Frame) error {
	if w.err != nil {
		return w.err
	}
	w.frames = append(w.frames, frames...)
	return nil
}

func TestPublisherPublishStoresReplayAndDeliversSubscribedSessionFrames(t *testing.T) {
	now := time.Unix(1760000000, 0)
	replay, err := NewReplayBuffer(8)
	if err != nil {
		t.Fatalf("NewReplayBuffer() error = %v", err)
	}
	registry := NewRegistry()
	publisher := NewPublisher(registry, replay, WithPublisherNow(func() time.Time { return now }))

	writerA := &recordingFrameWriter{}
	connA, err := NewConnectionState("conn_a", 15*time.Second, nil, now)
	if err != nil {
		t.Fatalf("NewConnectionState(conn_a) error = %v", err)
	}
	if err := connA.AttachWriter(writerA); err != nil {
		t.Fatalf("AttachWriter(conn_a) error = %v", err)
	}
	if err := connA.subscriptions.ApplySubscribe(SubscribePayload{Streams: []Subscription{{Name: StreamName("session:s_123")}}}); err != nil {
		t.Fatalf("ApplySubscribe(conn_a) error = %v", err)
	}
	if err := registry.Add(connA); err != nil {
		t.Fatalf("registry.Add(conn_a) error = %v", err)
	}

	writerB := &recordingFrameWriter{}
	connB, err := NewConnectionState("conn_b", 15*time.Second, nil, now)
	if err != nil {
		t.Fatalf("NewConnectionState(conn_b) error = %v", err)
	}
	if err := connB.AttachWriter(writerB); err != nil {
		t.Fatalf("AttachWriter(conn_b) error = %v", err)
	}
	if err := connB.subscriptions.ApplySubscribe(SubscribePayload{Streams: []Subscription{{Name: StreamName("session:s_999")}}}); err != nil {
		t.Fatalf("ApplySubscribe(conn_b) error = %v", err)
	}
	if err := registry.Add(connB); err != nil {
		t.Fatalf("registry.Add(conn_b) error = %v", err)
	}

	frame := Frame{
		Type:   FrameTypeSessionState,
		ID:     "evt_pub_1",
		TS:     UnixTS(now),
		Stream: "session:s_123",
		Payload: map[string]any{
			"session_id": "s_123",
			"stream_seq": 41,
			"busy":       true,
		},
	}
	report, err := publisher.PublishSession(41, frame)
	if err != nil {
		t.Fatalf("PublishSession() error = %v", err)
	}
	if !report.Stored || report.Delivered != 1 || report.Stream != StreamName("session:s_123") {
		t.Fatalf("PublishSession() report = %#v", report)
	}
	frames, err := replay.Replay(StreamName("session:s_123"), 40)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if !reflect.DeepEqual(frames, []Frame{frame}) {
		t.Fatalf("Replay() = %#v, want %#v", frames, []Frame{frame})
	}
	if !reflect.DeepEqual(writerA.frames, []Frame{frame}) {
		t.Fatalf("writerA.frames = %#v, want %#v", writerA.frames, []Frame{frame})
	}
	if len(writerB.frames) != 0 {
		t.Fatalf("writerB.frames = %#v, want none", writerB.frames)
	}
}

func TestPublisherBroadcastSessionsTargetsSessionsSubscribersOnly(t *testing.T) {
	now := time.Unix(1760000000, 0)
	registry := NewRegistry()
	publisher := NewPublisher(registry, nil, WithPublisherNow(func() time.Time { return now }))

	writerA := &recordingFrameWriter{}
	connA, err := NewConnectionState("conn_a", 15*time.Second, nil, now)
	if err != nil {
		t.Fatalf("NewConnectionState(conn_a) error = %v", err)
	}
	if err := connA.AttachWriter(writerA); err != nil {
		t.Fatalf("AttachWriter(conn_a) error = %v", err)
	}
	if err := connA.subscriptions.ApplySubscribe(SubscribePayload{Streams: []Subscription{{Name: SessionsStream}}}); err != nil {
		t.Fatalf("ApplySubscribe(conn_a) error = %v", err)
	}
	if err := registry.Add(connA); err != nil {
		t.Fatalf("registry.Add(conn_a) error = %v", err)
	}

	writerB := &recordingFrameWriter{}
	connB, err := NewConnectionState("conn_b", 15*time.Second, nil, now)
	if err != nil {
		t.Fatalf("NewConnectionState(conn_b) error = %v", err)
	}
	if err := connB.AttachWriter(writerB); err != nil {
		t.Fatalf("AttachWriter(conn_b) error = %v", err)
	}
	if err := connB.subscriptions.ApplySubscribe(SubscribePayload{Streams: []Subscription{{Name: StreamName("session:s_123")}}}); err != nil {
		t.Fatalf("ApplySubscribe(conn_b) error = %v", err)
	}
	if err := registry.Add(connB); err != nil {
		t.Fatalf("registry.Add(conn_b) error = %v", err)
	}

	frame := Frame{
		Type:   FrameTypeSessionsUpdated,
		ID:     "evt_pub_2",
		TS:     UnixTS(now),
		Stream: SessionsStream.String(),
		Payload: map[string]any{
			"reason": "session_created",
		},
	}
	report, err := publisher.BroadcastSessions(frame)
	if err != nil {
		t.Fatalf("BroadcastSessions() error = %v", err)
	}
	if report.Stored || report.Delivered != 1 || report.Stream != SessionsStream {
		t.Fatalf("BroadcastSessions() report = %#v", report)
	}
	if !reflect.DeepEqual(writerA.frames, []Frame{frame}) {
		t.Fatalf("writerA.frames = %#v, want %#v", writerA.frames, []Frame{frame})
	}
	if len(writerB.frames) != 0 {
		t.Fatalf("writerB.frames = %#v, want none", writerB.frames)
	}
}
