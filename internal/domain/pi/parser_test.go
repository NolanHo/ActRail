package pi

import (
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", name, err)
	}
	return data
}

func TestParseJSONLRuntimeSessionExtractsHeaderAndCommitLikeMessages(t *testing.T) {
	material, err := ParseJSONLBytes(loadFixture(t, "runtime_session.jsonl"))
	if err != nil {
		t.Fatalf("ParseJSONLBytes() error = %v", err)
	}
	if material.Header == nil {
		t.Fatal("Header = nil, want session header")
	}
	if material.Header.SessionID != "pi-session-001" {
		t.Fatalf("Header.SessionID = %q, want %q", material.Header.SessionID, "pi-session-001")
	}
	if material.Header.Version != 3 {
		t.Fatalf("Header.Version = %d, want %d", material.Header.Version, 3)
	}
	if material.Header.CWD != "/workspace/codoxear" {
		t.Fatalf("Header.CWD = %q, want %q", material.Header.CWD, "/workspace/codoxear")
	}
	if len(material.Events) != 4 {
		t.Fatalf("len(Events) = %d, want %d", len(material.Events), 4)
	}

	user := material.Events[0]
	if user.Kind != EventKindMessage || user.Message == nil {
		t.Fatalf("Events[0] = %#v, want message event", user)
	}
	if user.Message.Role != MessageRoleUser {
		t.Fatalf("Events[0].Message.Role = %q, want %q", user.Message.Role, MessageRoleUser)
	}
	if user.Message.Text != "Summarize the current repository state." {
		t.Fatalf("Events[0].Message.Text = %q, want user text", user.Message.Text)
	}

	started := material.Events[1]
	if started.Boundary == nil || started.Boundary.Kind != BoundaryKindTurnStarted {
		t.Fatalf("Events[1].Boundary = %#v, want turn_started", started.Boundary)
	}
	if !started.Boundary.Inferred {
		t.Fatal("Events[1].Boundary.Inferred = false, want true")
	}

	assistant := material.Events[2]
	if assistant.Kind != EventKindMessage || assistant.Message == nil {
		t.Fatalf("Events[2] = %#v, want assistant message", assistant)
	}
	if assistant.Message.Role != MessageRoleAssistant {
		t.Fatalf("Events[2].Message.Role = %q, want %q", assistant.Message.Role, MessageRoleAssistant)
	}
	if assistant.Message.Class != MessageClassFinal {
		t.Fatalf("Events[2].Message.Class = %q, want %q", assistant.Message.Class, MessageClassFinal)
	}
	if !assistant.Message.CommitLike {
		t.Fatal("Events[2].Message.CommitLike = false, want true")
	}

	completed := material.Events[3]
	if completed.Boundary == nil || completed.Boundary.Kind != BoundaryKindTurnCompleted {
		t.Fatalf("Events[3].Boundary = %#v, want turn_completed", completed.Boundary)
	}
	if !completed.Boundary.CommitLike {
		t.Fatal("Events[3].Boundary.CommitLike = false, want true")
	}
}

func TestParseJSONLAskUserRoundTripExtractsTypedRequestAndResolution(t *testing.T) {
	material, err := ParseJSONLBytes(loadFixture(t, "ask_user_roundtrip.jsonl"))
	if err != nil {
		t.Fatalf("ParseJSONLBytes() error = %v", err)
	}
	if len(material.Events) != 2 {
		t.Fatalf("len(Events) = %d, want %d", len(material.Events), 2)
	}

	request := material.Events[0]
	if request.Kind != EventKindUIRequest || request.UIRequest == nil {
		t.Fatalf("Events[0] = %#v, want ui_request", request)
	}
	if request.UIRequest.RequestID != "call_ask_q_1" {
		t.Fatalf("Events[0].UIRequest.RequestID = %q, want %q", request.UIRequest.RequestID, "call_ask_q_1")
	}
	if request.UIRequest.Source != UIRequestSourceAskUserTool {
		t.Fatalf("Events[0].UIRequest.Source = %q, want %q", request.UIRequest.Source, UIRequestSourceAskUserTool)
	}
	if request.UIRequest.Kind != UIRequestKindAskUser {
		t.Fatalf("Events[0].UIRequest.Kind = %q, want %q", request.UIRequest.Kind, UIRequestKindAskUser)
	}
	if request.UIRequest.Prompt != "How should Pi use ~/.pi/agent/models.json?" {
		t.Fatalf("Events[0].UIRequest.Prompt = %q, want question", request.UIRequest.Prompt)
	}
	if request.UIRequest.Context != "Pi list" {
		t.Fatalf("Events[0].UIRequest.Context = %q, want %q", request.UIRequest.Context, "Pi list")
	}
	if len(request.UIRequest.Options) != 2 {
		t.Fatalf("len(Events[0].UIRequest.Options) = %d, want %d", len(request.UIRequest.Options), 2)
	}
	if request.UIRequest.Options[0].Label != "Provider-linked model list" {
		t.Fatalf("Events[0].UIRequest.Options[0].Label = %q, want first label", request.UIRequest.Options[0].Label)
	}
	if len(request.UIRequest.Questions) != 1 {
		t.Fatalf("len(Events[0].UIRequest.Questions) = %d, want %d", len(request.UIRequest.Questions), 1)
	}
	if request.UIRequest.Metadata["source"] != "brainstorming" {
		t.Fatalf("Events[0].UIRequest.Metadata[source] = %#v, want %q", request.UIRequest.Metadata["source"], "brainstorming")
	}

	resolved := material.Events[1]
	if resolved.Kind != EventKindUIResolved || resolved.UIResolved == nil {
		t.Fatalf("Events[1] = %#v, want ui_resolved", resolved)
	}
	if resolved.UIResolved.RequestID != "call_ask_q_1" {
		t.Fatalf("Events[1].UIResolved.RequestID = %q, want %q", resolved.UIResolved.RequestID, "call_ask_q_1")
	}
	if resolved.UIResolved.AnswerText != "Provider-linked model list" {
		t.Fatalf("Events[1].UIResolved.AnswerText = %q, want answer", resolved.UIResolved.AnswerText)
	}
	if resolved.UIResolved.AnswersByQuestion["How should Pi use ~/.pi/agent/models.json?"] != "Provider-linked model list" {
		t.Fatalf("Events[1].UIResolved.AnswersByQuestion = %#v, want mapped answer", resolved.UIResolved.AnswersByQuestion)
	}
	if resolved.UIResolved.PromptFallbackAvailable {
		t.Fatal("Events[1].UIResolved.PromptFallbackAvailable = true, want false")
	}
}

func TestParseJSONLLiveRuntimeEventsExtractInteractiveRequestDeltaResolutionAndBoundary(t *testing.T) {
	material, err := ParseJSONLBytes(loadFixture(t, "live_runtime_events.jsonl"))
	if err != nil {
		t.Fatalf("ParseJSONLBytes() error = %v", err)
	}
	if len(material.Events) != 5 {
		t.Fatalf("len(Events) = %d, want %d", len(material.Events), 5)
	}

	request := material.Events[0]
	if request.UIRequest == nil {
		t.Fatalf("Events[0].UIRequest = nil, want request")
	}
	if request.UIRequest.Method != UIMethodSelect {
		t.Fatalf("Events[0].UIRequest.Method = %q, want %q", request.UIRequest.Method, UIMethodSelect)
	}
	if !request.UIRequest.AllowFreeform {
		t.Fatal("Events[0].UIRequest.AllowFreeform = false, want true")
	}
	if !request.UIRequest.AllowMultiple {
		t.Fatal("Events[0].UIRequest.AllowMultiple = false, want true")
	}
	if request.UIRequest.TimeoutMS == nil || *request.UIRequest.TimeoutMS != 10000 {
		t.Fatalf("Events[0].UIRequest.TimeoutMS = %#v, want %d", request.UIRequest.TimeoutMS, 10000)
	}

	delta := material.Events[1]
	if delta.Delta == nil || delta.Delta.Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("Events[1].Delta = %#v, want assistant delta", delta.Delta)
	}

	resolved := material.Events[2]
	if resolved.UIResolved == nil || resolved.UIResolved.AnswerText != "Sidebar" {
		t.Fatalf("Events[2].UIResolved = %#v, want resolved Sidebar answer", resolved.UIResolved)
	}
	if resolved.UIResolved.RequestID != "ui-req-1" {
		t.Fatalf("Events[2].UIResolved.RequestID = %q, want %q", resolved.UIResolved.RequestID, "ui-req-1")
	}

	committed := material.Events[3]
	if committed.Message == nil || committed.Message.Class != MessageClassCommitted {
		t.Fatalf("Events[3].Message = %#v, want committed assistant message", committed.Message)
	}
	if !committed.Message.CommitLike {
		t.Fatal("Events[3].Message.CommitLike = false, want true")
	}

	boundary := material.Events[4]
	if boundary.Boundary == nil || boundary.Boundary.Kind != BoundaryKindTurnCompleted {
		t.Fatalf("Events[4].Boundary = %#v, want turn_completed", boundary.Boundary)
	}
	if boundary.Boundary.Inferred {
		t.Fatal("Events[4].Boundary.Inferred = true, want false")
	}
}

func TestParseJSONLRPCRuntimeEventsExtractDeltasCommittedReplyAndBoundary(t *testing.T) {
	material, err := ParseJSONLBytes(loadFixture(t, "rpc_runtime_events.jsonl"))
	if err != nil {
		t.Fatalf("ParseJSONLBytes() error = %v", err)
	}
	if len(material.Events) != 5 {
		t.Fatalf("len(Events) = %d, want %d", len(material.Events), 5)
	}

	started := material.Events[0]
	if started.Boundary == nil || started.Boundary.Kind != BoundaryKindTurnStarted {
		t.Fatalf("Events[0].Boundary = %#v, want turn_started", started.Boundary)
	}
	if started.Boundary.CommitLike {
		t.Fatal("Events[0].Boundary.CommitLike = true, want false")
	}

	firstDelta := material.Events[1]
	if firstDelta.Delta == nil || firstDelta.Delta.Text != "Codoxear serves " {
		t.Fatalf("Events[1].Delta = %#v, want first assistant delta", firstDelta.Delta)
	}
	secondDelta := material.Events[2]
	if secondDelta.Delta == nil || secondDelta.Delta.Text != "a browser UI for Codex-style sessions." {
		t.Fatalf("Events[2].Delta = %#v, want second assistant delta", secondDelta.Delta)
	}
	if firstDelta.Timestamp != secondDelta.Timestamp {
		t.Fatalf("delta timestamps = (%v, %v), want stable turn timestamp", firstDelta.Timestamp, secondDelta.Timestamp)
	}

	committed := material.Events[3]
	if committed.Message == nil || committed.Message.Class != MessageClassCommitted {
		t.Fatalf("Events[3].Message = %#v, want committed assistant message", committed.Message)
	}
	if !committed.Message.CommitLike {
		t.Fatal("Events[3].Message.CommitLike = false, want true")
	}
	if committed.Message.Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("Events[3].Message.Text = %q, want committed reply", committed.Message.Text)
	}

	completed := material.Events[4]
	if completed.Boundary == nil || completed.Boundary.Kind != BoundaryKindTurnCompleted {
		t.Fatalf("Events[4].Boundary = %#v, want turn_completed", completed.Boundary)
	}
	if completed.Boundary.Inferred {
		t.Fatal("Events[4].Boundary.Inferred = true, want false")
	}
}

func TestParseJSONLAssistantNarrationAndFinalHeuristics(t *testing.T) {
	data := []byte("{" +
		"\"type\":\"message\",\"message\":{\"role\":\"assistant\",\"stopReason\":\"toolUse\",\"content\":[{\"type\":\"text\",\"text\":\"working\"},{\"type\":\"thinking\",\"thinking\":\"hmm\"},{\"type\":\"toolCall\",\"id\":\"t1\",\"name\":\"bash\",\"arguments\":{\"command\":\"pwd\"}}]}}\n" +
		"{\"type\":\"message\",\"message\":{\"role\":\"assistant\",\"stopReason\":\"stop\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"\"},{\"type\":\"text\",\"text\":\"done\",\"textSignature\":\"{\\\"v\\\":1,\\\"phase\\\":\\\"final_answer\\\"}\"}]}}\n")
	material, err := ParseJSONLBytes(data)
	if err != nil {
		t.Fatalf("ParseJSONLBytes() error = %v", err)
	}
	if len(material.Events) != 3 {
		t.Fatalf("len(Events) = %d, want %d", len(material.Events), 3)
	}
	if material.Events[0].Message == nil || material.Events[0].Message.Class != MessageClassNarration {
		t.Fatalf("Events[0].Message = %#v, want narration", material.Events[0].Message)
	}
	if material.Events[0].Message.CommitLike {
		t.Fatal("Events[0].Message.CommitLike = true, want false")
	}
	if material.Events[1].Message == nil || material.Events[1].Message.Class != MessageClassFinal {
		t.Fatalf("Events[1].Message = %#v, want final response", material.Events[1].Message)
	}
	if material.Events[2].Boundary == nil || material.Events[2].Boundary.Kind != BoundaryKindTurnCompleted {
		t.Fatalf("Events[2].Boundary = %#v, want inferred turn_completed", material.Events[2].Boundary)
	}
}
