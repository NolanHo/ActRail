package pi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const millisThreshold = 9_999_999_999

var uiResolutionFallbackPattern = regexp.MustCompile(`"([^"]+)"\s*=\s*"([^"]+)"`)

type ParseError struct {
	Line int
	Err  error
}

func (e ParseError) Error() string {
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}

func (e ParseError) Unwrap() error {
	return e.Err
}

func ParseObjectJSON(data []byte) (Material, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return Material{}, err
	}
	return parseObject(raw), nil
}

func ParseJSONLBytes(data []byte) (Material, error) {
	return ParseJSONL(bytes.NewReader(data))
}

func ParseJSONL(r io.Reader) (Material, error) {
	reader := bufio.NewReader(r)
	var out Material
	lineNo := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return Material{}, err
		}
		if len(bytes.TrimSpace(line)) > 0 {
			lineNo++
			parsed, parseErr := ParseObjectJSON(line)
			if parseErr != nil {
				return Material{}, ParseError{Line: lineNo, Err: parseErr}
			}
			if out.Header == nil && parsed.Header != nil {
				header := *parsed.Header
				out.Header = &header
			}
			out.Events = append(out.Events, parsed.Events...)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
	}
}

func parseObject(raw map[string]any) Material {
	if raw == nil {
		return Material{}
	}
	if header := parseHeader(raw); header != nil {
		return Material{Header: header}
	}

	rawType := cleanString(raw["type"])
	entryTS, entryTSOK := timestampValue(raw)
	messagePayload := primaryMessagePayload(raw)
	messageTS, messageTSOK := timestampValue(messagePayload)
	ts := mergedTimestamp(entryTS, entryTSOK, messageTS, messageTSOK)

	var events []Event
	if messagePayload != nil {
		events = append(events, parseMessagePayload(raw, messagePayload, rawType, ts)...)
	}

	switch rawType {
	case "message.delta":
		if delta := cleanString(raw["delta"]); delta != "" {
			events = append(events, Event{
				Kind:      EventKindMessageDelta,
				Timestamp: ts,
				RawType:   rawType,
				RawID:     cleanString(raw["id"]),
				ParentID:  cleanString(raw["parentId"]),
				SessionID: extractSessionID(raw),
				TurnID:    extractTurnID(raw),
				Delta: &MessageDelta{
					Role: parseRole(cleanString(raw["role"])),
					Text: delta,
				},
			})
		}
	case "extension_ui_request":
		if request := parseExtensionUIRequest(raw); request != nil {
			events = append(events, newUIRequestEvent(raw, ts, request))
		}
	case "turn.started":
		events = append(events, parseRuntimeMessageEvent(raw, ts, MessageClassUserPrompt, true)...)
		events = append(events, newBoundaryEvent(raw, ts, Boundary{Kind: BoundaryKindTurnStarted, CommitLike: false, Reason: rawType}))
	case "turn.completed":
		events = append(events, parseRuntimeMessageEvent(raw, ts, MessageClassCommitted, true)...)
		events = append(events, newBoundaryEvent(raw, ts, Boundary{Kind: BoundaryKindTurnCompleted, CommitLike: true, Reason: rawType}))
	case "turn.failed", "turn.aborted":
		events = append(events, newBoundaryEvent(raw, ts, Boundary{Kind: BoundaryKindTurnAborted, CommitLike: false, Reason: rawType}))
	case "turn_end":
		events = append(events, newBoundaryEvent(raw, ts, Boundary{Kind: BoundaryKindTurnCompleted, CommitLike: false, Reason: rawType}))
	}

	return Material{Events: events}
}

func parseHeader(raw map[string]any) *Header {
	if cleanString(raw["type"]) != "session" {
		return nil
	}
	version, _ := intValue(raw["version"])
	ts, _ := timestampValue(raw)
	return &Header{
		SessionID:     firstNonEmpty(cleanString(raw["id"]), cleanString(raw["session_id"]), cleanString(raw["sessionId"])),
		Version:       version,
		CWD:           cleanString(raw["cwd"]),
		Timestamp:     ts,
		Provider:      cleanString(raw["provider"]),
		Model:         firstNonEmpty(cleanString(raw["modelId"]), cleanString(raw["model"])),
		ThinkingLevel: cleanString(raw["thinkingLevel"]),
	}
}

func parseMessagePayload(raw, payload map[string]any, rawType string, ts float64) []Event {
	role := cleanString(payload["role"])
	if role == "" {
		return nil
	}
	base := func(kind EventKind) Event {
		return Event{
			Kind:      kind,
			Timestamp: ts,
			RawType:   rawType,
			RawID:     cleanString(raw["id"]),
			ParentID:  cleanString(raw["parentId"]),
			SessionID: extractSessionID(raw),
			TurnID:    extractTurnID(raw),
		}
	}

	var events []Event
	switch role {
	case string(MessageRoleUser):
		if text := extractText(payload); text != "" {
			event := base(EventKindMessage)
			event.Message = &Message{
				ID:         cleanString(raw["id"]),
				Role:       MessageRoleUser,
				Text:       text,
				Class:      MessageClassUserPrompt,
				CommitLike: true,
			}
			events = append(events, event)
			events = append(events, newBoundaryFromBase(event, Boundary{Kind: BoundaryKindTurnStarted, CommitLike: false, Inferred: true, Reason: "user_message"}))
		}
	case string(MessageRoleAssistant):
		toolCalls := askUserToolCalls(payload)
		for _, request := range toolCalls {
			events = append(events, newUIRequestEvent(raw, ts, request))
		}
		if text := extractText(payload); text != "" {
			toolCount := assistantToolCallCount(payload)
			thinkingCount := assistantThinkingCount(payload)
			final := assistantIsFinal(payload)
			class := MessageClassNarration
			if final {
				class = MessageClassFinal
			}
			event := base(EventKindMessage)
			event.Message = &Message{
				ID:            cleanString(raw["id"]),
				Role:          MessageRoleAssistant,
				Text:          text,
				Class:         class,
				StopReason:    cleanString(payload["stopReason"]),
				ToolCallCount: toolCount,
				ThinkingCount: thinkingCount,
				CommitLike:    final,
			}
			events = append(events, event)
			if final {
				events = append(events, newBoundaryFromBase(event, Boundary{Kind: BoundaryKindTurnCompleted, CommitLike: true, Inferred: true, Reason: "assistant_message_final"}))
			}
		}
	case "toolResult":
		if resolution := parseAskUserResolution(payload); resolution != nil {
			event := base(EventKindUIResolved)
			event.UIResolved = resolution
			events = append(events, event)
		}
	}
	return events
}

func parseRuntimeMessageEvent(raw map[string]any, ts float64, class MessageClass, commitLike bool) []Event {
	text := cleanString(raw["text"])
	role := parseRole(cleanString(raw["role"]))
	if text == "" || role == "" {
		return nil
	}
	return []Event{{
		Kind:      EventKindMessage,
		Timestamp: ts,
		RawType:   cleanString(raw["type"]),
		RawID:     cleanString(raw["id"]),
		ParentID:  cleanString(raw["parentId"]),
		SessionID: extractSessionID(raw),
		TurnID:    extractTurnID(raw),
		Message: &Message{
			ID:         cleanString(raw["id"]),
			Role:       role,
			Text:       text,
			Class:      class,
			CommitLike: commitLike,
		},
	}}
}

func newUIRequestEvent(raw map[string]any, ts float64, request *UIRequest) Event {
	copied := *request
	copied.Options = copyOptions(request.Options)
	copied.Questions = copyQuestions(request.Questions)
	copied.Metadata = copyAnyMap(request.Metadata)
	return Event{
		Kind:      EventKindUIRequest,
		Timestamp: ts,
		RawType:   cleanString(raw["type"]),
		RawID:     cleanString(raw["id"]),
		ParentID:  cleanString(raw["parentId"]),
		SessionID: extractSessionID(raw),
		TurnID:    extractTurnID(raw),
		UIRequest: &copied,
	}
}

func newBoundaryEvent(raw map[string]any, ts float64, boundary Boundary) Event {
	return Event{
		Kind:      EventKindBoundary,
		Timestamp: ts,
		RawType:   cleanString(raw["type"]),
		RawID:     cleanString(raw["id"]),
		ParentID:  cleanString(raw["parentId"]),
		SessionID: extractSessionID(raw),
		TurnID:    extractTurnID(raw),
		Boundary:  &boundary,
	}
}

func newBoundaryFromBase(base Event, boundary Boundary) Event {
	return Event{
		Kind:      EventKindBoundary,
		Timestamp: base.Timestamp,
		RawType:   base.RawType,
		RawID:     base.RawID,
		ParentID:  base.ParentID,
		SessionID: base.SessionID,
		TurnID:    base.TurnID,
		Boundary:  &boundary,
	}
}

func primaryMessagePayload(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	if cleanString(raw["type"]) == "message" {
		if message := objectValue(raw["message"]); message != nil {
			return message
		}
	}
	if payload := objectValue(raw["payload"]); payload != nil && cleanString(payload["type"]) == "message" {
		return payload
	}
	if message := objectValue(raw["message"]); message != nil {
		return message
	}
	return nil
}

func extractSessionID(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	for _, key := range []string{"session_id", "sessionId"} {
		if value := cleanString(raw[key]); value != "" {
			return value
		}
	}
	if cleanString(raw["type"]) == "session" {
		if value := cleanString(raw["id"]); value != "" {
			return value
		}
	}
	if payload := objectValue(raw["payload"]); payload != nil {
		if value := extractSessionID(payload); value != "" {
			return value
		}
	}
	if message := objectValue(raw["message"]); message != nil {
		if value := extractSessionID(message); value != "" {
			return value
		}
	}
	return ""
}

func extractTurnID(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	for _, key := range []string{"turn_id", "turnId", "current_turn_id", "active_turn_id"} {
		if value := cleanString(raw[key]); value != "" {
			return value
		}
	}
	if payload := objectValue(raw["payload"]); payload != nil {
		if value := extractTurnID(payload); value != "" {
			return value
		}
	}
	if message := objectValue(raw["message"]); message != nil {
		if value := extractTurnID(message); value != "" {
			return value
		}
	}
	return ""
}

func mergedTimestamp(entryTS float64, entryOK bool, payloadTS float64, payloadOK bool) float64 {
	switch {
	case entryOK && payloadOK:
		if payloadTS > entryTS {
			return payloadTS
		}
		return entryTS
	case payloadOK:
		return payloadTS
	case entryOK:
		return entryTS
	default:
		return 0
	}
}

func timestampValue(raw map[string]any) (float64, bool) {
	if raw == nil {
		return 0, false
	}
	for _, key := range []string{"ts", "timestamp", "created_at", "updated_at"} {
		if value, ok := numericTimestamp(raw[key]); ok {
			return value, true
		}
		if text, ok := raw[key].(string); ok {
			if value, ok := parseTimeString(text); ok {
				return value, true
			}
		}
	}
	return 0, false
}

func numericTimestamp(v any) (float64, bool) {
	switch value := v.(type) {
	case int:
		ts := float64(value)
		if ts > millisThreshold {
			ts /= 1000
		}
		return ts, true
	case int64:
		ts := float64(value)
		if ts > millisThreshold {
			ts /= 1000
		}
		return ts, true
	case float64:
		ts := value
		if ts > millisThreshold {
			ts /= 1000
		}
		return ts, true
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			ts := float64(integer)
			if ts > millisThreshold {
				ts /= 1000
			}
			return ts, true
		}
		if flt, err := value.Float64(); err == nil {
			ts := flt
			if ts > millisThreshold {
				ts /= 1000
			}
			return ts, true
		}
	}
	return 0, false
}

func parseTimeString(raw string) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	if strings.HasSuffix(text, "Z") {
		text = strings.TrimSuffix(text, "Z") + "+00:00"
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0, false
	}
	return float64(parsed.UnixNano()) / float64(time.Second), true
}

func parseRole(raw string) MessageRole {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "user":
		return MessageRoleUser
	case "assistant":
		return MessageRoleAssistant
	default:
		return ""
	}
}

func extractText(payload map[string]any) string {
	content, _ := payload["content"].([]any)
	var parts []string
	for _, item := range content {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch cleanString(obj["type"]) {
		case "text", "output_text", "input_text":
			if text := stringValue(obj["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "")
	}
	return stringValue(payload["text"])
}

func assistantToolCallCount(payload map[string]any) int {
	content, _ := payload["content"].([]any)
	count := 0
	for _, item := range content {
		obj, ok := item.(map[string]any)
		if ok && cleanString(obj["type"]) == "toolCall" {
			count++
		}
	}
	return count
}

func assistantThinkingCount(payload map[string]any) int {
	content, _ := payload["content"].([]any)
	count := 0
	for _, item := range content {
		obj, ok := item.(map[string]any)
		if ok && cleanString(obj["type"]) == "thinking" {
			count++
		}
	}
	return count
}

func assistantIsFinal(payload map[string]any) bool {
	if extractText(payload) == "" {
		return false
	}
	toolCalls := assistantToolCallCount(payload)
	thinking := assistantThinkingCount(payload)
	stopReason := cleanString(payload["stopReason"])
	if toolCalls == 0 && thinking == 0 && stopReason != "toolUse" {
		return true
	}
	if stopReason != "" && stopReason != "toolUse" {
		return true
	}
	content, _ := payload["content"].([]any)
	for _, item := range content {
		obj, ok := item.(map[string]any)
		if !ok || cleanString(obj["type"]) != "text" {
			continue
		}
		signature := stringValue(obj["textSignature"])
		if signature == "" {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(signature), &parsed); err != nil {
			continue
		}
		if cleanString(parsed["phase"]) == "final_answer" {
			return true
		}
	}
	return false
}

func askUserToolCalls(payload map[string]any) []*UIRequest {
	if cleanString(payload["role"]) != "assistant" {
		return nil
	}
	content, _ := payload["content"].([]any)
	var requests []*UIRequest
	for _, item := range content {
		obj, ok := item.(map[string]any)
		if !ok || cleanString(obj["type"]) != "toolCall" {
			continue
		}
		name := cleanString(obj["name"])
		if name != "ask_user" && name != "AskUserQuestion" {
			continue
		}
		request := parseAskUserRequest(obj)
		if request != nil {
			requests = append(requests, request)
		}
	}
	return requests
}

func parseAskUserRequest(raw map[string]any) *UIRequest {
	args := toolArguments(raw["arguments"])
	if args == nil {
		args = map[string]any{}
	}
	question := cleanString(args["question"])
	context := cleanString(args["context"])
	options := normalizeOptions(args["options"])
	allowFreeform, allowFreeformOK := boolArg(args, "allow_freeform", "allowFreeform")
	allowMultiple, allowMultipleOK := boolArg(args, "allow_multiple", "allowMultiple")
	questions := normalizeQuestions(args["questions"])
	if len(questions) > 0 {
		if question == "" {
			question = questions[0].Prompt
		}
		if context == "" {
			context = questions[0].Header
		}
		if len(options) == 0 {
			options = copyOptions(questions[0].Options)
		}
		if !allowMultipleOK {
			allowMultiple = questions[0].MultiSelect
		}
	}
	if !allowFreeformOK {
		allowFreeform = true
	}
	if !allowMultipleOK {
		allowMultiple = false
	}
	metadata := copyAnyMap(objectValue(args["metadata"]))
	timeout := timeoutValue(args)
	return &UIRequest{
		RequestID:     cleanString(raw["id"]),
		Source:        UIRequestSourceAskUserTool,
		Kind:          UIRequestKindAskUser,
		Prompt:        question,
		Context:       context,
		Options:       options,
		Questions:     questions,
		AllowFreeform: allowFreeform,
		AllowMultiple: allowMultiple,
		TimeoutMS:     timeout,
		Metadata:      metadata,
		Interactive:   true,
	}
}

func parseExtensionUIRequest(raw map[string]any) *UIRequest {
	method := parseMethod(cleanString(raw["method"]))
	if method == "" {
		return nil
	}
	allowFreeform, ok := boolArg(raw, "allow_freeform", "allowFreeform")
	if !ok {
		allowFreeform = method == UIMethodSelect || method == UIMethodInput || method == UIMethodEditor
	}
	allowMultiple, ok := boolArg(raw, "allow_multiple", "allowMultiple")
	if !ok {
		allowMultiple = false
	}
	return &UIRequest{
		RequestID:     cleanString(raw["id"]),
		Source:        UIRequestSourceExtensionRPC,
		Kind:          UIRequestKindDialog,
		Method:        method,
		Title:         stringValue(raw["title"]),
		Message:       stringValue(raw["message"]),
		Prompt:        firstNonEmpty(stringValue(raw["question"]), stringValue(raw["message"]), stringValue(raw["title"])),
		Context:       stringValue(raw["context"]),
		Options:       normalizeOptions(raw["options"]),
		AllowFreeform: allowFreeform,
		AllowMultiple: allowMultiple,
		TimeoutMS:     timeoutValue(raw),
		Interactive:   true,
	}
}

func parseMethod(raw string) UIMethod {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(UIMethodSelect):
		return UIMethodSelect
	case string(UIMethodConfirm):
		return UIMethodConfirm
	case string(UIMethodInput):
		return UIMethodInput
	case string(UIMethodEditor):
		return UIMethodEditor
	default:
		return ""
	}
}

func parseAskUserResolution(payload map[string]any) *UIResolution {
	toolName := cleanString(payload["toolName"])
	if toolName != "ask_user" && toolName != "AskUserQuestion" {
		return nil
	}
	requestID := cleanString(payload["toolCallId"])
	if requestID == "" {
		return nil
	}
	resolution := &UIResolution{RequestID: requestID}
	details := objectValue(payload["details"])
	contentText := extractText(payload)
	var question string
	var allowMultiple bool
	if details != nil {
		resolution.Cancelled = boolValue(details["cancelled"])
		resolution.WasCustom = boolValue(details["wasCustom"])
		resolution.AnswersByQuestion = answersByQuestion(details)
		questions := normalizeQuestions(details["questions"])
		if len(questions) > 0 {
			question = questions[0].Prompt
			allowMultiple = questions[0].MultiSelect
		}
		applyDirectAnswer(resolution, details["answer"])
		if resolution.AnswerText == "" && len(resolution.AnswerValues) == 0 {
			applyResponseAnswer(resolution, objectValue(details["response"]))
		}
		if resolution.AnswerText == "" && len(resolution.AnswerValues) == 0 && len(resolution.AnswersByQuestion) > 0 {
			if question != "" {
				if answer, ok := resolution.AnswersByQuestion[question]; ok {
					resolution.AnswerText = answer
				}
			}
			if resolution.AnswerText == "" && len(resolution.AnswersByQuestion) == 1 {
				for _, answer := range resolution.AnswersByQuestion {
					resolution.AnswerText = answer
				}
			}
		}
		if resolution.AnswerText == "" && len(resolution.AnswerValues) == 0 {
			applyFallbackAnswer(resolution, question, allowMultiple, contentText)
		}
	}
	if toolName == "AskUserQuestion" && boolValue(payload["isError"]) && resolution.AnswerText == "" && len(resolution.AnswerValues) == 0 {
		lowered := strings.ToLower(contentText)
		resolution.PromptFallbackAvailable = strings.Contains(lowered, "cannot read properties of undefined") && strings.Contains(lowered, "answers")
	}
	if resolution.AnswersByQuestion == nil {
		resolution.AnswersByQuestion = map[string]string{}
	}
	return resolution
}

func applyDirectAnswer(resolution *UIResolution, raw any) {
	switch value := raw.(type) {
	case string:
		text := strings.TrimSpace(value)
		if text != "" {
			resolution.AnswerText = text
		}
	case []any:
		var values []string
		for _, item := range value {
			if text := cleanString(item); text != "" {
				values = append(values, text)
			}
		}
		if len(values) > 0 {
			resolution.AnswerValues = values
		}
	}
}

func applyResponseAnswer(resolution *UIResolution, response map[string]any) {
	if response == nil {
		return
	}
	kind := cleanString(response["kind"])
	if selections, ok := response["selections"].([]any); ok {
		var values []string
		for _, item := range selections {
			if text := cleanString(item); text != "" {
				values = append(values, text)
			}
		}
		if len(values) == 1 {
			resolution.AnswerText = values[0]
		} else if len(values) > 1 {
			resolution.AnswerValues = values
		}
		if kind == "custom" {
			resolution.WasCustom = true
		}
		return
	}
	if value := cleanString(response["value"]); value != "" {
		resolution.AnswerText = value
		if kind == "custom" {
			resolution.WasCustom = true
		}
		return
	}
	if comment := cleanString(response["comment"]); comment != "" {
		resolution.AnswerText = comment
		resolution.WasCustom = true
	}
}

func applyFallbackAnswer(resolution *UIResolution, question string, allowMultiple bool, contentText string) {
	if question == "" || strings.TrimSpace(contentText) == "" {
		return
	}
	matches := uiResolutionFallbackPattern.FindAllStringSubmatch(contentText, -1)
	if len(matches) == 0 {
		return
	}
	for _, match := range matches {
		if len(match) < 3 || match[1] != question {
			continue
		}
		if allowMultiple {
			resolution.AnswerValues = []string{match[2]}
			return
		}
		resolution.AnswerText = match[2]
		return
	}
}

func answersByQuestion(details map[string]any) map[string]string {
	answers, _ := details["answers"].(map[string]any)
	if len(answers) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(answers))
	for key, value := range answers {
		question := strings.TrimSpace(key)
		answer := cleanString(value)
		if question == "" || answer == "" {
			continue
		}
		out[question] = answer
	}
	return out
}

func normalizeQuestions(raw any) []UIQuestion {
	questions, _ := raw.([]any)
	if len(questions) == 0 {
		return nil
	}
	out := make([]UIQuestion, 0, len(questions))
	for _, item := range questions {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prompt := stringValue(obj["question"])
		if prompt == "" {
			continue
		}
		multiSelect, ok := boolArg(obj, "allow_multiple", "allowMultiple", "multiSelect")
		if !ok {
			multiSelect = false
		}
		out = append(out, UIQuestion{
			Header:      stringValue(obj["header"]),
			Prompt:      prompt,
			Options:     normalizeOptions(obj["options"]),
			MultiSelect: multiSelect,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeOptions(raw any) []UIOption {
	items, _ := raw.([]any)
	if len(items) == 0 {
		return nil
	}
	out := make([]UIOption, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case string:
			text := strings.TrimSpace(value)
			if text == "" {
				continue
			}
			out = append(out, UIOption{Label: text, Value: text})
		case map[string]any:
			label := firstNonEmpty(stringValue(value["label"]), stringValue(value["value"]), stringValue(value["name"]), stringValue(value["title"]))
			description := stringValue(value["description"])
			if label == "" && description == "" {
				continue
			}
			out = append(out, UIOption{
				Label:       firstNonEmpty(label, description),
				Value:       firstNonEmpty(stringValue(value["value"]), label),
				Description: description,
				Raw:         copyAnyMap(value),
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolArguments(raw any) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		return value
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil
		}
		return parsed
	default:
		return nil
	}
}

func timeoutValue(raw map[string]any) *int {
	for _, key := range []string{"timeout_ms", "timeoutMs", "timeout"} {
		if value, ok := intValue(raw[key]); ok {
			v := value
			return &v
		}
	}
	return nil
}

func boolArg(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		return boolValue(value), true
	}
	return false, false
}

func objectValue(v any) map[string]any {
	obj, _ := v.(map[string]any)
	return obj
}

func intValue(v any) (int, bool) {
	switch value := v.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func boolValue(v any) bool {
	value, _ := v.(bool)
	return value
}

func stringValue(v any) string {
	text, _ := v.(string)
	return text
}

func cleanString(v any) string {
	return strings.TrimSpace(stringValue(v))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func copyAnyMap(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		out[key] = value
	}
	return out
}

func copyOptions(raw []UIOption) []UIOption {
	if len(raw) == 0 {
		return nil
	}
	out := make([]UIOption, len(raw))
	for i := range raw {
		out[i] = raw[i]
		out[i].Raw = copyAnyMap(raw[i].Raw)
	}
	return out
}

func copyQuestions(raw []UIQuestion) []UIQuestion {
	if len(raw) == 0 {
		return nil
	}
	out := make([]UIQuestion, len(raw))
	for i := range raw {
		out[i] = raw[i]
		out[i].Options = copyOptions(raw[i].Options)
	}
	return out
}
