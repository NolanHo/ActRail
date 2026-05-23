package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const (
	handoffSidecarFormatVersion = 2
	handoffRecentUserTurns      = 2
	handoffMaskedToolText       = "[masked: old tool result omitted. Search the source session JSONL if this output is needed.]"

	handoffL2FormatVersion       = 1
	handoffL3FormatVersion       = 1
	handoffDeepSeekBaseURL       = "https://api.deepseek.com"
	handoffDeepSeekModel         = "deepseek-v4-pro"
	handoffDeepSeekTimeout       = 45 * time.Second
	handoffDeepSeekMaxInputChars = 120000
)

type sessionHandoffSidecar struct {
	Version            int                   `json:"version"`
	Kind               string                `json:"kind"`
	SourceSessionID    string                `json:"source_session_id"`
	SourcePath         string                `json:"source_path,omitempty"`
	GeneratedAt        string                `json:"generated_at"`
	StartsAfterCompact bool                  `json:"starts_after_compact"`
	FirstSourceLine    int                   `json:"first_source_line"`
	RecentUserTurns    int                   `json:"recent_user_turns"`
	MaskedToolResults  int                   `json:"masked_tool_results"`
	Layers             sessionHandoffLayers  `json:"layers"`
	Entries            []sessionHandoffEntry `json:"entries"`
}

type sessionHandoffLayers struct {
	L1 sessionHandoffLayerRef `json:"l1"`
	L2 sessionHandoffL2       `json:"l2"`
	L3 sessionHandoffL3       `json:"l3"`
}

type sessionHandoffLayerRef struct {
	Description string `json:"description"`
	Location    string `json:"location"`
}

type sessionHandoffEntry struct {
	Line         int            `json:"line,omitempty"`
	SourceID     string         `json:"source_id,omitempty"`
	ParentID     string         `json:"parent_id,omitempty"`
	Role         string         `json:"role,omitempty"`
	Kind         string         `json:"kind"`
	ToolCallID   string         `json:"tool_call_id,omitempty"`
	ToolName     string         `json:"tool_name,omitempty"`
	IsError      bool           `json:"is_error,omitempty"`
	Text         string         `json:"text,omitempty"`
	Arguments    map[string]any `json:"arguments,omitempty"`
	Masked       bool           `json:"masked,omitempty"`
	MaskReason   string         `json:"mask_reason,omitempty"`
	OriginalSize int            `json:"original_size,omitempty"`
}

type sessionHandoffL2 struct {
	Version     int                    `json:"version"`
	Kind        string                 `json:"kind"`
	Description string                 `json:"description"`
	Messages    []sessionHandoffL2Item `json:"messages"`
}

type sessionHandoffL2Item struct {
	ID       string               `json:"id"`
	Role     string               `json:"role"`
	Text     string               `json:"text"`
	Source   sessionHandoffSource `json:"source"`
	Position int                  `json:"position"`
}

type sessionHandoffSource struct {
	Line     int    `json:"line,omitempty"`
	SourceID string `json:"source_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Role     string `json:"role,omitempty"`
}

type sessionHandoffL3 struct {
	Version          int                        `json:"version"`
	Kind             string                     `json:"kind"`
	GeneratedBy      sessionHandoffGenerator    `json:"generated_by"`
	CurrentState     sessionHandoffCurrentState `json:"current_state"`
	ConversationTree []sessionHandoffTreeNode   `json:"conversation_tree"`
	ActiveThreads    []sessionHandoffThread     `json:"active_threads"`
	Decisions        []sessionHandoffFinding    `json:"decisions"`
	Constraints      []sessionHandoffFinding    `json:"constraints"`
	OpenQuestions    []sessionHandoffFinding    `json:"open_questions"`
	NextActions      []sessionHandoffFinding    `json:"next_actions"`
	MustReadL2       []sessionHandoffMustRead   `json:"must_read_l2"`
	RiskNotes        []sessionHandoffRisk       `json:"risk_notes"`
	GenerationError  string                     `json:"generation_error,omitempty"`
}

type sessionHandoffGenerator struct {
	Type  string `json:"type"`
	Model string `json:"model,omitempty"`
}

type sessionHandoffCurrentState struct {
	Goal                string `json:"goal,omitempty"`
	LatestUserIntent    string `json:"latest_user_intent,omitempty"`
	Status              string `json:"status,omitempty"`
	WorkingAssumption   string `json:"working_assumption,omitempty"`
	RecommendedNextStep string `json:"recommended_next_step,omitempty"`
}

type sessionHandoffTreeNode struct {
	ID       string                   `json:"id"`
	Title    string                   `json:"title"`
	Type     string                   `json:"type"`
	Status   string                   `json:"status"`
	Summary  string                   `json:"summary"`
	Anchors  []sessionHandoffL2Anchor `json:"anchors"`
	Children []sessionHandoffTreeNode `json:"children,omitempty"`
}

type sessionHandoffThread struct {
	ThreadID    string   `json:"thread_id"`
	Question    string   `json:"question"`
	WhyActive   string   `json:"why_active"`
	ReadL2First []string `json:"read_l2_first"`
}

type sessionHandoffFinding struct {
	Text       string                   `json:"text"`
	Source     string                   `json:"source,omitempty"`
	Confidence string                   `json:"confidence,omitempty"`
	Anchors    []sessionHandoffL2Anchor `json:"anchors"`
}

type sessionHandoffMustRead struct {
	L2ID   string `json:"l2_id"`
	Reason string `json:"reason"`
	When   string `json:"when,omitempty"`
}

type sessionHandoffRisk struct {
	Risk       string `json:"risk"`
	Mitigation string `json:"mitigation"`
}

type sessionHandoffL2Anchor struct {
	L2ID   string `json:"l2_id"`
	Role   string `json:"role,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type sessionHandoffL3Client interface {
	SummarizeHandoff(context.Context, sessionHandoffL2) (sessionHandoffL3, error)
}

func handoffPrompt(sidecarPath string) string {
	path := strings.TrimSpace(sidecarPath)
	return strings.Join([]string{
		"Continue the previous session from this ActRail handoff sidecar:",
		path,
		"Read layers.l3 first as a navigation map, not as the source of truth.",
		"Then read every layers.l3.must_read_l2 item from layers.l2 before acting on user constraints, decisions, or current task details.",
		"Use layers.l2 as the primary semantic source for user and assistant wording.",
		"Use entries as L1 only for tool calls/results, errors, or evidence checks.",
		"Do not read the source session JSONL unless L1 has masked data or the sidecar source anchors are insufficient.",
		"If source inspection is needed, search the source file for the sidecar line number, source_id, tool_call_id, or nearby text instead of reading the whole file.",
		"Continue from the last user instruction in the sidecar.",
	}, "\n")
}

func (s *Stub) writeSessionHandoffSidecar(ctx context.Context, record sessionRecord) (string, error) {
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("session %q has no source path for handoff", record.identity.SessionID())
	}
	sidecar, err := buildSessionHandoffSidecar(ctx, record.identity.SessionID(), sourcePath, s.registry.now(), s.sessionHandoffL3Client())
	if err != nil {
		return "", err
	}
	path, err := newSessionHandoffSidecarPath(record.cwd, record.identity.SessionID(), s.registry.now())
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal handoff sidecar: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", fmt.Errorf("write handoff sidecar %q: %w", path, err)
	}
	return path, nil
}

func (s *Stub) sessionHandoffL3Client() sessionHandoffL3Client {
	if s == nil || s.disableExternalHandoffL3 {
		return nil
	}
	if s.handoffL3Client != nil {
		return s.handoffL3Client
	}
	return defaultSessionHandoffL3Client()
}

func newSessionHandoffSidecarPath(cwd string, sessionID session.SessionID, now time.Time) (string, error) {
	if err := sessionID.Validate(); err != nil {
		return "", err
	}
	root := piHistoryBaseRoot()
	dir := filepath.Join(root, piSessionDirName(cwd), "actrail-handoffs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir handoff sidecar dir %q: %w", dir, err)
	}
	stamp := now.UTC().Format("2006-01-02T15-04-05-000Z")
	return filepath.Join(dir, fmt.Sprintf("%s_handoff_%s.json", stamp, sessionID.String())), nil
}

func buildSessionHandoffSidecar(ctx context.Context, sessionID session.SessionID, sourcePath string, now time.Time, l3Client sessionHandoffL3Client) (sessionHandoffSidecar, error) {
	rows, err := handoffSourceRows(sourcePath)
	if err != nil {
		return sessionHandoffSidecar{}, err
	}
	start := lastCompactionEndRow(rows)
	selected := rows[start:]
	entries := make([]sessionHandoffEntry, 0, len(selected))
	for _, row := range selected {
		entries = append(entries, handoffEntriesFromRow(row)...)
	}
	masked := maskOldHandoffToolResults(entries)
	l2 := buildSessionHandoffL2(entries)
	l3 := buildFallbackSessionHandoffL3(l2, "")
	if l3Client != nil {
		generated, err := l3Client.SummarizeHandoff(ctx, l2)
		if err != nil {
			l3 = buildFallbackSessionHandoffL3(l2, err.Error())
		} else if err := validateSessionHandoffL3(generated, l2); err != nil {
			l3 = buildFallbackSessionHandoffL3(l2, fmt.Sprintf("invalid generated l3: %v", err))
		} else {
			l3 = generated
		}
	}
	return sessionHandoffSidecar{
		Version:            handoffSidecarFormatVersion,
		Kind:               "actrail_session_handoff",
		SourceSessionID:    sessionID.String(),
		SourcePath:         sourcePath,
		GeneratedAt:        now.UTC().Format(time.RFC3339Nano),
		StartsAfterCompact: start > 0,
		FirstSourceLine:    firstSelectedSourceLine(selected),
		RecentUserTurns:    handoffRecentUserTurns,
		MaskedToolResults:  masked,
		Layers: sessionHandoffLayers{
			L1: sessionHandoffLayerRef{
				Description: "Deterministic selected event extract with user, assistant, tool call/result, and error entries.",
				Location:    "entries",
			},
			L2: l2,
			L3: l3,
		},
		Entries: entries,
	}, nil
}

type handoffSourceRow struct {
	line     int
	raw      map[string]any
	material pi.Material
}

func handoffSourceRows(sourcePath string) ([]handoffSourceRow, error) {
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open handoff source %q: %w", sourcePath, err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	rows := make([]handoffSourceRow, 0)
	lineNo := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("read handoff source %q line %d: %w", sourcePath, lineNo+1, err)
			}
			break
		}
		lineNo++
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			var raw map[string]any
			if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
				return nil, fmt.Errorf("parse handoff source %q line %d: %w", sourcePath, lineNo, err)
			}
			material := pi.ParseRawObject(raw)
			rows = append(rows, handoffSourceRow{line: lineNo, raw: raw, material: material})
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("read handoff source %q line %d: %w", sourcePath, lineNo, err)
			}
			break
		}
	}
	return rows, nil
}

func lastCompactionEndRow(rows []handoffSourceRow) int {
	start := 0
	for i, row := range rows {
		for _, event := range row.material.Events {
			if event.Compaction != nil && event.Compaction.Phase == "end" && !event.Compaction.Aborted {
				start = i + 1
			}
		}
	}
	if start >= len(rows) {
		return 0
	}
	return start
}

func firstSelectedSourceLine(rows []handoffSourceRow) int {
	if len(rows) == 0 {
		return 0
	}
	return rows[0].line
}

func handoffEntriesFromRow(row handoffSourceRow) []sessionHandoffEntry {
	out := make([]sessionHandoffEntry, 0, len(row.material.Events))
	for _, event := range row.material.Events {
		sourceID := strings.TrimSpace(event.RawID)
		parentID := strings.TrimSpace(event.ParentID)
		switch event.Kind {
		case pi.EventKindMessage:
			if event.Message == nil {
				continue
			}
			text := strings.TrimSpace(event.Message.Text)
			if text == "" || strings.TrimSpace(event.Message.StopReason) == "status" {
				continue
			}
			if event.Message.Role != pi.MessageRoleUser && event.Message.Role != pi.MessageRoleAssistant {
				continue
			}
			if event.Message.Role == pi.MessageRoleAssistant && !event.Message.CommitLike {
				continue
			}
			out = append(out, sessionHandoffEntry{Line: row.line, SourceID: sourceID, ParentID: parentID, Role: string(event.Message.Role), Kind: "message", Text: text})
		case pi.EventKindTool:
			if event.Tool == nil {
				continue
			}
			kind := "tool_call"
			if event.Tool.Result {
				kind = "tool_result"
			}
			out = append(out, sessionHandoffEntry{
				Line:       row.line,
				SourceID:   sourceID,
				ParentID:   parentID,
				Kind:       kind,
				ToolCallID: strings.TrimSpace(event.Tool.CallID),
				ToolName:   strings.TrimSpace(event.Tool.Name),
				IsError:    event.Tool.IsError,
				Text:       strings.TrimSpace(event.Tool.Text),
				Arguments:  event.Tool.Arguments,
			})
		case pi.EventKindError:
			if event.Error == nil || strings.TrimSpace(event.Error.Message) == "" {
				continue
			}
			out = append(out, sessionHandoffEntry{Line: row.line, SourceID: sourceID, ParentID: parentID, Kind: "error", IsError: true, Text: strings.TrimSpace(event.Error.Message)})
		}
	}
	return out
}

func maskOldHandoffToolResults(entries []sessionHandoffEntry) int {
	remainingUserTurns := handoffRecentUserTurns
	masked := 0
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == "message" && entries[i].Role == string(pi.MessageRoleUser) {
			if remainingUserTurns > 0 {
				remainingUserTurns--
				continue
			}
		}
		if remainingUserTurns == 0 && entries[i].Kind == "tool_result" && strings.TrimSpace(entries[i].Text) != "" {
			entries[i].OriginalSize = len(entries[i].Text)
			entries[i].Text = handoffMaskedToolText
			entries[i].Masked = true
			entries[i].MaskReason = "older_than_recent_user_turn_window"
			masked++
		}
	}
	return masked
}

func buildSessionHandoffL2(entries []sessionHandoffEntry) sessionHandoffL2 {
	messages := make([]sessionHandoffL2Item, 0)
	for _, entry := range entries {
		if entry.Kind != "message" {
			continue
		}
		role := strings.TrimSpace(entry.Role)
		if role != string(pi.MessageRoleUser) && role != string(pi.MessageRoleAssistant) {
			continue
		}
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		id := fmt.Sprintf("m%d", len(messages)+1)
		messages = append(messages, sessionHandoffL2Item{
			ID:       id,
			Role:     role,
			Text:     text,
			Position: len(messages) + 1,
			Source: sessionHandoffSource{
				Line:     entry.Line,
				SourceID: entry.SourceID,
				ParentID: entry.ParentID,
				Role:     role,
			},
		})
	}
	return sessionHandoffL2{
		Version:     handoffL2FormatVersion,
		Kind:        "actrail_handoff_l2_transcript",
		Description: "User and committed assistant messages only. This is the primary semantic source for L3 anchors.",
		Messages:    messages,
	}
}

func buildFallbackSessionHandoffL3(l2 sessionHandoffL2, generationError string) sessionHandoffL3 {
	lastUser := lastL2MessageByRole(l2.Messages, string(pi.MessageRoleUser))
	lastAssistant := lastL2MessageByRole(l2.Messages, string(pi.MessageRoleAssistant))
	current := sessionHandoffCurrentState{
		Status:            "handoff_ready",
		WorkingAssumption: "L3 is a navigation map. L2 is the primary semantic source for user and assistant wording.",
	}
	if lastUser != nil {
		current.Goal = textSnippet(lastUser.Text, 180)
		current.LatestUserIntent = textSnippet(lastUser.Text, 320)
		current.RecommendedNextStep = "Read " + lastUser.ID + " in L2, then continue from that user instruction."
	}
	if current.Goal == "" && lastAssistant != nil {
		current.Goal = "Continue from the latest assistant-visible work in L2."
		current.RecommendedNextStep = "Read the latest L2 messages, then continue the session."
	}
	mustRead := make([]sessionHandoffMustRead, 0, 2)
	if lastUser != nil {
		mustRead = append(mustRead, sessionHandoffMustRead{
			L2ID:   lastUser.ID,
			Reason: "Latest user instruction; exact wording may contain constraints not captured by L3.",
			When:   "Before continuing the task.",
		})
	}
	if lastAssistant != nil && (lastUser == nil || lastAssistant.Position > lastUser.Position) {
		mustRead = append(mustRead, sessionHandoffMustRead{
			L2ID:   lastAssistant.ID,
			Reason: "Latest assistant response may contain current status or next-step context.",
			When:   "Before deciding whether to continue, revise, or verify prior work.",
		})
	}
	anchors := anchorsForL2Items(lastUser, lastAssistant)
	tree := []sessionHandoffTreeNode{}
	if len(anchors) > 0 {
		tree = append(tree, sessionHandoffTreeNode{
			ID:      "t1",
			Title:   "Current handoff thread",
			Type:    "current_thread",
			Status:  "active",
			Summary: "Fallback L3 could not infer a full conversation tree. Use the anchored L2 messages as the source of truth.",
			Anchors: anchors,
		})
	}
	active := []sessionHandoffThread{}
	if lastUser != nil {
		active = append(active, sessionHandoffThread{
			ThreadID:    "t1",
			Question:    textSnippet(lastUser.Text, 240),
			WhyActive:   "This is the latest user message in L2.",
			ReadL2First: []string{lastUser.ID},
		})
	}
	l3 := sessionHandoffL3{
		Version:          handoffL3FormatVersion,
		Kind:             "actrail_handoff_l3_summary",
		GeneratedBy:      sessionHandoffGenerator{Type: "deterministic_fallback"},
		CurrentState:     current,
		ConversationTree: tree,
		ActiveThreads:    active,
		MustReadL2:       mustRead,
		RiskNotes: []sessionHandoffRisk{
			{
				Risk:       "Treating L3 as complete context.",
				Mitigation: "Use L3 only as a map. Read the referenced L2 messages before acting on user intent, constraints, or decisions.",
			},
			{
				Risk:       "Missing tool or evidence details.",
				Mitigation: "Use L1 entries for tool calls, tool results, errors, and masked evidence details.",
			},
		},
		GenerationError: strings.TrimSpace(generationError),
	}
	if lastUser != nil {
		l3.NextActions = []sessionHandoffFinding{{
			Text:       "Continue from the latest user instruction after reading its exact L2 wording.",
			Source:     "deterministic",
			Confidence: "medium",
			Anchors:    []sessionHandoffL2Anchor{{L2ID: lastUser.ID, Role: lastUser.Role, Reason: "latest user instruction"}},
		}}
	}
	return l3
}

func anchorsForL2Items(items ...*sessionHandoffL2Item) []sessionHandoffL2Anchor {
	anchors := make([]sessionHandoffL2Anchor, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.ID) == "" || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		anchors = append(anchors, sessionHandoffL2Anchor{L2ID: item.ID, Role: item.Role, Reason: "fallback current-context anchor"})
	}
	return anchors
}

func lastL2MessageByRole(items []sessionHandoffL2Item, role string) *sessionHandoffL2Item {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role == role {
			return &items[i]
		}
	}
	return nil
}

func textSnippet(text string, maxRunes int) string {
	trimmed := strings.TrimSpace(text)
	if maxRunes <= 0 || len([]rune(trimmed)) <= maxRunes {
		return trimmed
	}
	runes := []rune(trimmed)
	return strings.TrimSpace(string(runes[:maxRunes])) + "..."
}

func validateSessionHandoffL3(l3 sessionHandoffL3, l2 sessionHandoffL2) error {
	if l3.Version == 0 {
		l3.Version = handoffL3FormatVersion
	}
	ids := make(map[string]sessionHandoffL2Item, len(l2.Messages))
	for _, item := range l2.Messages {
		ids[item.ID] = item
	}
	if len(ids) == 0 {
		return nil
	}
	checkAnchor := func(anchor sessionHandoffL2Anchor) error {
		id := strings.TrimSpace(anchor.L2ID)
		if id == "" {
			return fmt.Errorf("empty l2 anchor")
		}
		if _, ok := ids[id]; !ok {
			return fmt.Errorf("unknown l2 anchor %q", id)
		}
		return nil
	}
	var walkTree func([]sessionHandoffTreeNode) error
	walkTree = func(nodes []sessionHandoffTreeNode) error {
		for _, node := range nodes {
			for _, anchor := range node.Anchors {
				if err := checkAnchor(anchor); err != nil {
					return err
				}
			}
			if err := walkTree(node.Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walkTree(l3.ConversationTree); err != nil {
		return err
	}
	for _, finding := range append(append(append(append([]sessionHandoffFinding{}, l3.Decisions...), l3.Constraints...), l3.OpenQuestions...), l3.NextActions...) {
		for _, anchor := range finding.Anchors {
			if err := checkAnchor(anchor); err != nil {
				return err
			}
		}
	}
	for _, item := range l3.MustReadL2 {
		if _, ok := ids[strings.TrimSpace(item.L2ID)]; !ok {
			return fmt.Errorf("unknown must_read_l2 id %q", item.L2ID)
		}
	}
	for _, thread := range l3.ActiveThreads {
		for _, id := range thread.ReadL2First {
			if _, ok := ids[strings.TrimSpace(id)]; !ok {
				return fmt.Errorf("unknown active thread read_l2_first id %q", id)
			}
		}
	}
	return nil
}

func defaultSessionHandoffL3Client() sessionHandoffL3Client {
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		if loaded, ok := readSecretEnvValue("DEEPSEEK_API_KEY"); ok {
			key = loaded
		}
	}
	if key == "" {
		return nil
	}
	return deepSeekHandoffL3Client{
		apiKey:     key,
		baseURL:    handoffDeepSeekBaseURL,
		model:      handoffDeepSeekModel,
		httpClient: http.DefaultClient,
		timeout:    handoffDeepSeekTimeout,
	}
}

type deepSeekHandoffL3Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	timeout    time.Duration
}

func (c deepSeekHandoffL3Client) SummarizeHandoff(ctx context.Context, l2 sessionHandoffL2) (sessionHandoffL3, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return sessionHandoffL3{}, fmt.Errorf("deepseek api key missing")
	}
	model := strings.TrimSpace(c.model)
	if model == "" {
		model = handoffDeepSeekModel
	}
	baseURL := strings.TrimRight(strings.TrimSpace(c.baseURL), "/")
	if baseURL == "" {
		baseURL = handoffDeepSeekBaseURL
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = handoffDeepSeekTimeout
	}
	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	prompt, err := handoffL3Prompt(l2)
	if err != nil {
		return sessionHandoffL3{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": handoffL3SystemPrompt()},
			{"role": "user", "content": prompt},
		},
		"temperature":     0,
		"response_format": map[string]string{"type": "json_object"},
	})
	if err != nil {
		return sessionHandoffL3{}, err
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return sessionHandoffL3{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.apiKey))
	res, err := client.Do(req)
	if err != nil {
		return sessionHandoffL3{}, err
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return sessionHandoffL3{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return sessionHandoffL3{}, fmt.Errorf("deepseek handoff summary http %d", res.StatusCode)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resBody, &parsed); err != nil {
		return sessionHandoffL3{}, fmt.Errorf("parse deepseek handoff response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return sessionHandoffL3{}, fmt.Errorf("deepseek handoff summary returned no content")
	}
	var l3 sessionHandoffL3
	if err := json.Unmarshal([]byte(strings.TrimSpace(parsed.Choices[0].Message.Content)), &l3); err != nil {
		return sessionHandoffL3{}, fmt.Errorf("parse deepseek l3 json: %w", err)
	}
	l3.Version = handoffL3FormatVersion
	l3.Kind = "actrail_handoff_l3_summary"
	l3.GeneratedBy = sessionHandoffGenerator{Type: "external_model", Model: model}
	return l3, nil
}

func handoffL3SystemPrompt() string {
	return strings.Join([]string{
		"You generate ActRail handoff L3 JSON summaries.",
		"L3 is a navigation map, not a replacement for original messages.",
		"Use only the provided L2 user/assistant transcript.",
		"Every important claim, decision, constraint, open question, next action, and tree node must anchor to existing L2 ids.",
		"Do not invent facts. If uncertain, mark it as an open question or risk note.",
		"Return strict JSON only.",
	}, "\n")
}

func handoffL3Prompt(l2 sessionHandoffL2) (string, error) {
	input := l2
	if handoffDeepSeekMaxInputChars > 0 {
		input.Messages = boundedL2Messages(input.Messages, handoffDeepSeekMaxInputChars)
	}
	body, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"Generate JSON for this schema:",
		`{"version":1,"kind":"actrail_handoff_l3_summary","current_state":{"goal":"","latest_user_intent":"","status":"","working_assumption":"","recommended_next_step":""},"conversation_tree":[{"id":"t1","title":"","type":"active_thread","status":"active","summary":"","anchors":[{"l2_id":"m1","role":"user","reason":""}],"children":[]}],"active_threads":[{"thread_id":"t1","question":"","why_active":"","read_l2_first":["m1"]}],"decisions":[{"text":"","source":"user|assistant|inferred","confidence":"high|medium|low","anchors":[{"l2_id":"m1","role":"user","reason":""}]}],"constraints":[],"open_questions":[],"next_actions":[],"must_read_l2":[{"l2_id":"m1","reason":"","when":""}],"risk_notes":[{"risk":"","mitigation":""}]}`,
		"Rules:",
		"- Preserve user constraints and latest user intent.",
		"- Keep L3 concise. Prefer pointers to L2 over restating long details.",
		"- Include must_read_l2 for latest user instruction and any nuanced user constraint.",
		"- Use only l2_id values that exist in the input.",
		"Input L2 JSON:",
		string(body),
	}, "\n"), nil
}

func boundedL2Messages(items []sessionHandoffL2Item, maxChars int) []sessionHandoffL2Item {
	if maxChars <= 0 {
		return items
	}
	total := 0
	start := len(items)
	for i := len(items) - 1; i >= 0; i-- {
		total += len(items[i].Text)
		start = i
		if total >= maxChars {
			break
		}
	}
	if start <= 0 {
		return items
	}
	return items[start:]
}

func readSecretEnvValue(key string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	paths := []string{filepath.Join(home, ".secret.env")}
	if home != "/root" {
		paths = append(paths, "/root/.secret.env")
	}
	for _, path := range paths {
		value, ok := readEnvFileValue(path, key)
		if ok {
			return value, true
		}
	}
	return "", false
}

func readEnvFileValue(path, key string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	prefix := key + "="
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		value = strings.Trim(value, `"'`)
		if value != "" {
			return value, true
		}
	}
	return "", false
}
