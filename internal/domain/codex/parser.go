package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimeevent "actrail/internal/domain/runtimeevent"
)

type Projection struct {
	Events      []runtimeevent.Event
	ThreadID    string
	SessionPath string
	TurnID      string
	ClearTurn   bool
	ProbeTurn   bool
	Busy        *bool
	Initialized bool
	Desynced    bool
	Model       string
	Usage       *ContextUsage
	Timing      *TurnTiming
}

type ContextUsage struct {
	UsedTokens  *int
	TotalTokens *int
	PercentUsed *int
}

type TurnTiming struct {
	StartedTS   float64
	LastEventTS *float64
}

type appServerLine struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type threadEnvelope struct {
	Thread struct {
		ID     string      `json:"id"`
		Path   string      `json:"path"`
		Status any         `json:"status"`
		Turns  []turnState `json:"turns"`
	} `json:"thread"`
}

type turnState struct {
	ID     string `json:"id"`
	Status any    `json:"status"`
}

type threadStatusChangedParams struct {
	ThreadID string `json:"threadId"`
	Status   any    `json:"status"`
}

type turnEnvelope struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
		Status   any    `json:"status"`
		Error    any    `json:"error"`
	} `json:"turn"`
}

type agentMessageDeltaParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type itemNotification struct {
	ThreadID string         `json:"threadId"`
	TurnID   string         `json:"turnId"`
	Item     map[string]any `json:"item"`
}

func DecodeAppServerLine(raw []byte) (Projection, bool) {
	var line appServerLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return Projection{}, false
	}
	if strings.TrimSpace(line.Method) != "" {
		switch strings.TrimSpace(line.Method) {
		case "thread/started":
			var params threadEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return Projection{}, true
			}
			projection := Projection{ThreadID: strings.TrimSpace(params.Thread.ID), SessionPath: strings.TrimSpace(params.Thread.Path)}
			if busy, ok := threadBusy(params.Thread.Status, params.Thread.Turns); ok {
				projection.Busy = &busy
			}
			if turnID := activeTurnID(params.Thread.Turns); turnID != "" {
				projection.TurnID = turnID
			} else if projection.Busy != nil && !*projection.Busy {
				projection.ClearTurn = true
			}
			return projection, true
		case "thread/status/changed":
			var params threadStatusChangedParams
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return Projection{}, true
			}
			projection := Projection{ThreadID: strings.TrimSpace(params.ThreadID)}
			if busy, ok := statusBusy(params.Status); ok {
				projection.Busy = &busy
				if !busy {
					projection.ClearTurn = true
				}
			}
			return projection, true
		case "turn/started":
			var params turnEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return Projection{}, true
			}
			turnID := strings.TrimSpace(params.Turn.ID)
			threadID := turnEnvelopeThreadID(params)
			projection := Projection{
				ThreadID: threadID,
				TurnID:   turnID,
				Events: []runtimeevent.Event{{
					Kind:     runtimeevent.EventKindBoundary,
					RawType:  line.Method,
					ThreadID: threadID,
					TurnID:   turnID,
					Boundary: &runtimeevent.Boundary{Kind: runtimeevent.BoundaryKindTurnStarted},
				}},
			}
			busy := true
			projection.Busy = &busy
			if timing := turnTiming(line.Params); timing != nil {
				projection.Timing = timing
			}
			return projection, true
		case "turn/completed":
			var params turnEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return Projection{}, true
			}
			turnID := strings.TrimSpace(params.Turn.ID)
			threadID := turnEnvelopeThreadID(params)
			events := []runtimeevent.Event{{
				Kind:     runtimeevent.EventKindBoundary,
				RawType:  line.Method,
				ThreadID: threadID,
				TurnID:   turnID,
				Boundary: &runtimeevent.Boundary{
					Kind:       runtimeevent.BoundaryKindTurnCompleted,
					CommitLike: true,
					Reason:     "turn_end",
				},
			}}
			if errEvent := turnErrorEvent(line.Method, line.Params, threadID, turnID); errEvent != nil {
				events = append([]runtimeevent.Event{*errEvent}, events...)
			}
			projection := Projection{ClearTurn: true, ThreadID: threadID, TurnID: turnID, Events: events}
			busy := false
			projection.Busy = &busy
			if timing := turnTiming(line.Params); timing != nil {
				projection.Timing = timing
			}
			return projection, true
		case "item/agentMessage/delta":
			var params agentMessageDeltaParams
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return Projection{}, true
			}
			if strings.TrimSpace(params.Delta) == "" {
				return Projection{}, true
			}
			return Projection{Events: []runtimeevent.Event{{
				Kind:     runtimeevent.EventKindMessageDelta,
				RawType:  line.Method,
				RawID:    strings.TrimSpace(params.ItemID),
				ThreadID: strings.TrimSpace(params.ThreadID),
				TurnID:   strings.TrimSpace(params.TurnID),
				Delta:    &runtimeevent.MessageDelta{Role: runtimeevent.MessageRoleAssistant, Text: params.Delta},
			}}}, true
		case "item/started":
			events := itemEvents(line.Method, line.Params, false)
			if len(events) == 0 {
				return Projection{}, true
			}
			return Projection{Events: events}, true
		case "item/completed":
			events := itemEvents(line.Method, line.Params, true)
			if len(events) == 0 {
				return Projection{}, true
			}
			projection := Projection{Events: events}
			for _, event := range events {
				if projection.ThreadID == "" {
					projection.ThreadID = strings.TrimSpace(event.ThreadID)
				}
				if projection.TurnID == "" {
					projection.TurnID = strings.TrimSpace(event.TurnID)
				}
				if event.Message != nil && event.Message.Role == runtimeevent.MessageRoleAssistant && event.Message.CommitLike && strings.TrimSpace(event.Message.StopReason) == "" {
					projection.ProbeTurn = true
				}
			}
			return projection, true
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			event := reasoningDeltaEvent(line.Method, line.Params)
			if event == nil {
				return Projection{}, true
			}
			return Projection{Events: []runtimeevent.Event{*event}}, true
		case "command/exec/outputDelta", "item/commandExecution/outputDelta", "item/fileChange/outputDelta", "item/mcpToolCall/progress":
			event := outputDeltaEvent(line.Method, line.Params)
			if event == nil {
				return Projection{}, true
			}
			return Projection{Events: []runtimeevent.Event{*event}}, true
		case "thread/tokenUsage/updated":
			usage := contextUsage(line.Params)
			if usage == nil {
				return Projection{}, true
			}
			return Projection{ThreadID: threadIDFromRawParams(line.Params), Usage: usage}, true
		case "model/rerouted":
			var params map[string]any
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return Projection{}, true
			}
			toModel := strings.TrimSpace(stringValue(params["toModel"]))
			threadID := strings.TrimSpace(stringValue(params["threadId"]))
			message := strings.TrimSpace(fmt.Sprintf("Codex rerouted model from %s to %s", stringValue(params["fromModel"]), toModel))
			projection := Projection{ThreadID: threadID, Model: toModel}
			if toModel != "" {
				projection.Events = []runtimeevent.Event{{Kind: runtimeevent.EventKindMessage, RawType: line.Method, ThreadID: threadID, TurnID: strings.TrimSpace(stringValue(params["turnId"])), Message: &runtimeevent.Message{Role: runtimeevent.MessageRoleAssistant, Text: message, StopReason: "status"}}}
			}
			return projection, true
		case "error", "warning", "guardianWarning", "guardian/warning", "configWarning", "deprecationNotice", "thread/realtime/error":
			event := diagnosticEvent(line.Method, line.Params)
			if event == nil {
				return Projection{}, true
			}
			return Projection{Events: []runtimeevent.Event{*event}}, true
		default:
			return Projection{}, true
		}
	}
	if len(line.Result) > 0 && string(line.Result) != "null" {
		var thread threadEnvelope
		if err := json.Unmarshal(line.Result, &thread); err == nil && strings.TrimSpace(thread.Thread.ID) != "" {
			projection := Projection{ThreadID: strings.TrimSpace(thread.Thread.ID), SessionPath: strings.TrimSpace(thread.Thread.Path)}
			if busy, ok := threadBusy(thread.Thread.Status, thread.Thread.Turns); ok {
				projection.Busy = &busy
			}
			if turnID := activeTurnID(thread.Thread.Turns); turnID != "" {
				projection.TurnID = turnID
			} else if projection.Busy != nil && !*projection.Busy {
				projection.ClearTurn = true
			}
			return projection, true
		}
		var turn turnEnvelope
		if err := json.Unmarshal(line.Result, &turn); err == nil && strings.TrimSpace(turn.Turn.ID) != "" {
			return Projection{ThreadID: turnEnvelopeThreadID(turn), TurnID: strings.TrimSpace(turn.Turn.ID)}, true
		}
		return Projection{Initialized: true}, true
	}
	if alreadyInitializedError(line.Error) {
		return Projection{Initialized: true}, true
	}
	if notInitializedError(line.Error) {
		return Projection{Desynced: true}, true
	}
	return Projection{}, false
}

func alreadyInitializedError(raw json.RawMessage) bool {
	return errorMessageContains(raw, "already initialized")
}

func notInitializedError(raw json.RawMessage) bool {
	return errorMessageContains(raw, "not initialized")
}

func errorMessageContains(raw json.RawMessage, needle string) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(payload.Message)), strings.ToLower(strings.TrimSpace(needle)))
}

func turnEnvelopeThreadID(params turnEnvelope) string {
	if threadID := strings.TrimSpace(params.ThreadID); threadID != "" {
		return threadID
	}
	return strings.TrimSpace(params.Turn.ThreadID)
}

func threadIDFromRawParams(raw json.RawMessage) string {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return ""
	}
	return strings.TrimSpace(stringValue(params["threadId"]))
}

func itemEvents(method string, raw json.RawMessage, completed bool) []runtimeevent.Event {
	var params itemNotification
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil
	}
	item := params.Item
	itemType := strings.TrimSpace(stringValue(item["type"]))
	itemID := strings.TrimSpace(stringValue(item["id"]))
	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(stringValue(item["threadId"]))
	}
	turnID := strings.TrimSpace(params.TurnID)
	if turnID == "" {
		turnID = strings.TrimSpace(stringValue(item["turnId"]))
	}
	switch itemType {
	case "agentMessage":
		if !completed {
			return nil
		}
		text := strings.TrimSpace(stringValue(item["text"]))
		if text == "" {
			return nil
		}
		return []runtimeevent.Event{{Kind: runtimeevent.EventKindMessage, RawType: method, RawID: itemID, ThreadID: threadID, TurnID: turnID, Message: &runtimeevent.Message{ID: itemID, Role: runtimeevent.MessageRoleAssistant, Text: text, Class: runtimeevent.MessageClassCommitted, CommitLike: true}}}
	case "reasoning", "plan":
		text := reasoningText(item)
		if text == "" {
			return nil
		}
		event := reasoningEvent(method, itemID, turnID, text)
		event.ThreadID = threadID
		return []runtimeevent.Event{event}
	case "userMessage":
		if !completed {
			return nil
		}
		text := strings.TrimSpace(stringValue(item["text"]))
		if text == "" {
			text = jsonSummary(item["content"])
		}
		if text == "" {
			return nil
		}
		return []runtimeevent.Event{{Kind: runtimeevent.EventKindMessage, RawType: method, RawID: itemID, ThreadID: threadID, TurnID: turnID, Message: &runtimeevent.Message{ID: itemID, Role: runtimeevent.MessageRoleUser, Text: text, Class: runtimeevent.MessageClassUserPrompt, CommitLike: true}}}
	default:
		if !toolLikeItem(itemType) {
			return nil
		}
		tool := toolEventFromItem(itemType, item, completed)
		if tool == nil {
			return nil
		}
		return []runtimeevent.Event{{Kind: runtimeevent.EventKindTool, RawType: method, RawID: itemID, ThreadID: threadID, TurnID: turnID, Tool: tool}}
	}
}

func toolLikeItem(itemType string) bool {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch", "imageView", "imageGeneration", "contextCompaction", "enteredReviewMode", "exitedReviewMode":
		return true
	default:
		return false
	}
}

func toolEventFromItem(itemType string, item map[string]any, completed bool) *runtimeevent.ToolEvent {
	itemID := strings.TrimSpace(stringValue(item["id"]))
	return &runtimeevent.ToolEvent{
		CallID:    itemID,
		Name:      toolName(itemType, item),
		Text:      toolText(itemType, item, completed),
		Arguments: toolArguments(itemType, item, completed),
		Result:    completed,
		IsError:   toolIsError(item),
	}
}

func toolName(itemType string, item map[string]any) string {
	switch itemType {
	case "mcpToolCall":
		server := strings.TrimSpace(stringValue(item["server"]))
		tool := strings.TrimSpace(stringValue(item["tool"]))
		if server != "" && tool != "" {
			return server + "." + tool
		}
		if tool != "" {
			return tool
		}
	case "dynamicToolCall":
		namespace := strings.TrimSpace(stringValue(item["namespace"]))
		tool := strings.TrimSpace(stringValue(item["tool"]))
		if namespace != "" && tool != "" {
			return namespace + "." + tool
		}
		if tool != "" {
			return tool
		}
	case "collabAgentToolCall":
		if tool := strings.TrimSpace(stringValue(item["tool"])); tool != "" {
			return "collabAgent." + tool
		}
	case "webSearch":
		return "webSearch"
	case "imageView":
		return "imageView"
	case "imageGeneration":
		return "imageGeneration"
	case "contextCompaction":
		return "contextCompaction"
	}
	if itemType != "" {
		return itemType
	}
	return "codex_tool"
}

func toolText(itemType string, item map[string]any, completed bool) string {
	if completed {
		for _, key := range []string{"aggregatedOutput", "result", "review"} {
			if text := strings.TrimSpace(stringValue(item[key])); text != "" {
				return text
			}
		}
		if text := jsonSummary(item["contentItems"]); text != "" {
			return text
		}
		if text := jsonSummary(item["changes"]); text != "" {
			return text
		}
		if errText := jsonSummary(item["error"]); errText != "" {
			return errText
		}
	}
	for _, key := range []string{"command", "query", "path", "prompt", "revisedPrompt"} {
		if text := strings.TrimSpace(stringValue(item[key])); text != "" {
			return text
		}
	}
	if status := strings.TrimSpace(stringValue(item["status"])); status != "" {
		return toolName(itemType, item) + ": " + status
	}
	return toolName(itemType, item)
}

func toolArguments(itemType string, item map[string]any, completed bool) map[string]any {
	args := map[string]any{}
	for _, key := range []string{"command", "cwd", "server", "tool", "namespace", "arguments", "query", "path", "changes", "status", "exitCode", "durationMs", "success", "processId", "source", "model", "reasoningEffort", "prompt"} {
		if value, ok := item[key]; ok && value != nil {
			args[key] = value
		}
	}
	if completed {
		for _, key := range []string{"aggregatedOutput", "result", "error", "contentItems", "savedPath"} {
			if value, ok := item[key]; ok && value != nil {
				args[key] = value
			}
		}
	}
	if len(args) == 0 && itemType != "" {
		args["type"] = itemType
	}
	return args
}

func toolIsError(item map[string]any) bool {
	status := strings.ToLower(strings.TrimSpace(stringValue(item["status"])))
	if strings.Contains(status, "fail") || strings.Contains(status, "error") || strings.Contains(status, "denied") {
		return true
	}
	if value, ok := item["success"].(bool); ok && !value {
		return true
	}
	if code := numericValue(item["exitCode"]); code != 0 {
		return true
	}
	errorValue, ok := item["error"]
	if !ok || errorValue == nil {
		return false
	}
	if text := strings.TrimSpace(stringValue(errorValue)); text != "" && text != "null" {
		return true
	}
	if record, _ := errorValue.(map[string]any); len(record) > 0 {
		return true
	}
	return false
}

func reasoningDeltaEvent(method string, raw json.RawMessage) *runtimeevent.Event {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil
	}
	text := strings.TrimSpace(stringValue(params["delta"]))
	if text == "" {
		return nil
	}
	itemID := strings.TrimSpace(stringValue(params["itemId"]))
	turnID := strings.TrimSpace(stringValue(params["turnId"]))
	event := reasoningEvent(method, itemID, turnID, text)
	event.ThreadID = strings.TrimSpace(stringValue(params["threadId"]))
	return &event
}

func reasoningEvent(method, itemID, turnID, text string) runtimeevent.Event {
	return runtimeevent.Event{
		Kind:    runtimeevent.EventKindMessage,
		RawType: method,
		RawID:   strings.TrimSpace(itemID),
		TurnID:  strings.TrimSpace(turnID),
		Message: &runtimeevent.Message{ID: strings.TrimSpace(itemID), Role: runtimeevent.MessageRoleAssistant, Text: text, Class: runtimeevent.MessageClassNarration, StopReason: "reasoning", CommitLike: true},
	}
}

func reasoningText(item map[string]any) string {
	for _, key := range []string{"text", "summary", "content"} {
		if text := jsonText(item[key]); text != "" {
			return text
		}
	}
	return ""
}

func outputDeltaEvent(method string, raw json.RawMessage) *runtimeevent.Event {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil
	}
	text := strings.TrimSpace(stringValue(params["delta"]))
	if text == "" {
		text = strings.TrimSpace(stringValue(params["message"]))
	}
	if text == "" {
		return nil
	}
	itemID := strings.TrimSpace(stringValue(params["itemId"]))
	if itemID == "" {
		itemID = strings.TrimSpace(stringValue(params["callId"]))
	}
	name := "codex_output"
	if strings.Contains(method, "command") {
		name = "commandExecution"
	} else if strings.Contains(method, "fileChange") {
		name = "fileChange"
	} else if strings.Contains(method, "mcpToolCall") {
		name = "mcpToolCall"
	}
	return &runtimeevent.Event{
		Kind:     runtimeevent.EventKindTool,
		RawType:  method,
		ThreadID: strings.TrimSpace(stringValue(params["threadId"])),
		TurnID:   strings.TrimSpace(stringValue(params["turnId"])),
		Tool:     &runtimeevent.ToolEvent{CallID: itemID, Name: name, Text: text, Result: true},
	}
}

func contextUsage(raw json.RawMessage) *ContextUsage {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil
	}
	tokenUsage, _ := params["tokenUsage"].(map[string]any)
	if tokenUsage == nil {
		tokenUsage, _ = params["usage"].(map[string]any)
	}
	if tokenUsage == nil {
		return nil
	}
	total, _ := tokenUsage["total"].(map[string]any)
	used := intValueFromAny(total["totalTokens"])
	if used <= 0 {
		used = intValueFromAny(total["inputTokens"]) + intValueFromAny(total["outputTokens"]) + intValueFromAny(total["reasoningOutputTokens"])
	}
	contextWindow := intValueFromAny(tokenUsage["modelContextWindow"])
	if used <= 0 && contextWindow <= 0 {
		return nil
	}
	usage := &ContextUsage{}
	if used > 0 {
		usage.UsedTokens = &used
	}
	if contextWindow > 0 {
		usage.TotalTokens = &contextWindow
	}
	if used > 0 && contextWindow > 0 {
		percent := int((float64(used)/float64(contextWindow))*100 + 0.5)
		usage.PercentUsed = &percent
	}
	return usage
}

func diagnosticEvent(method string, raw json.RawMessage) *runtimeevent.Event {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil
	}
	message := diagnosticMessage(params)
	if message == "" {
		return nil
	}
	turnID := strings.TrimSpace(stringValue(params["turnId"]))
	itemID := strings.TrimSpace(stringValue(params["itemId"]))
	if itemID == "" {
		itemID = strings.TrimSpace(stringValue(params["requestId"]))
	}
	return &runtimeevent.Event{
		Kind:     runtimeevent.EventKindError,
		RawType:  method,
		RawID:    itemID,
		ThreadID: strings.TrimSpace(stringValue(params["threadId"])),
		TurnID:   turnID,
		Error:    &runtimeevent.ErrorMessage{Message: message, Source: "codex_app_server", StopReason: method},
	}
}

func diagnosticMessage(params map[string]any) string {
	if text := strings.TrimSpace(stringValue(params["message"])); text != "" {
		return text
	}
	if text := strings.TrimSpace(stringValue(params["reason"])); text != "" {
		return text
	}
	if errRecord, _ := params["error"].(map[string]any); errRecord != nil {
		if text := strings.TrimSpace(stringValue(errRecord["message"])); text != "" {
			return text
		}
		return jsonSummary(errRecord)
	}
	if errText := strings.TrimSpace(stringValue(params["error"])); errText != "" {
		return errText
	}
	return jsonSummary(params)
}

func turnErrorEvent(method string, raw json.RawMessage, threadID, turnID string) *runtimeevent.Event {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil
	}
	turn, _ := params["turn"].(map[string]any)
	if turn == nil {
		return nil
	}
	status := strings.ToLower(strings.TrimSpace(stringValue(turn["status"])))
	if !strings.Contains(status, "fail") && !strings.Contains(status, "error") && !strings.Contains(status, "abort") {
		return nil
	}
	message := "Codex turn failed"
	if errRecord, _ := turn["error"].(map[string]any); errRecord != nil {
		if text := strings.TrimSpace(stringValue(errRecord["message"])); text != "" {
			message = text
		}
	} else if text := strings.TrimSpace(stringValue(turn["error"])); text != "" {
		message = text
	}
	return &runtimeevent.Event{Kind: runtimeevent.EventKindError, RawType: method, ThreadID: strings.TrimSpace(threadID), TurnID: turnID, Error: &runtimeevent.ErrorMessage{Message: message, Source: "codex_app_server", StopReason: status}}
}

func turnTiming(raw json.RawMessage) *TurnTiming {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil
	}
	turn, _ := params["turn"].(map[string]any)
	if turn == nil {
		return nil
	}
	timing := &TurnTiming{}
	if started := numericValue(turn["startedAt"]); started > 0 {
		timing.StartedTS = normalizeTimestampSeconds(started)
	}
	if completed := numericValue(turn["completedAt"]); completed > 0 {
		value := normalizeTimestampSeconds(completed)
		timing.LastEventTS = &value
	}
	if timing.StartedTS == 0 && timing.LastEventTS == nil {
		return nil
	}
	return timing
}

func jsonText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := jsonText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]any:
		for _, key := range []string{"text", "content", "message", "summary"} {
			if text := jsonText(v[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func jsonSummary(value any) string {
	if text := jsonText(value); text != "" {
		return text
	}
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(encoded))
}

func threadBusy(status any, turns []turnState) (bool, bool) {
	if busy, ok := statusBusy(status); ok {
		return busy, true
	}
	for _, turn := range turns {
		if turnInProgress(turn.Status) {
			return true, true
		}
	}
	if turns != nil {
		return false, true
	}
	return false, false
}

func activeTurnID(turns []turnState) string {
	for index := len(turns) - 1; index >= 0; index-- {
		if turnInProgress(turns[index].Status) {
			return strings.TrimSpace(turns[index].ID)
		}
	}
	return ""
}

func statusBusy(status any) (bool, bool) {
	switch v := status.(type) {
	case string:
		switch strings.TrimSpace(v) {
		case "active", "inProgress":
			return true, true
		case "idle", "notLoaded", "systemError", "completed", "interrupted", "failed":
			return false, true
		}
	case map[string]any:
		switch strings.TrimSpace(stringValue(v["type"])) {
		case "active", "inProgress":
			return true, true
		case "idle", "notLoaded", "systemError", "completed", "interrupted", "failed":
			return false, true
		}
	}
	return false, false
}

func turnInProgress(status any) bool {
	busy, ok := statusBusy(status)
	return ok && busy
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func numericValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func intValueFromAny(value any) int {
	v := numericValue(value)
	if v <= 0 {
		return 0
	}
	return int(v + 0.5)
}

func normalizeTimestampSeconds(value float64) float64 {
	if value > 9_999_999_999 {
		return value / 1000
	}
	return value
}
