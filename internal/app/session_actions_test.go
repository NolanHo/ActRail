package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	piagentv1 "actrail/proto/pi/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func newSessionActionFixture(t *testing.T) (*Stub, *process.FakeHandle, session.SessionID, session.SessionID) {
	t.Helper()
	svc, handles, sessionID, runtimeID := newSessionActionFixtureForBackend(t, "pi")
	return svc, (*handles)[0], sessionID, runtimeID
}

func newSessionActionFixtureForBackend(t *testing.T, backend string) (*Stub, *[]*process.FakeHandle, session.SessionID, session.SessionID) {
	t.Helper()
	handles := &[]*process.FakeHandle{}
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		handle := process.NewFakeHandle(spec)
		handle.SetPID(321 + len(*handles))
		handle.SetPTY(&fakePTY{})
		*handles = append(*handles, handle)
		return handle
	}}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	cwd := t.TempDir()
	req := CreateSessionRequest{AgentBackend: backend, CWD: cwd}
	if strings.EqualFold(backend, "pi") {
		req.PIAgentGRPC = boolPtr(false)
	}
	created, err := svc.CreateSession(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(session) error = %v", err)
	}
	runtimeID, err := session.ParseSessionID(created.Session.RuntimeID)
	if err != nil {
		t.Fatalf("ParseSessionID(runtime) error = %v", err)
	}
	return svc, handles, sessionID, runtimeID
}

func TestStubSessionActionsMutateMetadataAndDelete(t *testing.T) {
	svc, handle, sessionID, _ := newSessionActionFixture(t)
	depCreated, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession(dependency) error = %v", err)
	}
	depID, err := session.ParseSessionID(depCreated.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(dependency) error = %v", err)
	}

	renamed, err := svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: sessionID, Name: "Renamed task"})
	if err != nil {
		t.Fatalf("RenameSession() error = %v", err)
	}
	if !renamed.OK || renamed.Alias != "Renamed task" {
		t.Fatalf("RenameSession() = %+v", renamed)
	}
	deletedName := "Deleted duplicate"
	deletedCreated, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir(), Title: &deletedName})
	if err != nil {
		t.Fatalf("CreateSession(deleted duplicate) error = %v", err)
	}
	deletedID, err := session.ParseSessionID(deletedCreated.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(deleted duplicate) error = %v", err)
	}
	if _, err := svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: deletedID}); err != nil {
		t.Fatalf("DeleteSession(deleted duplicate) error = %v", err)
	}
	if _, err := svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: sessionID, Name: deletedName}); err != nil {
		t.Fatalf("RenameSession(deleted duplicate name) error = %v", err)
	}
	if _, err := svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: sessionID, Name: "Renamed task"}); err != nil {
		t.Fatalf("RenameSession(restore name) error = %v", err)
	}

	focused, err := svc.FocusSession(context.Background(), FocusSessionRequest{SessionID: sessionID, Focused: true})
	if err != nil {
		t.Fatalf("FocusSession() error = %v", err)
	}
	if !focused.OK || !focused.Focused {
		t.Fatalf("FocusSession() = %+v", focused)
	}

	priority := 0.5
	snoozeUntil := int64(1760000100)
	dependency := depID.String()
	edited, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:           sessionID,
		PriorityOffset:      Float64Patch{Present: true, Value: &priority},
		SnoozeUntil:         Int64Patch{Present: true, Value: &snoozeUntil},
		DependencySessionID: StringPatch{Present: true, Value: &dependency},
	})
	if err != nil {
		t.Fatalf("EditSession() error = %v", err)
	}
	if !edited.OK || edited.PriorityOffset != priority || edited.SnoozeUntil == nil || *edited.SnoozeUntil != snoozeUntil {
		t.Fatalf("EditSession() = %+v", edited)
	}
	if edited.DependencySessionID == nil || *edited.DependencySessionID != depID.String() {
		t.Fatalf("EditSession().DependencySessionID = %+v, want %q", edited.DependencySessionID, depID)
	}

	model := "gpt-next"
	provider := "openrouter"
	switched, err := svc.SwitchSessionModel(context.Background(), SwitchSessionModelRequest{
		SessionID: sessionID,
		Model:     StringPatch{Present: true, Value: &model},
		Provider:  StringPatch{Present: true, Value: &provider},
	})
	if err != nil {
		t.Fatalf("SwitchSessionModel() error = %v", err)
	}
	if !switched.OK || switched.Model != model || switched.Provider != provider {
		t.Fatalf("SwitchSessionModel() = %+v", switched)
	}

	details, err := svc.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	if details.Alias != "Renamed task" || !details.Focused || details.PriorityOffset != priority || details.Model != model || details.Provider != provider {
		t.Fatalf("SessionDetails() = %+v", details)
	}
	if details.DependencySessionID != depID.String() {
		t.Fatalf("SessionDetails().DependencySessionID = %q, want %q", details.DependencySessionID, depID)
	}
	if strings.TrimSpace(details.SessionFilePath) == "" {
		t.Fatalf("SessionDetails().SessionFilePath is empty")
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("len(ListSessions().Items) = %d, want 2", len(listed.Items))
	}
	var listedRecord *SessionSummary
	for i := range listed.Items {
		if listed.Items[i].SessionID == sessionID.String() {
			listedRecord = &listed.Items[i]
			break
		}
	}
	if listedRecord == nil {
		t.Fatalf("ListSessions() missing session %q in %+v", sessionID, listed.Items)
	}
	if listedRecord.Alias != "Renamed task" || !listedRecord.Focused || listedRecord.Model != model {
		t.Fatalf("ListSessions() record = %+v", *listedRecord)
	}
	if listedRecord.SessionFilePath != details.SessionFilePath {
		t.Fatalf("ListSessions().SessionFilePath = %q, want %q", listedRecord.SessionFilePath, details.SessionFilePath)
	}

	deleted, err := svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if !deleted.OK || !deleted.Removed || deleted.SessionID != sessionID.String() {
		t.Fatalf("DeleteSession() = %+v", deleted)
	}
	if handle.KillCalls() != 1 {
		t.Fatalf("handle.KillCalls() = %d, want 1", handle.KillCalls())
	}
	listed, err = svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() after delete error = %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].SessionID != depID.String() {
		t.Fatalf("ListSessions() after delete = %+v", listed.Items)
	}
}

func TestStubCreateSessionCanResumeListedPISessionCandidate(t *testing.T) {
	svc, handles, sessionID, _ := newSessionActionFixtureForBackend(t, "pi")
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	resumeID := sessionID.String()
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend:    "pi",
		PIAgentGRPC:     boolPtr(false),
		CWD:             record.cwd,
		ResumeSessionID: &resumeID,
	})
	if err != nil {
		t.Fatalf("CreateSession(resume) error = %v", err)
	}
	if created.Session.SessionID == resumeID {
		t.Fatalf("CreateSession(resume).SessionID = %q, want new ActRail slot", created.Session.SessionID)
	}
	if len(*handles) != 2 {
		t.Fatalf("len(handles) = %d, want 2", len(*handles))
	}
	args := (*handles)[1].Spec().Command().Args()
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--session" && args[i+1] == record.importedSourcePath {
			found = true
		}
	}
	if !found {
		t.Fatalf("resume launch args = %#v, want --session %q", args, record.importedSourcePath)
	}
}

func TestStubSessionResumeCandidatesAndRuntimeRouteLookup(t *testing.T) {
	svc, _, sessionID, runtimeRoute := newSessionActionFixture(t)
	otherDir := t.TempDir()
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: otherDir}); err != nil {
		t.Fatalf("CreateSession(other) error = %v", err)
	}
	if _, ok, err := svc.registry.AppendMessage(sessionID, "user", "message", "Investigate backlog"); err != nil || !ok {
		t.Fatalf("AppendMessage() = (_, %v, %v), want ok=true err=nil", ok, err)
	}

	renamed, err := svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: runtimeRoute, Name: "Runtime route rename"})
	if err != nil {
		t.Fatalf("RenameSession(runtime route) error = %v", err)
	}
	if renamed.Alias != "Runtime route rename" {
		t.Fatalf("RenameSession(runtime route) = %+v", renamed)
	}

	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          record.cwd,
		AgentBackend: "PI",
		Offset:       0,
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	if !resume.OK || !resume.Exists || resume.Remaining != 0 || len(resume.Sessions) != 1 {
		t.Fatalf("SessionResumeCandidates() = %+v", resume)
	}
	candidate := resume.Sessions[0]
	if candidate.SessionID != sessionID.String() || candidate.Alias != "Runtime route rename" || candidate.FirstUserMessage != "Investigate backlog" {
		t.Fatalf("resume candidate = %+v", candidate)
	}
}

func TestPIResumeCandidateFromSourcePathPrefersSessionInfoName(t *testing.T) {
	cwd := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "pi-named.jsonl")
	body := `{"type":"session","version":3,"id":"pi-named","cwd":` + fmt.Sprintf("%q", cwd) + `}
{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"Investigate a long prompt that should not be the display label"}]}}
{"type":"session_info","name":"ActRail draft"}
{"type":"session_info","name":"ActRail"}
`
	if err := os.WriteFile(sourcePath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", sourcePath, err)
	}

	candidate, ok := piResumeCandidateFromSourcePath(cwd, sourcePath)
	if !ok {
		t.Fatal("piResumeCandidateFromSourcePath() ok = false, want true")
	}
	if candidate.DisplayName != "ActRail" || candidate.Title != "ActRail" || candidate.FirstUserMessage != "Investigate a long prompt that should not be the display label" {
		t.Fatalf("resume candidate = %+v, want session_info name before first user text", candidate)
	}
}

func TestPIResumeCandidateFromSourcePathReadsCommonMessageShapes(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "content parts",
			body: `{"type":"session","version":3,"id":"pi-content","cwd":` + fmt.Sprintf("%q", cwd) + `}
{"type":"message","message":{"role":"user","content":[{"type":"input_text","text":"alpha"},{"type":"text","text":" beta"}]}}
`,
			want: "alpha beta",
		},
		{
			name: "text fallback",
			body: `{"type":"session","version":3,"id":"pi-text","cwd":` + fmt.Sprintf("%q", cwd) + `}
{"type":"message","message":{"role":"user","text":"fallback text"}}
`,
			want: "fallback text",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourcePath := filepath.Join(t.TempDir(), "pi.jsonl")
			if err := os.WriteFile(sourcePath, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("WriteFile(%q) error = %v", sourcePath, err)
			}
			candidate, ok := piResumeCandidateFromSourcePath(cwd, sourcePath)
			if !ok {
				t.Fatal("piResumeCandidateFromSourcePath() ok = false, want true")
			}
			if candidate.FirstUserMessage != tc.want {
				t.Fatalf("FirstUserMessage = %q, want %q", candidate.FirstUserMessage, tc.want)
			}
		})
	}
}

func TestStubSessionResumeCandidatesUseUpdatedTimeDescendingForDemotedSessions(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	cwd := t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: cwd}); err != nil {
			t.Fatalf("CreateSession(%d) error = %v", i, err)
		}
	}
	blockedPriority := 1.0
	blockedDependency := "s_1"
	now = now.Add(time.Minute)
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:           mustSessionID(t, "s_2"),
		PriorityOffset:      Float64Patch{Present: true, Value: &blockedPriority},
		DependencySessionID: StringPatch{Present: true, Value: &blockedDependency},
	}); err != nil {
		t.Fatalf("EditSession(blocked) error = %v", err)
	}
	snoozedPriority := 1.0
	now = now.Add(time.Minute)
	snoozeUntil := now.Add(time.Hour).Unix()
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:      mustSessionID(t, "s_3"),
		PriorityOffset: Float64Patch{Present: true, Value: &snoozedPriority},
		SnoozeUntil:    Int64Patch{Present: true, Value: &snoozeUntil},
	}); err != nil {
		t.Fatalf("EditSession(snoozed) error = %v", err)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	want := []string{"s_1", "s_3", "s_2"}
	listedOrder := make([]string, 0, len(want))
	for _, item := range listed.Items {
		if item.CWD == cwd && item.AgentBackend == "pi" {
			listedOrder = append(listedOrder, item.SessionID)
		}
	}
	assertSessionIDOrder(t, "ListSessions() filtered", listedOrder, want)

	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeOrder := make([]string, 0, len(resume.Sessions))
	for _, item := range resume.Sessions {
		resumeOrder = append(resumeOrder, item.SessionID)
	}
	assertSessionIDOrder(t, "SessionResumeCandidates()", resumeOrder, []string{"s_3", "s_2", "s_1"})
}

func TestStubSessionResumeCandidatesUseUpdatedTimeTieBreaker(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	cwd := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: cwd}); err != nil {
			t.Fatalf("CreateSession(%d) error = %v", i, err)
		}
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	want := []string{"s_1", "s_2"}
	listedOrder := make([]string, 0, len(want))
	for _, item := range listed.Items {
		if item.CWD == cwd && item.AgentBackend == "pi" {
			listedOrder = append(listedOrder, item.SessionID)
		}
	}
	assertSessionIDOrder(t, "ListSessions() filtered", listedOrder, want)

	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeOrder := make([]string, 0, len(resume.Sessions))
	for _, item := range resume.Sessions {
		resumeOrder = append(resumeOrder, item.SessionID)
	}
	assertSessionIDOrder(t, "SessionResumeCandidates()", resumeOrder, []string{"s_1", "s_2"})
}

func TestStubSessionActionsReturnNotFoundOrUnsupported(t *testing.T) {
	svc, _, _, _ := newSessionActionFixture(t)
	unknown, err := session.ParseSessionID("s_404")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, err = svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: unknown, Name: "Missing"})
	assertNotFound(t, err)

	_, err = svc.FocusSession(context.Background(), FocusSessionRequest{SessionID: unknown, Focused: true})
	assertNotFound(t, err)

	name := "Missing"
	_, err = svc.EditSession(context.Background(), EditSessionRequest{SessionID: unknown, Name: StringPatch{Present: true, Value: &name}})
	assertNotFound(t, err)

	model := "gpt-next"
	_, err = svc.SwitchSessionModel(context.Background(), SwitchSessionModelRequest{SessionID: unknown, Model: StringPatch{Present: true, Value: &model}})
	assertNotFound(t, err)

	_, err = svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: unknown})
	assertNotFound(t, err)

	_, err = svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: unknown})
	assertNotFound(t, err)

	_, err = svc.HandoffSession(context.Background(), HandoffSessionRequest{SessionID: unknown})
	assertNotFound(t, err)

}

func TestStubHandoffSessionHandlesLargeSourceLines(t *testing.T) {
	svc, handles, sessionID, _ := newSessionActionFixtureForBackend(t, "pi")
	oldRecord, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	oldSourcePath := strings.TrimSpace(oldRecord.importedSourcePath)
	if oldSourcePath == "" {
		t.Fatal("old importedSourcePath is empty")
	}
	if err := os.MkdirAll(filepath.Dir(oldSourcePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(old source dir) error = %v", err)
	}
	largeToolResult := strings.Repeat("x", maxRuntimeLineBytes+1024)
	body := strings.Join([]string{
		`{"type":"session","version":3,"id":"pi-old","cwd":"/tmp/project"}`,
		`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"continue work"}]}}`,
		fmt.Sprintf(`{"type":"tool_result","id":"t1","call_id":"call-1","name":"bash","text":%q}`, largeToolResult),
		`{"type":"message","id":"u2","message":{"role":"user","content":[{"type":"text","text":"last instruction"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(oldSourcePath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(old source) error = %v", err)
	}

	response, err := svc.HandoffSession(context.Background(), HandoffSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("HandoffSession() error = %v", err)
	}
	if !response.OK || response.Session == nil {
		t.Fatalf("HandoffSession() = %+v", response)
	}
	if len(*handles) != 2 {
		t.Fatalf("len(handles) = %d, want 2", len(*handles))
	}
	if _, err := os.Stat(response.SidecarPath); err != nil {
		t.Fatalf("Stat(SidecarPath) error = %v", err)
	}
	newID, err := session.ParseSessionID(response.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(new) error = %v", err)
	}
	if _, err := svc.lookupSession(newID); err != nil {
		t.Fatalf("lookupSession(new) error = %v", err)
	}
}

func TestStubHandoffSessionCreatesFreshPISessionAndArchivesPrevious(t *testing.T) {
	svc, handles, sessionID, _ := newSessionActionFixtureForBackend(t, "pi")
	oldRecord, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	oldSourcePath := strings.TrimSpace(oldRecord.importedSourcePath)
	if oldSourcePath == "" {
		t.Fatal("old importedSourcePath is empty")
	}
	if err := os.MkdirAll(filepath.Dir(oldSourcePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(old source dir) error = %v", err)
	}
	if err := os.WriteFile(oldSourcePath, []byte(`{"type":"session","version":3,"id":"pi-old","cwd":"/tmp/project"}
{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"continue work"}]}}
`), 0o644); err != nil {
		t.Fatalf("WriteFile(old source) error = %v", err)
	}
	oldHandle := (*handles)[0]

	response, err := svc.HandoffSession(context.Background(), HandoffSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("HandoffSession() error = %v", err)
	}
	if !response.OK || response.Session == nil {
		t.Fatalf("HandoffSession() = %+v", response)
	}
	if response.PreviousSessionID != sessionID.String() || response.HistoryPath != oldSourcePath {
		t.Fatalf("HandoffSession() previous/history = %q/%q, want %q/%q", response.PreviousSessionID, response.HistoryPath, sessionID, oldSourcePath)
	}
	if strings.TrimSpace(response.SidecarPath) == "" {
		t.Fatal("HandoffSession().SidecarPath is empty")
	}
	if _, err := os.Stat(response.SidecarPath); err != nil {
		t.Fatalf("Stat(SidecarPath) error = %v", err)
	}
	if response.SessionID == "" || response.RuntimeID == "" || response.SessionID == sessionID.String() {
		t.Fatalf("HandoffSession() ids = %+v", response)
	}
	if oldHandle.KillCalls() != 1 {
		t.Fatalf("old handle KillCalls() = %d, want 1", oldHandle.KillCalls())
	}
	if len(*handles) != 2 {
		t.Fatalf("len(handles) = %d, want 2", len(*handles))
	}
	promptWrites := (*handles)[1].PTY().(*fakePTY).Writes()
	if len(promptWrites) != 1 || !strings.Contains(promptWrites[0], response.SidecarPath) || strings.Contains(promptWrites[0], oldSourcePath) {
		t.Fatalf("handoff prompt writes = %#v, want sidecar path only", promptWrites)
	}
	newID, err := session.ParseSessionID(response.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(new) error = %v", err)
	}
	newRecord, err := svc.lookupSession(newID)
	if err != nil {
		t.Fatalf("lookupSession(new) error = %v", err)
	}
	if strings.TrimSpace(newRecord.importedSourcePath) == "" || newRecord.importedSourcePath == oldSourcePath {
		t.Fatalf("new importedSourcePath = %q, old = %q", newRecord.importedSourcePath, oldSourcePath)
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].SessionID != response.SessionID {
		t.Fatalf("ListSessions() = %+v, want only new session %q", listed.Items, response.SessionID)
	}
}

func TestStubRestartSessionWithMissingPISourceCreatesFreshSource(t *testing.T) {
	svc, handles, sessionID, _ := newSessionActionFixtureForBackend(t, "pi")
	missingPath := filepath.Join(t.TempDir(), "missing.jsonl")
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.importedSourcePath = missingPath
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = ok %v err %v", ok, err)
	}

	restarted, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("RestartSession() error = %v", err)
	}
	if !restarted.OK {
		t.Fatalf("RestartSession() = %+v, want OK", restarted)
	}
	updated, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession(updated) error = %v", err)
	}
	if strings.TrimSpace(updated.importedSourcePath) == "" || updated.importedSourcePath == missingPath {
		t.Fatalf("updated importedSourcePath = %q, missing = %q", updated.importedSourcePath, missingPath)
	}
	args := (*handles)[1].Spec().Command().Args()
	for i, arg := range args {
		if arg == "--session" && i+1 < len(args) && args[i+1] == missingPath {
			t.Fatalf("restart args use missing source path: %#v", args)
		}
	}
}

func TestHandoffSidecarStartsAfterLastCompactionAndMasksOldToolResults(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.jsonl")
	body := `{"type":"message","id":"old-user","message":{"role":"user","content":[{"type":"text","text":"old user"}]}}
{"type":"turn_end","id":"old-turn","toolResults":[{"toolCallId":"old-call","toolName":"read","content":[{"type":"text","text":"old result before compaction"}]}]}
{"type":"compaction_end","reason":"manual","aborted":false,"result":{"summary":"compact","firstKeptEntryId":"after-compact","tokensBefore":100}}
{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"first after compact"}]}}
{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-1","name":"read","arguments":{"path":"/tmp/a"}},{"type":"text","text":"assistant one","textSignature":"{\"phase\":\"final_answer\"}"}],"stopReason":"stop"}}
{"type":"turn_end","id":"t1","toolResults":[{"toolCallId":"call-1","toolName":"read","content":[{"type":"text","text":"result one"}]}]}
{"type":"message","id":"u2","message":{"role":"user","content":[{"type":"text","text":"second after compact"}]}}
{"type":"turn_end","id":"t2","toolResults":[{"toolCallId":"call-2","toolName":"read","content":[{"type":"text","text":"result two"}]}]}
{"type":"message","id":"u3","message":{"role":"user","content":[{"type":"text","text":"third after compact"}]}}
`
	if err := os.WriteFile(sourcePath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	sessionID, err := session.ParseSessionID("s_1")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	sidecar, err := buildSessionHandoffSidecar(sessionID, sourcePath, time.Unix(1760000000, 0).UTC())
	if err != nil {
		t.Fatalf("buildSessionHandoffSidecar() error = %v", err)
	}
	if !sidecar.StartsAfterCompact || sidecar.FirstSourceLine != 4 {
		t.Fatalf("sidecar compact window = (%v, %d), want true/4", sidecar.StartsAfterCompact, sidecar.FirstSourceLine)
	}
	for _, entry := range sidecar.Entries {
		if strings.Contains(entry.Text, "old user") || strings.Contains(entry.Text, "old result before compaction") {
			t.Fatalf("sidecar includes pre-compaction entry: %+v", entry)
		}
	}
	var masked, recent bool
	for _, entry := range sidecar.Entries {
		if entry.ToolCallID == "call-1" && entry.Masked && entry.OriginalSize == len("result one") {
			masked = true
		}
		if entry.ToolCallID == "call-2" && !entry.Masked && entry.Text == "result two" {
			recent = true
		}
	}
	if !masked || !recent || sidecar.MaskedToolResults != 1 {
		t.Fatalf("sidecar tool result masking = masked:%v recent:%v count:%d entries:%+v", masked, recent, sidecar.MaskedToolResults, sidecar.Entries)
	}
}

func TestStubEditSessionRejectsIODModeSwitchWhileBusy(t *testing.T) {
	svc, _, sessionID, _ := newSessionActionFixtureForBackend(t, "pi")
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok=true err=nil", ok, err)
	}
	mode := "grpc"
	_, err := svc.EditSession(context.Background(), EditSessionRequest{SessionID: sessionID, IODMode: StringPatch{Present: true, Value: &mode}})
	if err == nil {
		t.Fatal("EditSession() err = nil, want conflict")
	}
	appErr, ok := err.(*Error)
	if !ok || appErr.Code != "conflict" {
		t.Fatalf("EditSession() err = %T %[1]v, want conflict", err)
	}
}

func TestStubEditSessionIODModeSwitchWaitsForInputLock(t *testing.T) {
	runner := &process.FakeRunner{}
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, fakePiAgentServer{})
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()

	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner: runner,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
	})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(session) error = %v", err)
	}
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	record.inputMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	mode := "grpc"
	go func() {
		close(started)
		_, err := svc.EditSession(context.Background(), EditSessionRequest{SessionID: sessionID, IODMode: StringPatch{Present: true, Value: &mode}})
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("EditSession returned before input lock released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	record.inputMu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("EditSession() error = %v", err)
	}
}

func TestStubEditSessionSwitchToGRPCClearsHelperAttachment(t *testing.T) {
	runner := &process.FakeRunner{}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(session) error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_1777800090704767107")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	proof, err := iod.NewHelloProof(12345, nil, filepath.Join(t.TempDir(), "transport.wal"), filepath.Join(t.TempDir(), "io"), float64(time.Now().UTC().Unix()))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	hello, err := iod.NewHelloPacket(sessionID, generationID, 1, manifest.HelloProof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	svc.helpers.replaceAll(map[session.SessionID]attachedHelper{
		sessionID: {
			Binding:  helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
			Manifest: manifest,
			Hello:    hello,
			Client:   &iodclient.Client{},
		},
	}, nil)
	bindingDir := t.TempDir()
	svc.helperBindings = newHelperBindingStore(bindingDir)
	if err := svc.bindCurrentGeneration(helperGenerationBinding{SessionID: sessionID, GenerationID: generationID}); err != nil {
		t.Fatalf("bindCurrentGeneration() error = %v", err)
	}

	mode := "grpc"
	grpcCfg := grpcRuntimeConfigForTest(t)
	svc.launcher = newRuntimeLauncher(grpcCfg)
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{SessionID: sessionID, IODMode: StringPatch{Present: true, Value: &mode}}); err != nil {
		t.Fatalf("EditSession() error = %v", err)
	}
	if _, ok := svc.helpers.Attachment(sessionID); ok {
		t.Fatal("helpers.Attachment(grpc) ok = true, want cleared")
	}
	bindings, err := svc.helperBindings.Load()
	if err != nil {
		t.Fatalf("helperBindings.Load() error = %v", err)
	}
	if _, ok := bindings[sessionID]; ok {
		t.Fatal("helper binding persisted after grpc switch")
	}
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	runtime := svc.runtimeForSession(sessionID, session.BackendPI, record.runtime)
	if runtime.piAgentGRPC == nil || runtime.helper != nil {
		t.Fatalf("runtimeForSession() = helper:%v grpc:%v, want grpc only", runtime.helper, runtime.piAgentGRPC)
	}
}

func grpcRuntimeConfigForTest(t *testing.T) RuntimeConfig {
	t.Helper()
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, fakePiAgentServer{})
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()
	return RuntimeConfig{
		Runner: &process.FakeRunner{},
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
	}
}

func TestStubEditSessionSwitchesIODMode(t *testing.T) {
	runner := &process.FakeRunner{}
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, fakePiAgentServer{})
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()

	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner: runner,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
	})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(session) error = %v", err)
	}
	mode := "grpc"
	resp, err := svc.EditSession(context.Background(), EditSessionRequest{SessionID: sessionID, IODMode: StringPatch{Present: true, Value: &mode}})
	if err != nil {
		t.Fatalf("EditSession() error = %v", err)
	}
	if resp.IOD == nil || resp.IOD.Mode != "grpc" {
		t.Fatalf("EditSession().IOD = %+v, want grpc", resp.IOD)
	}
	if len(runner.Starts) != 2 {
		t.Fatalf("len(runner.Starts) = %d, want 2", len(runner.Starts))
	}
	args := runner.Starts[1].Command().Args()
	wantPrefix := []string{"--mode", "grpc", "--grpc-socket", "/tmp/custom-pi-agent.sock"}
	if len(args) < len(wantPrefix) || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("restart args = %#v, want grpc prefix %#v", args, wantPrefix)
	}
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	if record.runtime.piAgentGRPC == nil {
		t.Fatal("record.runtime.piAgentGRPC = nil after mode switch")
	}
}

func TestStubRestartSessionWaitsForInputLock(t *testing.T) {
	svc, _, sessionID, _ := newSessionActionFixtureForBackend(t, "codex")
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	record.inputMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: sessionID})
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("RestartSession returned before input lock released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	record.inputMu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("RestartSession() error = %v", err)
	}
}

func TestStubRestartSessionDefaultsPIToGRPC(t *testing.T) {
	runner := &process.FakeRunner{}
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, fakePiAgentServer{})
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()

	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner: runner,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
	})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(session) error = %v", err)
	}
	restarted, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("RestartSession() error = %v", err)
	}
	if !restarted.OK || restarted.Session == nil {
		t.Fatalf("RestartSession() = %+v, want session", restarted)
	}
	if len(runner.Starts) != 2 {
		t.Fatalf("len(runner.Starts) = %d, want 2", len(runner.Starts))
	}
	wantPrefix := []string{"--mode", "grpc", "--grpc-socket", "/tmp/custom-pi-agent.sock"}
	for i, start := range runner.Starts {
		args := start.Command().Args()
		if len(args) < len(wantPrefix) || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
			t.Fatalf("runner.Starts[%d] args = %#v, want grpc prefix %#v", i, args, wantPrefix)
		}
	}
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	if record.runtime.piAgentGRPC == nil {
		t.Fatal("record.runtime.piAgentGRPC = nil after restart")
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].IOD == nil || listed.Items[0].IOD.Mode != "grpc" {
		t.Fatalf("ListSessions().Items = %+v, want grpc mode", listed.Items)
	}
}

func TestStubRestartSessionConvertsStdPIToGRPC(t *testing.T) {
	runner := &process.FakeRunner{}
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, fakePiAgentServer{})
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()

	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner: runner,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
	})
	useGRPC := false
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir(), PIAgentGRPC: &useGRPC})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	restarted, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("RestartSession() error = %v", err)
	}
	if !restarted.OK || restarted.Session == nil {
		t.Fatalf("RestartSession() = %+v, want session", restarted)
	}
	if len(runner.Starts) != 2 {
		t.Fatalf("len(runner.Starts) = %d, want std start plus grpc restart", len(runner.Starts))
	}
	args := runner.Starts[1].Command().Args()
	wantPrefix := []string{"--mode", "grpc", "--grpc-socket", "/tmp/custom-pi-agent.sock"}
	if len(args) < len(wantPrefix) || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("restart args = %#v, want grpc prefix %#v", args, wantPrefix)
	}
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	if record.runtime.piAgentGRPC == nil {
		t.Fatal("record.runtime.piAgentGRPC = nil after std restart")
	}
}

func TestStubRestartSessionReplacesRuntimeAndPreservesSessionState(t *testing.T) {
	svc, handles, sessionID, runtimeID := newSessionActionFixtureForBackend(t, "codex")
	if len(*handles) != 1 {
		t.Fatalf("len(handles) = %d, want 1", len(*handles))
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok=true err=nil", ok, err)
	}
	if _, err := svc.AppendSessionMessage(sessionID, "user", "message", "hello"); err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}
	if _, err := svc.AppendAssistantDelta(sessionID, "turn_restart", "partial reply"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}
	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose one"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	if _, ok, err := svc.registry.SetTransport(sessionID, SessionTransportSnapshot{State: SessionTransportStateEnded, Reason: "helper_not_running"}); err != nil || !ok {
		t.Fatalf("registry.SetTransport() = (_, %v, %v), want ok=true err=nil", ok, err)
	}

	restarted, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: runtimeID})
	if err != nil {
		t.Fatalf("RestartSession() error = %v", err)
	}
	if !restarted.OK || !restarted.Restarted {
		t.Fatalf("RestartSession() = %+v, want ok restarted response", restarted)
	}
	if restarted.SessionID != sessionID.String() {
		t.Fatalf("RestartSession().SessionID = %q, want %q", restarted.SessionID, sessionID)
	}
	if restarted.PreviousRuntimeID != runtimeID.String() {
		t.Fatalf("RestartSession().PreviousRuntimeID = %q, want %q", restarted.PreviousRuntimeID, runtimeID)
	}
	if restarted.RuntimeID == runtimeID.String() || restarted.RuntimeID == "" {
		t.Fatalf("RestartSession().RuntimeID = %q, want new runtime id", restarted.RuntimeID)
	}
	if restarted.Session == nil || restarted.Session.RuntimeID != restarted.RuntimeID {
		t.Fatalf("RestartSession().Session = %+v, want runtime %q", restarted.Session, restarted.RuntimeID)
	}
	if restarted.Session.TransportState != SessionTransportStateAttached.String() {
		t.Fatalf("RestartSession().Session.TransportState = %q, want %q", restarted.Session.TransportState, SessionTransportStateAttached)
	}
	if restarted.WSAttach == nil || restarted.WSAttach.SessionID != sessionID.String() {
		t.Fatalf("RestartSession().WSAttach = %+v, want session %q", restarted.WSAttach, sessionID)
	}
	if len(*handles) != 2 {
		t.Fatalf("len(handles) after restart = %d, want 2", len(*handles))
	}
	if (*handles)[0].KillCalls() != 1 {
		t.Fatalf("old handle KillCalls() = %d, want 1", (*handles)[0].KillCalls())
	}
	if (*handles)[1].KillCalls() != 0 {
		t.Fatalf("new handle KillCalls() = %d, want 0", (*handles)[1].KillCalls())
	}

	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() after restart error = %v", err)
	}
	updatedRuntimeID, ok := record.identity.RuntimeID()
	if !ok {
		t.Fatal("record.identity.RuntimeID() ok = false, want true")
	}
	if updatedRuntimeID.String() != restarted.RuntimeID {
		t.Fatalf("record runtime id = %q, want %q", updatedRuntimeID, restarted.RuntimeID)
	}
	if record.runtime.PID() != 322 {
		t.Fatalf("record.runtime.PID() = %d, want 322", record.runtime.PID())
	}
	if record.state.Busy() {
		t.Fatal("record.state.Busy() = true, want false after restart")
	}
	if record.uiRequest != nil {
		t.Fatalf("record.uiRequest = %+v, want nil after restart", record.uiRequest)
	}
	if record.transport != (SessionTransportSnapshot{}) {
		t.Fatalf("record.transport = %+v, want empty after restart", record.transport)
	}
	if record.resumeCursors != (SessionResumeCursors{}) {
		t.Fatalf("record.resumeCursors = %+v, want empty", record.resumeCursors)
	}
	if partial, ok := record.transcript.PartialAssistantTurn(); ok {
		t.Fatalf("record.transcript.PartialAssistantTurn() = %+v, want cleared partial", partial)
	}
	items := record.transcript.Items()
	if len(items) != 1 || items[0].Text() != "hello" {
		t.Fatalf("record transcript items = %+v, want preserved user message", items)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after restart error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false after restart")
	}
	if state.UIRequest != nil {
		t.Fatalf("SessionState().UIRequest = %+v, want nil after restart", state.UIRequest)
	}
	if state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState().Transport = %+v, want attached after restart", state.Transport)
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil after restart", state.PartialAssistantTurn)
	}
}

func TestStubRestartSessionSupportsCodexSessions(t *testing.T) {
	svc, _, sessionID, runtimeID := newSessionActionFixtureForBackend(t, "codex")

	restarted, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("RestartSession(codex) error = %v", err)
	}
	if restarted.SessionID != sessionID.String() {
		t.Fatalf("RestartSession(codex).SessionID = %q, want %q", restarted.SessionID, sessionID)
	}
	if restarted.PreviousRuntimeID != runtimeID.String() {
		t.Fatalf("RestartSession(codex).PreviousRuntimeID = %q, want %q", restarted.PreviousRuntimeID, runtimeID)
	}
	if restarted.RuntimeID == runtimeID.String() || restarted.RuntimeID == "" {
		t.Fatalf("RestartSession(codex).RuntimeID = %q, want new runtime id", restarted.RuntimeID)
	}
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession(codex) error = %v", err)
	}
	if record.identity.Backend() != session.BackendCodex {
		t.Fatalf("record.identity.Backend() = %q, want %q", record.identity.Backend(), session.BackendCodex)
	}
}

func assertUnsupported(t *testing.T, err error) {
	t.Helper()
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if appErr.Code != "unsupported" {
		t.Fatalf("error code = %q, want %q", appErr.Code, "unsupported")
	}
}

func assertSessionIDOrder(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s len = %d, want %d (%#v)", label, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s order = %#v, want %#v", label, got, want)
		}
	}
}
