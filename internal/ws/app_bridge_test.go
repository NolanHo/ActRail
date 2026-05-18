package ws

import (
	"context"
	"errors"
	"testing"
	"time"

	"actrail/internal/app"
	"actrail/internal/domain/session"
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
