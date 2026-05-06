package app

import (
	"context"
	"testing"
	"time"

	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func newWaitTestStub(now *time.Time) *Stub {
	return NewStubForTest(config.Load(), func() time.Time { return *now }, RuntimeConfig{})
}

func createWaitTestSessionID(t *testing.T, svc *Stub) session.SessionID {
	t.Helper()
	identity, err := session.NewLiveIdentity(newID("s"), newID("r"), newID("t"), session.BackendPI.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	_, err = svc.registry.Create(sessionCreateSpec{Identity: &identity, Backend: session.BackendPI, CWD: t.TempDir(), Runtime: sessionRuntime{protocol: runtimeProtocolTTY}})
	if err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	return identity.SessionID()
}

func createWaitTestSession(t *testing.T, svc *Stub) string {
	t.Helper()
	return createWaitTestSessionID(t, svc).String()
}

func createWaitRequest(t *testing.T, sessionID string) CreateWaitRequest {
	t.Helper()
	return CreateWaitRequest{SessionID: mustSessionID(t, sessionID), Question: "Need input?", BlockingReason: "blocked", Attempted: "looked", DefaultIfNoReply: "fallback"}
}

func TestWaitLifecycleAndUniqueness(t *testing.T) {
	now := time.Unix(1760000000, 0).UTC()
	svc := newWaitTestStub(&now)
	sessionID := createWaitTestSession(t, svc)
	req := createWaitRequest(t, sessionID)
	created, err := svc.CreateWait(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateWait() error = %v", err)
	}
	if created.Wait == nil || created.Wait.State != WaitPendingUnread || created.ActiveWait == nil {
		t.Fatalf("CreateWait() = %+v", created)
	}
	if _, err := svc.CreateWait(context.Background(), req); err == nil {
		t.Fatal("CreateWait(second active) error = nil, want conflict")
	}
	if _, err := svc.AnswerWait(context.Background(), WaitLifecycleRequest{SessionID: req.SessionID, WaitID: created.Wait.WaitID, Answer: "answer"}); err == nil {
		t.Fatal("AnswerWait(unclaimed) error = nil, want conflict")
	}
	claimed, err := svc.ClaimWait(context.Background(), WaitLifecycleRequest{SessionID: req.SessionID, WaitID: created.Wait.WaitID})
	if err != nil {
		t.Fatalf("ClaimWait() error = %v", err)
	}
	if claimed.Wait == nil || claimed.Wait.State != WaitClaimed || claimed.ActiveWait == nil {
		t.Fatalf("ClaimWait() = %+v", claimed)
	}
	answered, err := svc.AnswerWait(context.Background(), WaitLifecycleRequest{SessionID: req.SessionID, WaitID: created.Wait.WaitID, Answer: "answer"})
	if err != nil {
		t.Fatalf("AnswerWait() error = %v", err)
	}
	if answered.Wait == nil || answered.Wait.State != WaitAnswered || answered.Wait.Answer != "answer" || answered.ActiveWait != nil {
		t.Fatalf("AnswerWait() = %+v", answered)
	}
}

func TestWaitTimeoutOnlyPendingUnread(t *testing.T) {
	now := time.Unix(1760000000, 0).UTC()
	svc := newWaitTestStub(&now)
	sessionID := createWaitTestSession(t, svc)
	minutes := 1
	req := createWaitRequest(t, sessionID)
	req.TimeoutAfterMinutes = &minutes
	created, err := svc.CreateWait(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateWait() error = %v", err)
	}
	now = now.Add(time.Minute)
	if err := svc.sweepWaitTimeouts(context.Background()); err != nil {
		t.Fatalf("sweepWaitTimeouts() error = %v", err)
	}
	thread, err := svc.WaitThread(context.Background(), WaitThreadRequest{SessionID: req.SessionID, ThreadID: created.Wait.ThreadID})
	if err != nil {
		t.Fatalf("WaitThread() error = %v", err)
	}
	if len(thread.Waits) != 1 || thread.Waits[0].State != WaitTimedOut || thread.Waits[0].FallbackUsed != "fallback" {
		t.Fatalf("timed out wait = %+v", thread.Waits)
	}

	sessionID = createWaitTestSession(t, svc)
	req = createWaitRequest(t, sessionID)
	req.TimeoutAfterMinutes = &minutes
	created, err = svc.CreateWait(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateWait(claimed) error = %v", err)
	}
	if _, err := svc.ClaimWait(context.Background(), WaitLifecycleRequest{SessionID: req.SessionID, WaitID: created.Wait.WaitID}); err != nil {
		t.Fatalf("ClaimWait() error = %v", err)
	}
	now = now.Add(time.Minute)
	if err := svc.sweepWaitTimeouts(context.Background()); err != nil {
		t.Fatalf("sweepWaitTimeouts(claimed) error = %v", err)
	}
	thread, err = svc.WaitThread(context.Background(), WaitThreadRequest{SessionID: req.SessionID, ThreadID: created.Wait.ThreadID})
	if err != nil {
		t.Fatalf("WaitThread(claimed) error = %v", err)
	}
	if len(thread.Waits) != 1 || thread.Waits[0].State != WaitClaimed {
		t.Fatalf("claimed timeout wait = %+v", thread.Waits)
	}
}

func TestAskUserWaitReturnsAnswerAndTimeout(t *testing.T) {
	now := time.Unix(1760000000, 0).UTC()
	svc := newWaitTestStub(&now)
	sessionID := mustSessionID(t, createWaitTestSession(t, svc))
	resultCh := make(chan RuntimeWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := svc.AskUserWait(context.Background(), RuntimeWaitRequest{SessionID: sessionID, RequestID: "ask-1", Question: "Need input?", BlockingReason: "blocked", Attempted: "looked", DefaultIfNoReply: "fallback"})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()
	waitForAppCondition(t, func() bool { return svc.activeWaitForSession(sessionID) != nil })
	wait := svc.activeWaitForSession(sessionID)
	if _, err := svc.ClaimWait(context.Background(), WaitLifecycleRequest{SessionID: sessionID, WaitID: wait.WaitID}); err != nil {
		t.Fatalf("ClaimWait() error = %v", err)
	}
	if _, err := svc.AnswerWait(context.Background(), WaitLifecycleRequest{SessionID: sessionID, WaitID: wait.WaitID, Answer: "answer"}); err != nil {
		t.Fatalf("AnswerWait() error = %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("AskUserWait() error = %v", err)
	case result := <-resultCh:
		if result.State != RuntimeWaitAnswered || result.Answer != "answer" || result.WaitID != wait.WaitID {
			t.Fatalf("AskUserWait() = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("AskUserWait() did not return")
	}

	minutes := 1
	timeoutCh := make(chan RuntimeWaitResult, 1)
	go func() {
		result, err := svc.AskUserWait(context.Background(), RuntimeWaitRequest{SessionID: sessionID, RequestID: "ask-2", Question: "Need input?", BlockingReason: "blocked", Attempted: "looked", DefaultIfNoReply: "fallback", TimeoutAfterMinutes: &minutes})
		if err != nil {
			errCh <- err
			return
		}
		timeoutCh <- result
	}()
	waitForAppCondition(t, func() bool {
		active := svc.activeWaitForSession(sessionID)
		return active != nil && active.WaitID != wait.WaitID
	})
	timeoutWait := svc.activeWaitForSession(sessionID)
	now = now.Add(time.Minute)
	if err := svc.sweepWaitTimeouts(context.Background()); err != nil {
		t.Fatalf("sweepWaitTimeouts() error = %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("AskUserWait(timeout) error = %v", err)
	case result := <-timeoutCh:
		if result.State != RuntimeWaitTimedOut || result.FallbackUsed != "fallback" || result.WaitID != timeoutWait.WaitID {
			t.Fatalf("AskUserWait(timeout) = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("AskUserWait(timeout) did not return")
	}
}
