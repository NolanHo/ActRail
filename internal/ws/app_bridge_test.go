package ws

import (
	"context"
	"errors"
	"testing"
	"time"

	"actrail/internal/app"
	"actrail/internal/domain/session"
	"actrail/internal/realtime"
)

type timeoutSessionController struct {
	app.SessionController
	sawDeadline chan bool
}

func (c *timeoutSessionController) Send(ctx context.Context, _ app.SendRequest) (app.SendResponse, error) {
	_, ok := ctx.Deadline()
	c.sawDeadline <- ok
	<-ctx.Done()
	return app.SendResponse{}, ctx.Err()
}

func TestAppBridgeHandleSendUsesCommandDeadline(t *testing.T) {
	controller := &timeoutSessionController{sawDeadline: make(chan bool, 1)}
	bridge := NewAppBridge(controller, nil, nil)
	bridge.commandTimeout = 10 * time.Millisecond
	sessionID, err := session.ParseSessionID("s_123")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	started := time.Now()
	err = bridge.HandleSend(SendCommand{SessionID: sessionID, Text: "hello"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HandleSend() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("HandleSend() elapsed = %s, want deadline-bounded return", elapsed)
	}
	select {
	case saw := <-controller.sawDeadline:
		if !saw {
			t.Fatal("Send() context deadline present = false, want true")
		}
	default:
		t.Fatal("Send() was not called")
	}
}

type captureObserver struct {
	events []realtime.Event
}

func (o *captureObserver) ObserveEvent(event realtime.Event) {
	o.events = append(o.events, event)
}

func TestAppBridgeBroadcastsSystemEventsToObserver(t *testing.T) {
	observer := &captureObserver{}
	publisher := NewPublisher(NewRegistry(), nil, WithEventObserver(observer))
	bridge := NewAppBridge(nil, nil, publisher)

	bridge.PublishWaitsUpdated(app.WaitsUpdatedEvent{Waits: []app.ActiveWaitSummary{{
		WaitID:    "wait_1",
		ThreadID:  "thread_1",
		SessionID: "s_123",
		State:     app.WaitPendingUnread,
		Question:  "approve?",
	}}})
	bridge.PublishNotification(app.NotificationEvent{SessionID: "s_123", Title: "done", Body: "ready"})

	if len(observer.events) != 2 {
		t.Fatalf("observer events = %+v, want 2 events", observer.events)
	}
	if observer.events[0].Type != string(FrameTypeWaitsUpdated) || observer.events[0].Stream != SystemStream.String() {
		t.Fatalf("waits.updated event = %+v", observer.events[0])
	}
	if observer.events[1].Type != string(FrameTypeNotification) || observer.events[1].Stream != SystemStream.String() {
		t.Fatalf("notification event = %+v", observer.events[1])
	}
}
