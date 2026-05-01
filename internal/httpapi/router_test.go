package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	"actrail/internal/httpapi/authn"
	"actrail/internal/ws"
)

type serviceStub struct {
	base              *app.Stub
	listSessionsFunc  func(context.Context, app.ListSessionsRequest) (app.ListSessionsResponse, error)
	createSessionFunc func(context.Context, app.CreateSessionRequest) (app.CreateSessionResponse, error)
	resumeFunc        func(context.Context, app.SessionResumeCandidatesRequest) (app.SessionResumeCandidatesResponse, error)
	detailsFunc       func(context.Context, app.SessionDetailsRequest) (app.SessionDetailsResponse, error)
	messagesFunc      func(context.Context, app.SessionMessagesRequest) (app.SessionMessagesResponse, error)
	stateFunc         func(context.Context, app.SessionStateRequest) (app.SessionStateResponse, error)
	probeStateFunc    func(context.Context, app.ProbeSessionStateRequest) (app.ProbeSessionStateResponse, error)
	workspaceFunc     func(context.Context, app.SessionWorkspaceRequest) (app.SessionWorkspaceResponse, error)
	workspaceSetFunc  func(context.Context, app.UpdateSessionWorkspaceRequest) (app.SessionWorkspaceResponse, error)
	fileListFunc      func(context.Context, app.WorkspaceFileListRequest) (app.WorkspaceFileListResponse, error)
	fileReadFunc      func(context.Context, app.WorkspaceFileReadRequest) (app.WorkspaceFileReadResponse, error)
	gitFunc           func(context.Context, app.GitFileVersionsRequest) (app.GitFileVersionsResponse, error)
	renameFunc        func(context.Context, app.RenameSessionRequest) (app.RenameSessionResponse, error)
	focusFunc         func(context.Context, app.FocusSessionRequest) (app.FocusSessionResponse, error)
	editFunc          func(context.Context, app.EditSessionRequest) (app.EditSessionResponse, error)
	editCwdGroupFunc  func(context.Context, app.EditCwdGroupRequest) (app.EditCwdGroupResponse, error)
	modelFunc         func(context.Context, app.SwitchSessionModelRequest) (app.SwitchSessionModelResponse, error)
	deleteFunc        func(context.Context, app.DeleteSessionRequest) (app.DeleteSessionResponse, error)
	restartFunc       func(context.Context, app.RestartSessionRequest) (app.RestartSessionResponse, error)
	handoffFunc       func(context.Context, app.HandoffSessionRequest) (app.HandoffSessionResponse, error)
}

func newServiceStub(cfg config.Config) serviceStub {
	return serviceStub{base: app.NewStubForTest(cfg, time.Now, app.RuntimeConfig{})}
}

func (s serviceStub) Bootstrap(ctx context.Context, req app.BootstrapRequest) app.BootstrapSnapshot {
	return s.base.Bootstrap(ctx, req)
}

func (s serviceStub) ListSessions(ctx context.Context, req app.ListSessionsRequest) (app.ListSessionsResponse, error) {
	if s.listSessionsFunc != nil {
		return s.listSessionsFunc(ctx, req)
	}
	return s.base.ListSessions(ctx, req)
}

func (s serviceStub) CreateSession(ctx context.Context, req app.CreateSessionRequest) (app.CreateSessionResponse, error) {
	if s.createSessionFunc != nil {
		return s.createSessionFunc(ctx, req)
	}
	return s.base.CreateSession(ctx, req)
}

func (s serviceStub) SessionResumeCandidates(ctx context.Context, req app.SessionResumeCandidatesRequest) (app.SessionResumeCandidatesResponse, error) {
	if s.resumeFunc != nil {
		return s.resumeFunc(ctx, req)
	}
	return s.base.SessionResumeCandidates(ctx, req)
}

func (s serviceStub) SessionDetails(ctx context.Context, req app.SessionDetailsRequest) (app.SessionDetailsResponse, error) {
	if s.detailsFunc != nil {
		return s.detailsFunc(ctx, req)
	}
	return s.base.SessionDetails(ctx, req)
}

func (s serviceStub) SessionMessages(ctx context.Context, req app.SessionMessagesRequest) (app.SessionMessagesResponse, error) {
	if s.messagesFunc != nil {
		return s.messagesFunc(ctx, req)
	}
	return s.base.SessionMessages(ctx, req)
}

func (s serviceStub) SessionState(ctx context.Context, req app.SessionStateRequest) (app.SessionStateResponse, error) {
	if s.stateFunc != nil {
		return s.stateFunc(ctx, req)
	}
	return s.base.SessionState(ctx, req)
}

func (s serviceStub) ProbeSessionState(ctx context.Context, req app.ProbeSessionStateRequest) (app.ProbeSessionStateResponse, error) {
	if s.probeStateFunc != nil {
		return s.probeStateFunc(ctx, req)
	}
	return s.base.ProbeSessionState(ctx, req)
}

func (s serviceStub) SessionWorkspace(ctx context.Context, req app.SessionWorkspaceRequest) (app.SessionWorkspaceResponse, error) {
	if s.workspaceFunc != nil {
		return s.workspaceFunc(ctx, req)
	}
	return s.base.SessionWorkspace(ctx, req)
}

func (s serviceStub) UpdateSessionWorkspace(ctx context.Context, req app.UpdateSessionWorkspaceRequest) (app.SessionWorkspaceResponse, error) {
	if s.workspaceSetFunc != nil {
		return s.workspaceSetFunc(ctx, req)
	}
	return s.base.UpdateSessionWorkspace(ctx, req)
}

func (s serviceStub) WorkspaceFileList(ctx context.Context, req app.WorkspaceFileListRequest) (app.WorkspaceFileListResponse, error) {
	if s.fileListFunc != nil {
		return s.fileListFunc(ctx, req)
	}
	return s.base.WorkspaceFileList(ctx, req)
}

func (s serviceStub) WorkspaceFileRead(ctx context.Context, req app.WorkspaceFileReadRequest) (app.WorkspaceFileReadResponse, error) {
	if s.fileReadFunc != nil {
		return s.fileReadFunc(ctx, req)
	}
	return s.base.WorkspaceFileRead(ctx, req)
}

func (s serviceStub) GitFileVersions(ctx context.Context, req app.GitFileVersionsRequest) (app.GitFileVersionsResponse, error) {
	if s.gitFunc != nil {
		return s.gitFunc(ctx, req)
	}
	return s.base.GitFileVersions(ctx, req)
}

func (s serviceStub) RenameSession(ctx context.Context, req app.RenameSessionRequest) (app.RenameSessionResponse, error) {
	if s.renameFunc != nil {
		return s.renameFunc(ctx, req)
	}
	return s.base.RenameSession(ctx, req)
}

func (s serviceStub) FocusSession(ctx context.Context, req app.FocusSessionRequest) (app.FocusSessionResponse, error) {
	if s.focusFunc != nil {
		return s.focusFunc(ctx, req)
	}
	return s.base.FocusSession(ctx, req)
}

func (s serviceStub) EditSession(ctx context.Context, req app.EditSessionRequest) (app.EditSessionResponse, error) {
	if s.editFunc != nil {
		return s.editFunc(ctx, req)
	}
	return s.base.EditSession(ctx, req)
}

func (s serviceStub) EditCwdGroup(ctx context.Context, req app.EditCwdGroupRequest) (app.EditCwdGroupResponse, error) {
	if s.editCwdGroupFunc != nil {
		return s.editCwdGroupFunc(ctx, req)
	}
	return s.base.EditCwdGroup(ctx, req)
}

func (s serviceStub) SwitchSessionModel(ctx context.Context, req app.SwitchSessionModelRequest) (app.SwitchSessionModelResponse, error) {
	if s.modelFunc != nil {
		return s.modelFunc(ctx, req)
	}
	return s.base.SwitchSessionModel(ctx, req)
}

func (s serviceStub) SessionCommands(ctx context.Context, req app.SessionCommandsRequest) (app.SessionCommandsResponse, error) {
	return s.base.SessionCommands(ctx, req)
}

func (s serviceStub) ExecuteSessionCommand(ctx context.Context, req app.ExecuteSessionCommandRequest) (app.ExecuteSessionCommandResponse, error) {
	return s.base.ExecuteSessionCommand(ctx, req)
}

func (s serviceStub) WaitInbox(ctx context.Context) (app.WaitInboxResponse, error) {
	return s.base.WaitInbox(ctx)
}

func (s serviceStub) WaitThreads(ctx context.Context, req app.WaitThreadsRequest) (app.WaitThreadsResponse, error) {
	return s.base.WaitThreads(ctx, req)
}

func (s serviceStub) WaitThread(ctx context.Context, req app.WaitThreadRequest) (app.WaitThreadResponse, error) {
	return s.base.WaitThread(ctx, req)
}

func (s serviceStub) CreateWait(ctx context.Context, req app.CreateWaitRequest) (app.WaitLifecycleResponse, error) {
	return s.base.CreateWait(ctx, req)
}

func (s serviceStub) ClaimWait(ctx context.Context, req app.WaitLifecycleRequest) (app.WaitLifecycleResponse, error) {
	return s.base.ClaimWait(ctx, req)
}

func (s serviceStub) AnswerWait(ctx context.Context, req app.WaitLifecycleRequest) (app.WaitLifecycleResponse, error) {
	return s.base.AnswerWait(ctx, req)
}

func (s serviceStub) CancelWait(ctx context.Context, req app.WaitLifecycleRequest) (app.WaitLifecycleResponse, error) {
	return s.base.CancelWait(ctx, req)
}

func (s serviceStub) DeleteSession(ctx context.Context, req app.DeleteSessionRequest) (app.DeleteSessionResponse, error) {
	if s.deleteFunc != nil {
		return s.deleteFunc(ctx, req)
	}
	return s.base.DeleteSession(ctx, req)
}

func (s serviceStub) RestartSession(ctx context.Context, req app.RestartSessionRequest) (app.RestartSessionResponse, error) {
	if s.restartFunc != nil {
		return s.restartFunc(ctx, req)
	}
	return s.base.RestartSession(ctx, req)
}

func (s serviceStub) HandoffSession(ctx context.Context, req app.HandoffSessionRequest) (app.HandoffSessionResponse, error) {
	if s.handoffFunc != nil {
		return s.handoffFunc(ctx, req)
	}
	return s.base.HandoffSession(ctx, req)
}

func (s serviceStub) SupervisorProvider(ctx context.Context, req app.SupervisorProviderRequest) (app.SupervisorProviderResponse, error) {
	return s.base.SupervisorProvider(ctx, req)
}

func (s serviceStub) UpdateSupervisorProvider(ctx context.Context, req app.UpdateSupervisorProviderRequest) (app.SupervisorProviderResponse, error) {
	return s.base.UpdateSupervisorProvider(ctx, req)
}

func (s serviceStub) SessionSupervisor(ctx context.Context, req app.SessionSupervisorRequest) (app.SessionSupervisorResponse, error) {
	return s.base.SessionSupervisor(ctx, req)
}

func (s serviceStub) UpdateSessionSupervisor(ctx context.Context, req app.UpdateSessionSupervisorRequest) (app.SessionSupervisorResponse, error) {
	return s.base.UpdateSessionSupervisor(ctx, req)
}

func (s serviceStub) SupervisorRuns(ctx context.Context, req app.SupervisorRunsRequest) (app.SupervisorRunsResponse, error) {
	return s.base.SupervisorRuns(ctx, req)
}

func (s serviceStub) RunSupervisorOnce(ctx context.Context, req app.SupervisorRunOnceRequest) (app.SupervisorRunOnceResponse, error) {
	return s.base.RunSupervisorOnce(ctx, req)
}

type fixtureService struct {
	listReq           app.ListSessionsRequest
	createReq         app.CreateSessionRequest
	resumeReq         app.SessionResumeCandidatesRequest
	detailsReq        app.SessionDetailsRequest
	messagesReq       app.SessionMessagesRequest
	stateReq          app.SessionStateRequest
	workspaceReq      app.SessionWorkspaceRequest
	workspaceSetReq   app.UpdateSessionWorkspaceRequest
	fileListReq       app.WorkspaceFileListRequest
	fileReadReq       app.WorkspaceFileReadRequest
	gitReq            app.GitFileVersionsRequest
	renameReq         app.RenameSessionRequest
	focusReq          app.FocusSessionRequest
	editReq           app.EditSessionRequest
	editCwdGroupReq   app.EditCwdGroupRequest
	modelReq          app.SwitchSessionModelRequest
	deleteReq         app.DeleteSessionRequest
	restartReq        app.RestartSessionRequest
	handoffReq        app.HandoffSessionRequest
	supervisorReq     app.SessionSupervisorRequest
	supervisorEditReq app.UpdateSessionSupervisorRequest
	supervisorRunsReq app.SupervisorRunsRequest
	supervisorOnceReq app.SupervisorRunOnceRequest
}

func (s *fixtureService) Bootstrap(_ context.Context, _ app.BootstrapRequest) app.BootstrapSnapshot {
	return app.BootstrapSnapshot{
		ProtocolVersion: 1,
		Capabilities: app.Capabilities{
			WSRealtime:          true,
			Voice:               false,
			Harness:             false,
			Notifications:       false,
			PIUI:                true,
			WorkspaceRead:       true,
			WorkspaceWrite:      false,
			ExpConnectTransport: true,
		},
		WS: app.WSConfig{
			URL:                 "/api/ws",
			HeartbeatIntervalMS: 15000,
			ResumeBufferEvents:  500,
		},
		LaunchDefaults: app.LaunchConfig{
			DefaultBackend:    "pi",
			AvailableBackends: []string{"pi", "codex"},
			Providers:         []string{},
			Models:            []string{},
		},
		UI:         app.UIConfig{DeferredFeatures: []string{"voice", "harness", "notifications"}},
		RecentCwds: []string{"/root/code/ActRail", "/tmp/project"},
		CwdGroups:  map[string]app.CwdGroupMeta{"/root/code/ActRail": {Label: "ActRail", Collapsed: true}},
	}
}

func (s *fixtureService) ListSessions(_ context.Context, req app.ListSessionsRequest) (app.ListSessionsResponse, error) {
	s.listReq = req
	return app.ListSessionsResponse{
		Items: []app.SessionSummary{{
			SessionID:     "s_123",
			RuntimeID:     "r_123",
			ThreadID:      "t_123",
			AgentBackend:  "pi",
			Title:         "Current task",
			CWD:           "/root/code/ActRail",
			Busy:          true,
			LastUpdatedTS: 1760000000,
			Historical:    false,
		}},
		RemainingCount: 0,
		GroupKey:       nil,
	}, nil
}

func (s *fixtureService) CreateSession(_ context.Context, req app.CreateSessionRequest) (app.CreateSessionResponse, error) {
	s.createReq = req
	return app.CreateSessionResponse{
		OK: true,
		Session: &app.CreatedSession{
			SessionID:    "s_123",
			RuntimeID:    "r_123",
			ThreadID:     "t_123",
			AgentBackend: req.AgentBackend,
			CWD:          req.CWD,
			Busy:         false,
		},
		WSAttach: &app.SessionAttachRequest{
			SessionID:            "s_123",
			SuggestSubscriptions: []string{"session:s_123"},
		},
	}, nil
}

func (s *fixtureService) SessionResumeCandidates(_ context.Context, req app.SessionResumeCandidatesRequest) (app.SessionResumeCandidatesResponse, error) {
	s.resumeReq = req
	return app.SessionResumeCandidatesResponse{
		OK:         true,
		Exists:     true,
		WillCreate: false,
		GitRepo:    false,
		Offset:     req.Offset,
		Limit:      req.Limit,
		Remaining:  0,
		Sessions: []app.SessionResumeCandidate{{
			SessionID:        "s_123",
			Title:            "Current task",
			Alias:            "Current task",
			FirstUserMessage: "Investigate backlog",
			UpdatedTS:        1760000000,
		}},
	}, nil
}

func (s *fixtureService) SessionDetails(_ context.Context, req app.SessionDetailsRequest) (app.SessionDetailsResponse, error) {
	s.detailsReq = req
	return app.SessionDetailsResponse{
		SessionID:      req.SessionID.String(),
		RuntimeID:      "r_123",
		ThreadID:       "t_123",
		Title:          "Current task",
		CWD:            "/root/code/ActRail",
		AgentBackend:   "pi",
		Provider:       "openrouter",
		Model:          "gpt-test",
		Busy:           true,
		QueueLength:    1,
		LastUpdatedTS:  1760000001,
		LastActivityTS: 1760000002,
		Historical:     false,
		Capabilities: app.SessionCapabilitySnapshot{
			WSRealtime:     true,
			Voice:          false,
			Harness:        false,
			Notifications:  false,
			PIUI:           true,
			WorkspaceRead:  true,
			WorkspaceWrite: false,
		},
	}, nil
}

func (s *fixtureService) SessionMessages(_ context.Context, req app.SessionMessagesRequest) (app.SessionMessagesResponse, error) {
	s.messagesReq = req
	next := uint64(100)
	return app.SessionMessagesResponse{
		Items: []app.SessionMessage{{
			Seq:  101,
			Role: "assistant",
			Kind: "message",
			Text: "...",
			TS:   1760000000,
		}},
		NextBeforeSeq: &next,
		HasMore:       true,
		TailSeq:       180,
	}, nil
}

func (s *fixtureService) SessionState(_ context.Context, req app.SessionStateRequest) (app.SessionStateResponse, error) {
	s.stateReq = req
	return app.SessionStateResponse{
		Busy: true,
		Queue: app.SessionQueueSnapshot{Items: []app.QueuedPromptSnapshot{{
			ID:    "q_1",
			Text:  "queued prompt",
			State: "queued",
		}}},
		UIRequest: &app.SessionUIRequestSnapshot{
			RequestID:     "ui_123",
			Kind:          "ask_user",
			Method:        "select",
			Prompt:        "Need input",
			Question:      "Need input",
			AllowFreeform: false,
			Options: []app.SessionUIOptionSnapshot{{
				Label: "Approve",
				Value: "Approve",
			}},
		},
		PartialAssistantTurn: &app.PartialAssistantTurnSnapshot{
			TurnID: "turn_123",
			Text:   "partial",
		},
		TailSeq: 180,
		ResumeCursors: app.SessionResumeCursors{
			Session:   "cur_session",
			UI:        "cur_ui",
			Transport: "cur_transport",
		},
	}, nil
}

func (s *fixtureService) ProbeSessionState(_ context.Context, req app.ProbeSessionStateRequest) (app.ProbeSessionStateResponse, error) {
	state, err := s.SessionState(context.Background(), app.SessionStateRequest{SessionID: req.SessionID})
	if err != nil {
		return app.ProbeSessionStateResponse{}, err
	}
	return app.ProbeSessionStateResponse{ProbeID: "probe_1", State: state}, nil
}

func (s *fixtureService) SessionWorkspace(_ context.Context, req app.SessionWorkspaceRequest) (app.SessionWorkspaceResponse, error) {
	s.workspaceReq = req
	return app.SessionWorkspaceResponse{
		RootPath:     "/root/code/ActRail",
		SelectedPath: "README.md",
		OpenPaths:    []string{"README.md", "go.mod"},
		HistoryItems: []app.WorkspaceHistoryItem{{Path: "README.md", Label: "Current"}},
	}, nil
}

func (s *fixtureService) UpdateSessionWorkspace(_ context.Context, req app.UpdateSessionWorkspaceRequest) (app.SessionWorkspaceResponse, error) {
	s.workspaceSetReq = req
	return app.SessionWorkspaceResponse{
		RootPath:     "/root/code/ActRail",
		SelectedPath: req.SelectedPath,
		OpenPaths:    append([]string(nil), req.OpenPaths...),
		HistoryItems: append([]app.WorkspaceHistoryItem(nil), req.HistoryItems...),
	}, nil
}

func (s *fixtureService) WorkspaceFileList(_ context.Context, req app.WorkspaceFileListRequest) (app.WorkspaceFileListResponse, error) {
	s.fileListReq = req
	return app.WorkspaceFileListResponse{
		RootPath: "/root/code/ActRail",
		Path:     req.Path,
		Items: []app.WorkspaceFileEntry{{
			Path:      "internal/httpapi/router.go",
			Name:      "router.go",
			Kind:      "file",
			SizeBytes: 1024,
		}},
		Truncated: false,
	}, nil
}

func (s *fixtureService) WorkspaceFileRead(_ context.Context, req app.WorkspaceFileReadRequest) (app.WorkspaceFileReadResponse, error) {
	s.fileReadReq = req
	return app.WorkspaceFileReadResponse{
		Path:     req.Path,
		Kind:     "text",
		MIMEType: "text/plain",
		Encoding: "utf-8",
		Text:     "package main",
	}, nil
}

func (s *fixtureService) GitFileVersions(_ context.Context, req app.GitFileVersionsRequest) (app.GitFileVersionsResponse, error) {
	s.gitReq = req
	return app.GitFileVersionsResponse{
		Path: req.Path,
		Items: []app.GitFileVersion{{
			VersionID:  "head",
			Label:      "HEAD",
			CommitHash: "abc1234",
			Author:     "ActRail",
			CommitTS:   1760000000,
			Message:    "Current version",
			Current:    true,
		}},
	}, nil
}

func (s *fixtureService) RenameSession(_ context.Context, req app.RenameSessionRequest) (app.RenameSessionResponse, error) {
	s.renameReq = req
	return app.RenameSessionResponse{OK: true, Alias: req.Name}, nil
}

func (s *fixtureService) FocusSession(_ context.Context, req app.FocusSessionRequest) (app.FocusSessionResponse, error) {
	s.focusReq = req
	return app.FocusSessionResponse{OK: true, Focused: req.Focused}, nil
}

func (s *fixtureService) EditSession(_ context.Context, req app.EditSessionRequest) (app.EditSessionResponse, error) {
	s.editReq = req
	return app.EditSessionResponse{OK: true, Alias: "Edited task", PriorityOffset: 0.5, Focused: true}, nil
}

func (s *fixtureService) EditCwdGroup(_ context.Context, req app.EditCwdGroupRequest) (app.EditCwdGroupResponse, error) {
	s.editCwdGroupReq = req
	return app.EditCwdGroupResponse{OK: true, CWD: req.CWD, Label: "ActRail", Collapsed: true}, nil
}

func (s *fixtureService) SwitchSessionModel(_ context.Context, req app.SwitchSessionModelRequest) (app.SwitchSessionModelResponse, error) {
	s.modelReq = req
	model := ""
	if req.Model.Value != nil {
		model = *req.Model.Value
	}
	provider := ""
	if req.Provider.Value != nil {
		provider = *req.Provider.Value
	}
	return app.SwitchSessionModelResponse{OK: true, Model: model, Provider: provider}, nil
}

func (s *fixtureService) SessionCommands(_ context.Context, req app.SessionCommandsRequest) (app.SessionCommandsResponse, error) {
	return app.SessionCommandsResponse{Commands: []app.SessionCommand{{Name: "rename", Source: "actrail"}}}, nil
}

func (s *fixtureService) ExecuteSessionCommand(_ context.Context, req app.ExecuteSessionCommandRequest) (app.ExecuteSessionCommandResponse, error) {
	return app.ExecuteSessionCommandResponse{OK: true, Command: req.Name, SessionID: req.SessionID.String()}, nil
}

func (s *fixtureService) WaitInbox(_ context.Context) (app.WaitInboxResponse, error) {
	return app.WaitInboxResponse{OK: true}, nil
}

func (s *fixtureService) WaitThreads(_ context.Context, req app.WaitThreadsRequest) (app.WaitThreadsResponse, error) {
	return app.WaitThreadsResponse{OK: true}, nil
}

func (s *fixtureService) WaitThread(_ context.Context, req app.WaitThreadRequest) (app.WaitThreadResponse, error) {
	return app.WaitThreadResponse{OK: true, Thread: app.WaitThreadSummary{ThreadID: req.ThreadID, SessionID: req.SessionID.String()}}, nil
}

func (s *fixtureService) CreateWait(_ context.Context, req app.CreateWaitRequest) (app.WaitLifecycleResponse, error) {
	wait := app.WaitRecord{ActiveWaitSummary: app.ActiveWaitSummary{WaitID: "wait_1", ThreadID: "thread_1", SessionID: req.SessionID.String(), State: app.WaitPendingUnread, Question: req.Question}}
	active := wait.ActiveWaitSummary
	return app.WaitLifecycleResponse{OK: true, Wait: &wait, ActiveWait: &active}, nil
}

func (s *fixtureService) ClaimWait(_ context.Context, req app.WaitLifecycleRequest) (app.WaitLifecycleResponse, error) {
	wait := app.WaitRecord{ActiveWaitSummary: app.ActiveWaitSummary{WaitID: req.WaitID, ThreadID: "thread_1", SessionID: req.SessionID.String(), State: app.WaitClaimed, Question: "question"}}
	active := wait.ActiveWaitSummary
	return app.WaitLifecycleResponse{OK: true, Wait: &wait, ActiveWait: &active}, nil
}

func (s *fixtureService) AnswerWait(_ context.Context, req app.WaitLifecycleRequest) (app.WaitLifecycleResponse, error) {
	wait := app.WaitRecord{ActiveWaitSummary: app.ActiveWaitSummary{WaitID: req.WaitID, ThreadID: "thread_1", SessionID: req.SessionID.String(), State: app.WaitAnswered, Question: "question"}, Answer: req.Answer}
	return app.WaitLifecycleResponse{OK: true, Wait: &wait}, nil
}

func (s *fixtureService) CancelWait(_ context.Context, req app.WaitLifecycleRequest) (app.WaitLifecycleResponse, error) {
	wait := app.WaitRecord{ActiveWaitSummary: app.ActiveWaitSummary{WaitID: req.WaitID, ThreadID: "thread_1", SessionID: req.SessionID.String(), State: app.WaitCancelled, Question: "question"}}
	return app.WaitLifecycleResponse{OK: true, Wait: &wait}, nil
}

func (s *fixtureService) DeleteSession(_ context.Context, req app.DeleteSessionRequest) (app.DeleteSessionResponse, error) {
	s.deleteReq = req
	return app.DeleteSessionResponse{OK: true, SessionID: req.SessionID.String(), Removed: true}, nil
}

func (s *fixtureService) RestartSession(_ context.Context, req app.RestartSessionRequest) (app.RestartSessionResponse, error) {
	s.restartReq = req
	return app.RestartSessionResponse{
		OK:                true,
		SessionID:         req.SessionID.String(),
		RuntimeID:         "r_456",
		PreviousRuntimeID: "r_123",
		Restarted:         true,
	}, nil
}

func (s *fixtureService) HandoffSession(_ context.Context, req app.HandoffSessionRequest) (app.HandoffSessionResponse, error) {
	s.handoffReq = req
	return app.HandoffSessionResponse{}, app.Unsupported("session handoff not implemented")
}

func (s *fixtureService) SupervisorProvider(context.Context, app.SupervisorProviderRequest) (app.SupervisorProviderResponse, error) {
	return app.SupervisorProviderResponse{OK: true, BaseURL: "https://llm.invalid/v1", Model: "test-model", APIKeyConfigured: true, Complete: true}, nil
}

func (s *fixtureService) UpdateSupervisorProvider(_ context.Context, req app.UpdateSupervisorProviderRequest) (app.SupervisorProviderResponse, error) {
	return app.SupervisorProviderResponse{OK: true, BaseURL: req.BaseURL, Model: req.Model, APIKeyConfigured: req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "", Complete: true}, nil
}

func (s *fixtureService) SessionSupervisor(_ context.Context, req app.SessionSupervisorRequest) (app.SessionSupervisorResponse, error) {
	s.supervisorReq = req
	return app.SessionSupervisorResponse{OK: true, Supported: true, Enabled: false, Status: "idle", IdleAfterMinutes: 5, MaxConsecutiveInjections: 10, ContextFiles: []string{}}, nil
}

func (s *fixtureService) UpdateSessionSupervisor(_ context.Context, req app.UpdateSessionSupervisorRequest) (app.SessionSupervisorResponse, error) {
	s.supervisorEditReq = req
	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return app.SessionSupervisorResponse{OK: true, Supported: true, Enabled: enabled, Status: "idle", IdleAfterMinutes: 5, MaxConsecutiveInjections: 10, ContextFiles: []string{}}, nil
}

func (s *fixtureService) SupervisorRuns(_ context.Context, req app.SupervisorRunsRequest) (app.SupervisorRunsResponse, error) {
	s.supervisorRunsReq = req
	return app.SupervisorRunsResponse{OK: true, Runs: []app.SupervisorRunSummary{{RunID: "supervisor_1", AnchorAssistantEventID: "pi:message:a1", Status: "stop", Reason: "complete"}}}, nil
}

func (s *fixtureService) RunSupervisorOnce(_ context.Context, req app.SupervisorRunOnceRequest) (app.SupervisorRunOnceResponse, error) {
	s.supervisorOnceReq = req
	return app.SupervisorRunOnceResponse{OK: true, Run: app.SupervisorRunSummary{RunID: "supervisor_2", AnchorAssistantEventID: "pi:message:a2", Status: "stop", Reason: "dry run"}}, nil
}

func newTestRouter(cfg config.Config, svc app.Service) http.Handler {
	return New(cfg, svc, ws.NewHandler(cfg))
}

func TestSupervisorRoutesReturnProviderAndSessionConfig(t *testing.T) {
	svc := &fixtureService{}
	h := newTestRouter(config.Load(), svc)

	providerReq := httptest.NewRequest(http.MethodGet, "/api/supervisor/provider", nil)
	providerRes := httptest.NewRecorder()
	h.ServeHTTP(providerRes, providerReq)
	if providerRes.Code != http.StatusOK {
		t.Fatalf("GET provider status = %d, want %d", providerRes.Code, http.StatusOK)
	}
	var providerBody map[string]any
	if err := json.Unmarshal(providerRes.Body.Bytes(), &providerBody); err != nil {
		t.Fatalf("decode provider response: %v", err)
	}
	if _, ok := providerBody["api_key"]; ok {
		t.Fatalf("provider response leaked api_key: %s", providerRes.Body.String())
	}
	if providerBody["api_key_configured"] != true || providerBody["model"] != "test-model" {
		t.Fatalf("provider response = %v", providerBody)
	}

	body := strings.NewReader(`{"enabled":true,"idle_after_minutes":2,"max_consecutive_injections":12,"goal":"finish","acceptance_criteria":"tests","context_files":["README.md"]}`)
	configReq := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/supervisor", body)
	configReq.Header.Set("Content-Type", "application/json")
	configRes := httptest.NewRecorder()
	h.ServeHTTP(configRes, configReq)
	if configRes.Code != http.StatusOK {
		t.Fatalf("POST session supervisor status = %d, want %d body=%s", configRes.Code, http.StatusOK, configRes.Body.String())
	}
	if svc.supervisorEditReq.SessionID.String() != "s_123" || svc.supervisorEditReq.Enabled == nil || *svc.supervisorEditReq.Enabled != true || svc.supervisorEditReq.IdleAfterMinutes == nil || *svc.supervisorEditReq.IdleAfterMinutes != 2 {
		t.Fatalf("supervisor edit req = %+v", svc.supervisorEditReq)
	}

	runsReq := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/supervisor/runs?limit=5", nil)
	runsRes := httptest.NewRecorder()
	h.ServeHTTP(runsRes, runsReq)
	if runsRes.Code != http.StatusOK || svc.supervisorRunsReq.SessionID.String() != "s_123" || svc.supervisorRunsReq.Limit != 5 {
		t.Fatalf("GET runs status=%d req=%+v body=%s", runsRes.Code, svc.supervisorRunsReq, runsRes.Body.String())
	}

	runOnceReq := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/supervisor/run-once", strings.NewReader(`{"dry_run":true}`))
	runOnceReq.Header.Set("Content-Type", "application/json")
	runOnceRes := httptest.NewRecorder()
	h.ServeHTTP(runOnceRes, runOnceReq)
	if runOnceRes.Code != http.StatusOK || svc.supervisorOnceReq.SessionID.String() != "s_123" || !svc.supervisorOnceReq.DryRun {
		t.Fatalf("POST run-once status=%d req=%+v body=%s", runOnceRes.Code, svc.supervisorOnceReq, runOnceRes.Body.String())
	}
}

func TestBootstrapRoute(t *testing.T) {
	h := newTestRouter(config.Load(), &fixtureService{})
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}

	var body struct {
		ProtocolVersion int `json:"protocol_version"`
		WS              struct {
			URL string `json:"url"`
		} `json:"ws"`
		RecentCwds []string                    `json:"recent_cwds"`
		CwdGroups  map[string]app.CwdGroupMeta `json:"cwd_groups"`
	}
	decodeJSON(t, res, &body)
	if body.ProtocolVersion != 1 {
		t.Fatalf("expected protocol version 1, got %d", body.ProtocolVersion)
	}
	if body.WS.URL != "/api/ws" {
		t.Fatalf("expected websocket path /api/ws, got %q", body.WS.URL)
	}
	if len(body.RecentCwds) != 2 || body.RecentCwds[0] != "/root/code/ActRail" {
		t.Fatalf("bootstrap recent_cwds = %#v", body.RecentCwds)
	}
	if meta, ok := body.CwdGroups["/root/code/ActRail"]; !ok || meta.Label != "ActRail" || !meta.Collapsed {
		t.Fatalf("bootstrap cwd_groups = %#v", body.CwdGroups)
	}
}

func TestSnapshotRoutesReturnContractShapes(t *testing.T) {
	svc := &fixtureService{}
	h := newTestRouter(config.Load(), svc)

	t.Run("list sessions", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions?offset=2&limit=10&group_offset=1&group_limit=5", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}

		var body app.ListSessionsResponse
		decodeJSON(t, res, &body)
		if len(body.Items) != 1 || body.Items[0].SessionID != "s_123" {
			t.Fatalf("unexpected session payload: %+v", body.Items)
		}
		if svc.listReq.Offset != 2 || svc.listReq.Limit != 10 || svc.listReq.GroupOffset != 1 || svc.listReq.GroupLimit != 5 {
			t.Fatalf("unexpected list request: %+v", svc.listReq)
		}
	})

	t.Run("create session", func(t *testing.T) {
		body := bytes.NewBufferString(`{"agent_backend":"pi","cwd":"/root/code/ActRail"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}

		var payload app.CreateSessionResponse
		decodeJSON(t, res, &payload)
		if !payload.OK || payload.Session == nil || payload.WSAttach == nil {
			t.Fatalf("unexpected create payload: %+v", payload)
		}
		if svc.createReq.AgentBackend != "pi" || svc.createReq.CWD != "/root/code/ActRail" {
			t.Fatalf("unexpected create request: %+v", svc.createReq)
		}
	})

	t.Run("session details", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/details", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionDetailsResponse
		decodeJSON(t, res, &payload)
		if payload.SessionID != "s_123" || payload.AgentBackend != "pi" {
			t.Fatalf("unexpected details payload: %+v", payload)
		}
	})

	t.Run("session messages", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/messages?after_seq=100&before_seq=120&limit=20&init=true", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionMessagesResponse
		decodeJSON(t, res, &payload)
		if len(payload.Items) != 1 || payload.Items[0].Seq != 101 || payload.TailSeq != 180 {
			t.Fatalf("unexpected messages payload: %+v", payload)
		}
		if svc.messagesReq.AfterSeq == nil || *svc.messagesReq.AfterSeq != 100 || svc.messagesReq.BeforeSeq == nil || *svc.messagesReq.BeforeSeq != 120 || svc.messagesReq.Limit != 20 || !svc.messagesReq.Init {
			t.Fatalf("unexpected messages request: %+v", svc.messagesReq)
		}
	})

	t.Run("session state", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/state", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionStateResponse
		decodeJSON(t, res, &payload)
		if !payload.Busy || len(payload.Queue.Items) != 1 || payload.ResumeCursors.Transport != "cur_transport" {
			t.Fatalf("unexpected state payload: %+v", payload)
		}
		if payload.UIRequest == nil || payload.UIRequest.Method != "select" || len(payload.UIRequest.Options) != 1 || payload.UIRequest.Options[0].Label != "Approve" {
			t.Fatalf("unexpected ui request snapshot: %+v", payload.UIRequest)
		}
	})

	t.Run("session workspace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/workspace", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionWorkspaceResponse
		decodeJSON(t, res, &payload)
		if payload.RootPath != "/root/code/ActRail" || len(payload.OpenPaths) != 2 {
			t.Fatalf("unexpected workspace payload: %+v", payload)
		}
	})

	t.Run("update workspace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/workspace", bytes.NewBufferString(`{"selected_path":"internal/httpapi/router.go","open_paths":["internal","internal/httpapi/router.go"],"history_items":[{"path":"README.md","label":"Readme"}]}`))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionWorkspaceResponse
		decodeJSON(t, res, &payload)
		if payload.SelectedPath != "internal/httpapi/router.go" || len(payload.OpenPaths) != 2 {
			t.Fatalf("unexpected update workspace payload: %+v", payload)
		}
		if svc.workspaceSetReq.SessionID.String() != "s_123" || svc.workspaceSetReq.SelectedPath != "internal/httpapi/router.go" {
			t.Fatalf("unexpected update workspace request: %+v", svc.workspaceSetReq)
		}
	})

	t.Run("workspace file list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/file/list?path=internal/httpapi&search=router&limit=25", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.WorkspaceFileListResponse
		decodeJSON(t, res, &payload)
		if payload.Path != "internal/httpapi" || len(payload.Items) != 1 {
			t.Fatalf("unexpected file list payload: %+v", payload)
		}
		if svc.fileListReq.Path != "internal/httpapi" || svc.fileListReq.Search != "router" || svc.fileListReq.Limit != 25 {
			t.Fatalf("unexpected file list request: %+v", svc.fileListReq)
		}
	})

	t.Run("workspace file read", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/file/read?path=./internal/httpapi/../httpapi/router.go", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.WorkspaceFileReadResponse
		decodeJSON(t, res, &payload)
		if payload.Path != "internal/httpapi/router.go" || payload.Kind != "text" {
			t.Fatalf("unexpected file read payload: %+v", payload)
		}
		if svc.fileReadReq.Path != "internal/httpapi/router.go" {
			t.Fatalf("expected normalized path, got %+v", svc.fileReadReq)
		}
	})

	t.Run("git file versions", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/git/file_versions?path=go.mod", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.GitFileVersionsResponse
		decodeJSON(t, res, &payload)
		if payload.Path != "go.mod" || len(payload.Items) != 1 || payload.Items[0].Label != "HEAD" {
			t.Fatalf("unexpected git versions payload: %+v", payload)
		}
		if svc.gitReq.Path != "go.mod" {
			t.Fatalf("unexpected git request: %+v", svc.gitReq)
		}
	})
}

func TestRouteValidationReturnsErrorEnvelope(t *testing.T) {
	h := newTestRouter(config.Load(), app.NewStubForTest(config.Load(), time.Now, app.RuntimeConfig{}))

	cases := []struct {
		name   string
		method string
		target string
		body   string
		status int
		code   string
		field  string
	}{
		{name: "list sessions offset not integer", method: http.MethodGet, target: "/api/sessions?offset=nope", status: http.StatusBadRequest, code: "invalid_request", field: "offset"},
		{name: "list sessions limit negative", method: http.MethodGet, target: "/api/sessions?limit=-1", status: http.StatusBadRequest, code: "invalid_request", field: "limit"},
		{name: "list sessions group offset not integer", method: http.MethodGet, target: "/api/sessions?group_offset=x", status: http.StatusBadRequest, code: "invalid_request", field: "group_offset"},
		{name: "list sessions group limit negative", method: http.MethodGet, target: "/api/sessions?group_limit=-2", status: http.StatusBadRequest, code: "invalid_request", field: "group_limit"},
		{name: "create session missing cwd", method: http.MethodPost, target: "/api/sessions", body: `{"agent_backend":"pi"}`, status: http.StatusBadRequest, code: "invalid_request", field: "cwd"},
		{name: "details invalid session id", method: http.MethodGet, target: "/api/sessions/%20/details", status: http.StatusBadRequest, code: "invalid_request", field: "session_id"},
		{name: "messages invalid after seq", method: http.MethodGet, target: "/api/sessions/s_123/messages?after_seq=nope", status: http.StatusBadRequest, code: "invalid_request", field: "after_seq"},
		{name: "messages invalid before seq", method: http.MethodGet, target: "/api/sessions/s_123/messages?before_seq=nope", status: http.StatusBadRequest, code: "invalid_request", field: "before_seq"},
		{name: "messages invalid init", method: http.MethodGet, target: "/api/sessions/s_123/messages?init=nope", status: http.StatusBadRequest, code: "invalid_request", field: "init"},
		{name: "file read missing path", method: http.MethodGet, target: "/api/sessions/s_123/file/read", status: http.StatusBadRequest, code: "invalid_request", field: "path"},
		{name: "file read absolute path", method: http.MethodGet, target: "/api/sessions/s_123/file/read?path=/etc/passwd", status: http.StatusBadRequest, code: "invalid_request", field: "path"},
		{name: "git file versions path escape", method: http.MethodGet, target: "/api/sessions/s_123/git/file_versions?path=../go.mod", status: http.StatusBadRequest, code: "invalid_request", field: "path"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = bytes.NewBuffer(nil)
			}
			req := httptest.NewRequest(tc.method, tc.target, body)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			assertErrorEnvelope(t, res, tc.status, tc.code, tc.field)
		})
	}
}

func TestRouteHelpersAcceptHistoricalSessionIDs(t *testing.T) {
	historical, err := session.NewHistoricalIdentity("s_123", "pi")
	if err != nil {
		t.Fatalf("create historical identity: %v", err)
	}
	svc := &fixtureService{}
	h := newTestRouter(config.Load(), svc)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+historical.HTTPRouteKey()+"/details", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}
	if svc.detailsReq.SessionID.String() != historical.HTTPRouteKey() {
		t.Fatalf("expected historical session id %q, got %q", historical.HTTPRouteKey(), svc.detailsReq.SessionID)
	}
}

func TestMeRequiresValidCookieValue(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Auth.CookieName, Value: "wrong"})
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}
	var body authStatus
	decodeJSON(t, res, &body)
	if body.OK {
		t.Fatal("GET /api/me returned ok=true for invalid cookie value")
	}
}

func TestMeReportsTrueForConfiguredValidCookie(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	cookie, err := authn.SessionCookie(cfg.Auth)
	if err != nil {
		t.Fatalf("SessionCookie() error = %v", err)
	}
	req.AddCookie(cookie)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	var body authStatus
	decodeJSON(t, res, &body)
	if !body.OK {
		t.Fatal("GET /api/me returned ok=false for valid cookie")
	}
}

func TestMeReportsTrueInLocalNoAuthMode(t *testing.T) {
	cfg := config.Load()
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	var body authStatus
	decodeJSON(t, res, &body)
	if !body.OK {
		t.Fatal("GET /api/me returned ok=false in local no-auth mode")
	}
}

func TestProtectedRoutesRequireAuthCookieInPasswordMode(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))

	tests := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "bootstrap", method: http.MethodGet, target: "/api/bootstrap"},
		{name: "list sessions", method: http.MethodGet, target: "/api/sessions"},
		{name: "create session", method: http.MethodPost, target: "/api/sessions"},
		{name: "resume candidates", method: http.MethodGet, target: "/api/session_resume_candidates"},
		{name: "session details", method: http.MethodGet, target: "/api/sessions/s_123/details"},
		{name: "session messages", method: http.MethodGet, target: "/api/sessions/s_123/messages"},
		{name: "session state", method: http.MethodGet, target: "/api/sessions/s_123/state"},
		{name: "session workspace", method: http.MethodGet, target: "/api/sessions/s_123/workspace"},
		{name: "file list", method: http.MethodGet, target: "/api/sessions/s_123/file/list"},
		{name: "file read", method: http.MethodGet, target: "/api/sessions/s_123/file/read?path=README.md"},
		{name: "git file versions", method: http.MethodGet, target: "/api/sessions/s_123/git/file_versions?path=README.md"},
		{name: "rename", method: http.MethodPost, target: "/api/sessions/s_123/rename"},
		{name: "focus", method: http.MethodPost, target: "/api/sessions/s_123/focus"},
		{name: "edit", method: http.MethodPost, target: "/api/sessions/s_123/edit"},
		{name: "model", method: http.MethodPost, target: "/api/sessions/s_123/model"},
		{name: "delete", method: http.MethodPost, target: "/api/sessions/s_123/delete"},
		{name: "restart", method: http.MethodPost, target: "/api/sessions/s_123/restart"},
		{name: "handoff", method: http.MethodPost, target: "/api/sessions/s_123/handoff"},
		{name: "websocket", method: http.MethodGet, target: "/api/ws"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, tc.target, body)
			res := httptest.NewRecorder()

			h.ServeHTTP(res, req)

			assertErrorEnvelope(t, res, http.StatusUnauthorized, "unauthorized", "")
		})
	}
}

func TestLoginRejectsInvalidPasswordWithSharedErrorEnvelope(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"wrong"}`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, res.Code)
	}
	var body errorEnvelope
	decodeJSON(t, res, &body)
	if body.Error.Code != "unauthorized" {
		t.Fatalf("error code = %q, want %q", body.Error.Code, "unauthorized")
	}
	if body.Error.Message != "invalid password" {
		t.Fatalf("error message = %q, want %q", body.Error.Message, "invalid password")
	}
}

func TestLoginReturnsUnsupportedInLocalNoAuthMode(t *testing.T) {
	cfg := config.Load()
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"secret"}`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	assertErrorEnvelope(t, res, http.StatusNotImplemented, "unsupported", "")
}

func TestLoginRejectsInvalidJSONWithSharedErrorEnvelope(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password"`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, res.Code)
	}
	var body errorEnvelope
	decodeJSON(t, res, &body)
	if body.Error.Code != "invalid_request" {
		t.Fatalf("error code = %q, want %q", body.Error.Code, "invalid_request")
	}
}

func TestLoginSetsAuthCookieOnSuccess(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"secret"}`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	token, err := authn.SessionToken(cfg.Auth)
	if err != nil {
		t.Fatalf("SessionToken() error = %v", err)
	}
	if cookies[0].Value != token {
		t.Fatalf("cookie value = %q, want issued auth token", cookies[0].Value)
	}
}

func TestWriteAppErrorMapsTransportResetRequiredToConflict(t *testing.T) {
	cfg := config.Load()
	svc := newServiceStub(cfg)
	svc.listSessionsFunc = func(context.Context, app.ListSessionsRequest) (app.ListSessionsResponse, error) {
		return app.ListSessionsResponse{}, &app.Error{Code: "transport_reset_required", Message: "resume cursor expired", Field: "resume_from"}
	}
	h := newTestRouter(cfg, svc)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, res.Code)
	}
	var body errorEnvelope
	decodeJSON(t, res, &body)
	if body.Error.Code != "transport_reset_required" {
		t.Fatalf("error code = %q, want %q", body.Error.Code, "transport_reset_required")
	}
}

func TestSessionActionRoutesUseAppSeams(t *testing.T) {
	svc := &fixtureService{}
	h := newTestRouter(config.Load(), svc)

	t.Run("resume candidates", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/session_resume_candidates?cwd=/root/code/ActRail&backend=pi&offset=1&limit=5", nil)
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}
		if svc.resumeReq.CWD != "/root/code/ActRail" || svc.resumeReq.AgentBackend != "pi" || svc.resumeReq.Offset != 1 || svc.resumeReq.Limit != 5 {
			t.Fatalf("resume request = %+v", svc.resumeReq)
		}
		var body app.SessionResumeCandidatesResponse
		decodeJSON(t, res, &body)
		if !body.OK || len(body.Sessions) != 1 {
			t.Fatalf("resume response = %+v", body)
		}
	})

	t.Run("rename", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/rename", bytes.NewBufferString(`{"name":"New title"}`))
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}
		if svc.renameReq.Name != "New title" || svc.renameReq.SessionID.String() != "s_123" {
			t.Fatalf("rename request = %+v", svc.renameReq)
		}
	})

	t.Run("focus", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/focus", bytes.NewBufferString(`{"focused":true}`))
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}
		if !svc.focusReq.Focused || svc.focusReq.SessionID.String() != "s_123" {
			t.Fatalf("focus request = %+v", svc.focusReq)
		}
	})

	t.Run("edit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/edit", bytes.NewBufferString(`{"name":"Edited task","priority_offset":0.5,"snooze_until":1760000100,"dependency_session_id":"s_456"}`))
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}
		if svc.editReq.SessionID.String() != "s_123" || svc.editReq.Name.Value == nil || *svc.editReq.Name.Value != "Edited task" {
			t.Fatalf("edit request = %+v", svc.editReq)
		}
		if svc.editReq.PriorityOffset.Value == nil || *svc.editReq.PriorityOffset.Value != 0.5 {
			t.Fatalf("edit priority request = %+v", svc.editReq.PriorityOffset)
		}
		if svc.editReq.SnoozeUntil.Value == nil || *svc.editReq.SnoozeUntil.Value != 1760000100 {
			t.Fatalf("edit snooze request = %+v", svc.editReq.SnoozeUntil)
		}
		if svc.editReq.DependencySessionID.Value == nil || *svc.editReq.DependencySessionID.Value != "s_456" {
			t.Fatalf("edit dependency request = %+v", svc.editReq.DependencySessionID)
		}
	})

	t.Run("cwd group", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/cwd_groups/edit", bytes.NewBufferString(`{"cwd":"/root/code/ActRail","label":"ActRail","collapsed":true}`))
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}
		if svc.editCwdGroupReq.CWD != "/root/code/ActRail" || svc.editCwdGroupReq.Label == nil || *svc.editCwdGroupReq.Label != "ActRail" {
			t.Fatalf("cwd group request = %+v", svc.editCwdGroupReq)
		}
		if svc.editCwdGroupReq.Collapsed == nil || !*svc.editCwdGroupReq.Collapsed {
			t.Fatalf("cwd group collapsed request = %+v", svc.editCwdGroupReq)
		}
	})

	t.Run("model", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/model", bytes.NewBufferString(`{"model":"gpt-next","provider":"openrouter"}`))
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}
		if svc.modelReq.Model.Value == nil || *svc.modelReq.Model.Value != "gpt-next" {
			t.Fatalf("model request = %+v", svc.modelReq)
		}
		if svc.modelReq.Provider.Value == nil || *svc.modelReq.Provider.Value != "openrouter" {
			t.Fatalf("provider request = %+v", svc.modelReq)
		}
	})

	t.Run("delete", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/delete", bytes.NewBufferString(`{}`))
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}
		if svc.deleteReq.SessionID.String() != "s_123" {
			t.Fatalf("delete request = %+v", svc.deleteReq)
		}
	})

	t.Run("restart", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/restart", bytes.NewBufferString(`{}`))
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}
		var body app.RestartSessionResponse
		decodeJSON(t, res, &body)
		if !body.OK || !body.Restarted || body.SessionID != "s_123" || body.RuntimeID != "r_456" || body.PreviousRuntimeID != "r_123" {
			t.Fatalf("unexpected restart payload: %+v", body)
		}
		if svc.restartReq.SessionID.String() != "s_123" {
			t.Fatalf("restart request = %+v", svc.restartReq)
		}
	})

	t.Run("handoff unsupported", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/handoff", bytes.NewBufferString(`{}`))
		res := httptest.NewRecorder()

		h.ServeHTTP(res, req)

		assertErrorEnvelope(t, res, http.StatusNotImplemented, "unsupported", "")
		if svc.handoffReq.SessionID.String() != "s_123" {
			t.Fatalf("handoff request = %+v", svc.handoffReq)
		}
	})
}

func TestSessionActionRoutesSurfaceNotFound(t *testing.T) {
	cfg := config.Load()
	svc := newServiceStub(cfg)
	svc.renameFunc = func(context.Context, app.RenameSessionRequest) (app.RenameSessionResponse, error) {
		return app.RenameSessionResponse{}, app.NotFound("session \"s_404\" not found")
	}
	h := newTestRouter(cfg, svc)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_404/rename", bytes.NewBufferString(`{"name":"Missing"}`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	assertErrorEnvelope(t, res, http.StatusNotFound, "not_found", "")
}

func decodeJSON(t *testing.T, res *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(res.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func assertErrorEnvelope(t *testing.T, res *httptest.ResponseRecorder, status int, code, field string) {
	t.Helper()
	if res.Code != status {
		t.Fatalf("expected status %d, got %d", status, res.Code)
	}
	var body struct {
		OK    bool `json:"ok"`
		Error struct {
			Code  string `json:"code"`
			Field string `json:"field"`
		} `json:"error"`
	}
	decodeJSON(t, res, &body)
	if body.OK {
		t.Fatalf("expected ok=false, got true")
	}
	if body.Error.Code != code {
		t.Fatalf("expected error code %q, got %q", code, body.Error.Code)
	}
	if body.Error.Field != field {
		t.Fatalf("expected error field %q, got %q", field, body.Error.Field)
	}
}
