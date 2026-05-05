package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	"actrail/internal/httpapi/authn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type Router struct {
	cfg     config.Config
	app     app.Service
	ws      http.Handler
	connect http.Handler
}

type authStatus struct {
	OK bool `json:"ok"`
}

type loginRequest struct {
	Password string `json:"password"`
}

type renameSessionRequest struct {
	Name *string `json:"name"`
}

type focusSessionRequest struct {
	Focused *bool `json:"focused"`
}

type executeSessionCommandRequest struct {
	Command string `json:"command"`
	Name    string `json:"name"`
	Args    string `json:"args"`
}

type answerWaitRequest struct {
	Answer string `json:"answer"`
}

type supervisorProviderRequest struct {
	BaseURL string  `json:"base_url"`
	APIKey  *string `json:"api_key"`
	Model   string  `json:"model"`
}

type sessionSupervisorRequest struct {
	Enabled                  *bool     `json:"enabled"`
	IdleAfterMinutes         *int      `json:"idle_after_minutes"`
	MaxConsecutiveInjections *int      `json:"max_consecutive_injections"`
	ConsecutiveInjections    *int      `json:"consecutive_injections"`
	Goal                     *string   `json:"goal"`
	AcceptanceCriteria       *string   `json:"acceptance_criteria"`
	ContextFiles             *[]string `json:"context_files"`
}

type supervisorRunOnceRequest struct {
	DryRun bool `json:"dry_run"`
}

type schedulerSettingsRequest struct {
	IdleBeforeDeliverySeconds *int `json:"idle_before_delivery_seconds"`
}

type setAlarmRequest struct {
	DurationSeconds int    `json:"duration_seconds"`
	Title           string `json:"title"`
	Message         string `json:"message"`
}

func New(cfg config.Config, svc app.Service, wsHandler http.Handler, connectHandlers ...http.Handler) http.Handler {
	var connectHandler http.Handler
	if len(connectHandlers) > 0 {
		connectHandler = connectHandlers[0]
	}
	if connectHandler == nil {
		connectHandler = http.NotFoundHandler()
	}
	r := Router{cfg: cfg, app: svc, ws: wsHandler, connect: connectHandler}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", r.healthz)
	mux.HandleFunc("GET /api/me", r.me)
	mux.HandleFunc("POST /api/login", r.login)
	mux.HandleFunc("POST /api/logout", r.logout)
	mux.Handle("GET /api/bootstrap", r.requireAuth(http.HandlerFunc(r.bootstrap)))
	mux.Handle("POST /team/command", r.requireAuth(http.HandlerFunc(r.teamCommand)))
	mux.Handle("GET /team/events", r.requireAuth(http.HandlerFunc(r.teamEvents)))
	mux.Handle("GET /api/sessions", r.requireAuth(http.HandlerFunc(r.listSessions)))
	mux.Handle("POST /api/sessions", r.requireAuth(http.HandlerFunc(r.createSession)))
	mux.Handle("GET /api/teams", r.requireAuth(http.HandlerFunc(r.listSubagents)))
	mux.Handle("POST /api/teams/spawn", r.requireAuth(http.HandlerFunc(r.spawnSubagent)))
	mux.Handle("POST /api/teams/{actor_id}/prompt", r.requireAuth(http.HandlerFunc(r.promptSubagent)))
	mux.Handle("POST /api/teams/{actor_id}/followup", r.requireAuth(http.HandlerFunc(r.followupSubagent)))
	mux.Handle("POST /api/teams/{actor_id}/send", r.requireAuth(http.HandlerFunc(r.sendSubagent)))
	mux.Handle("POST /api/teams/{actor_id}/ask_parent", r.requireAuth(http.HandlerFunc(r.askParent)))
	mux.Handle("POST /api/teams/{actor_id}/ask_parent/resume", r.requireAuth(http.HandlerFunc(r.resumeAskParent)))
	mux.Handle("POST /api/teams/{actor_id}/answer", r.requireAuth(http.HandlerFunc(r.answerSubagent)))
	mux.Handle("POST /api/teams/{actor_id}/abort", r.requireAuth(http.HandlerFunc(r.abortSubagent)))
	mux.Handle("POST /api/teams/{actor_id}/close", r.requireAuth(http.HandlerFunc(r.closeSubagent)))
	mux.Handle("GET /api/teams/{actor_id}/events", r.requireAuth(http.HandlerFunc(r.subagentEvents)))
	mux.Handle("GET /api/session_resume_candidates", r.requireAuth(http.HandlerFunc(r.sessionResumeCandidates)))
	mux.Handle("GET /api/settings/voice", r.requireAuth(http.HandlerFunc(r.voiceSettings)))
	mux.Handle("POST /api/settings/voice", r.requireAuth(http.HandlerFunc(r.updateVoiceSettings)))
	mux.Handle("POST /api/settings/voice/test_provider", r.requireAuth(http.HandlerFunc(r.testVoiceProvider)))
	mux.Handle("GET /api/supervisor/provider", r.requireAuth(http.HandlerFunc(r.supervisorProvider)))
	mux.Handle("POST /api/supervisor/provider", r.requireAuth(http.HandlerFunc(r.updateSupervisorProvider)))
	mux.Handle("GET /api/sessions/{session_id}/details", r.requireAuth(http.HandlerFunc(r.sessionDetails)))
	mux.Handle("GET /api/sessions/{session_id}/messages", r.requireAuth(http.HandlerFunc(r.sessionMessages)))
	mux.Handle("GET /api/sessions/{session_id}/supervisor", r.requireAuth(http.HandlerFunc(r.sessionSupervisor)))
	mux.Handle("POST /api/sessions/{session_id}/supervisor", r.requireAuth(http.HandlerFunc(r.updateSessionSupervisor)))
	mux.Handle("GET /api/sessions/{session_id}/supervisor/runs", r.requireAuth(http.HandlerFunc(r.supervisorRuns)))
	mux.Handle("POST /api/sessions/{session_id}/supervisor/run-once", r.requireAuth(http.HandlerFunc(r.runSupervisorOnce)))
	mux.Handle("GET /api/scheduler", r.requireAuth(http.HandlerFunc(r.schedulerSnapshot)))
	mux.Handle("POST /api/scheduler/settings", r.requireAuth(http.HandlerFunc(r.updateSchedulerSettings)))
	mux.Handle("GET /api/sessions/{session_id}/inbox", r.requireAuth(http.HandlerFunc(r.sessionInbox)))
	mux.Handle("POST /api/sessions/{session_id}/alarms", r.requireAuth(http.HandlerFunc(r.setSessionAlarm)))
	mux.Handle("GET /api/sessions/{session_id}/state", r.requireAuth(http.HandlerFunc(r.sessionState)))
	mux.Handle("POST /api/sessions/{session_id}/state/probe", r.requireAuth(http.HandlerFunc(r.probeSessionState)))
	mux.Handle("GET /api/sessions/{session_id}/workspace", r.requireAuth(http.HandlerFunc(r.sessionWorkspace)))
	mux.Handle("POST /api/sessions/{session_id}/workspace", r.requireAuth(http.HandlerFunc(r.updateSessionWorkspace)))
	mux.Handle("GET /api/sessions/{session_id}/file/list", r.requireAuth(http.HandlerFunc(r.workspaceFileList)))
	mux.Handle("GET /api/sessions/{session_id}/file/read", r.requireAuth(http.HandlerFunc(r.workspaceFileRead)))
	mux.Handle("GET /api/sessions/{session_id}/git/file_versions", r.requireAuth(http.HandlerFunc(r.gitFileVersions)))
	mux.Handle("GET /api/sessions/{session_id}/commands", r.requireAuth(http.HandlerFunc(r.sessionCommands)))
	mux.Handle("POST /api/sessions/{session_id}/commands", r.requireAuth(http.HandlerFunc(r.executeSessionCommand)))
	mux.Handle("GET /api/waits/inbox", r.requireAuth(http.HandlerFunc(r.waitInbox)))
	mux.Handle("GET /api/sessions/{session_id}/waits/threads", r.requireAuth(http.HandlerFunc(r.waitThreads)))
	mux.Handle("GET /api/sessions/{session_id}/waits/threads/{thread_id}", r.requireAuth(http.HandlerFunc(r.waitThread)))
	mux.Handle("POST /api/sessions/{session_id}/waits", r.requireAuth(http.HandlerFunc(r.createWait)))
	mux.Handle("POST /api/sessions/{session_id}/waits/{wait_id}/claim", r.requireAuth(http.HandlerFunc(r.claimWait)))
	mux.Handle("POST /api/sessions/{session_id}/waits/{wait_id}/answer", r.requireAuth(http.HandlerFunc(r.answerWait)))
	mux.Handle("POST /api/sessions/{session_id}/waits/{wait_id}/cancel", r.requireAuth(http.HandlerFunc(r.cancelWait)))
	mux.Handle("POST /api/sessions/{session_id}/rename", r.requireAuth(http.HandlerFunc(r.renameSession)))
	mux.Handle("POST /api/sessions/{session_id}/focus", r.requireAuth(http.HandlerFunc(r.focusSession)))
	mux.Handle("POST /api/sessions/{session_id}/edit", r.requireAuth(http.HandlerFunc(r.editSession)))
	mux.Handle("POST /api/cwd_groups/edit", r.requireAuth(http.HandlerFunc(r.editCwdGroup)))
	mux.Handle("POST /api/sessions/{session_id}/model", r.requireAuth(http.HandlerFunc(r.switchSessionModel)))
	mux.Handle("POST /api/sessions/{session_id}/delete", r.requireAuth(http.HandlerFunc(r.deleteSession)))
	mux.Handle("POST /api/sessions/{session_id}/restart", r.requireAuth(http.HandlerFunc(r.restartSession)))
	mux.Handle("POST /api/sessions/{session_id}/handoff", r.requireAuth(http.HandlerFunc(r.handoffSession)))
	mux.Handle("GET /api/ws", r.requireAuth(r.ws))
	mux.Handle("/api/connect/", r.requireAuth(r.connect))

	return mux
}

func (r Router) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !authn.Authenticated(req, r.cfg.Auth) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid auth cookie required", "")
			return
		}
		next.ServeHTTP(w, req)
	})
}

func (r Router) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (r Router) me(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, authStatus{OK: authn.Authenticated(req, r.cfg.Auth)})
}

func (r Router) login(w http.ResponseWriter, req *http.Request) {
	var body loginRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	if strings.TrimSpace(body.Password) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "password required", "password")
		return
	}
	if !authn.Configured(r.cfg.Auth) {
		writeError(w, http.StatusNotImplemented, "unsupported", "password auth not configured", "")
		return
	}
	if !authn.PasswordMatches(r.cfg.Auth, body.Password) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid password", "")
		return
	}
	cookie, err := authn.SessionCookie(r.cfg.Auth)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
		return
	}
	http.SetCookie(w, cookie)
	writeJSON(w, http.StatusOK, authStatus{OK: true})
}

func (r Router) logout(w http.ResponseWriter, _ *http.Request) {
	cookie := &http.Cookie{
		Name:     r.cfg.Auth.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	}
	http.SetCookie(w, cookie)
	writeJSON(w, http.StatusOK, authStatus{OK: true})
}

func (r Router) bootstrap(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, r.app.Bootstrap(req.Context(), app.BootstrapRequest{
		RefreshPIModels: req.URL.Query().Get("refresh_pi_models") == "1",
	}))
}

func (r Router) listSessions(w http.ResponseWriter, req *http.Request) {
	offset, err := queryInt(req, "offset")
	if err != nil {
		writeAppError(w, err)
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	groupOffset, err := queryInt(req, "group_offset")
	if err != nil {
		writeAppError(w, err)
		return
	}
	groupLimit, err := queryInt(req, "group_limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.ListSessions(req.Context(), app.ListSessionsRequest{
		GroupKey:     strings.TrimSpace(req.URL.Query().Get("group_key")),
		Offset:       offset,
		Limit:        limit,
		GroupOffset:  groupOffset,
		GroupLimit:   groupLimit,
		AgentBackend: strings.TrimSpace(req.URL.Query().Get("agent_backend")),
		CWD:          strings.TrimSpace(req.URL.Query().Get("cwd")),
		Title:        strings.TrimSpace(req.URL.Query().Get("title")),
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) listSubagents(w http.ResponseWriter, req *http.Request) {
	includeClosed, err := queryBool(req, "include_closed")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.ListSubagents(req.Context(), app.ListSubagentsRequest{IncludeClosed: includeClosed})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) spawnSubagent(w http.ResponseWriter, req *http.Request) {
	var body app.SpawnSubagentRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.SpawnSubagent(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) promptSubagent(w http.ResponseWriter, req *http.Request) {
	var body app.PromptSubagentRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.ActorID = req.PathValue("actor_id")
	payload, err := r.app.PromptSubagent(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) followupSubagent(w http.ResponseWriter, req *http.Request) {
	var body app.FollowupSubagentRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.ActorID = req.PathValue("actor_id")
	payload, err := r.app.FollowupSubagent(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sendSubagent(w http.ResponseWriter, req *http.Request) {
	var body app.SendSubagentRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.ActorID = req.PathValue("actor_id")
	payload, err := r.app.SendSubagent(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) askParent(w http.ResponseWriter, req *http.Request) {
	var body app.AskParentRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.ActorID = req.PathValue("actor_id")
	payload, err := r.app.AskParent(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) resumeAskParent(w http.ResponseWriter, req *http.Request) {
	var body app.AskParentRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.ActorID = req.PathValue("actor_id")
	payload, err := r.app.ResumeAskParent(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) answerSubagent(w http.ResponseWriter, req *http.Request) {
	var body app.AnswerSubagentRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.ActorID = req.PathValue("actor_id")
	payload, err := r.app.AnswerSubagent(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) abortSubagent(w http.ResponseWriter, req *http.Request) {
	var body app.AbortSubagentRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.ActorID = req.PathValue("actor_id")
	payload, err := r.app.AbortSubagent(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) closeSubagent(w http.ResponseWriter, req *http.Request) {
	payload, err := r.app.CloseSubagent(req.Context(), app.CloseSubagentRequest{ActorID: req.PathValue("actor_id")})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) subagentEvents(w http.ResponseWriter, req *http.Request) {
	payload, err := r.app.SubagentEvents(req.Context(), app.SubagentEventsRequest{ActorID: req.PathValue("actor_id"), AfterEventID: strings.TrimSpace(req.URL.Query().Get("after_event_id"))})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) createSession(w http.ResponseWriter, req *http.Request) {
	var body app.CreateSessionRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.CreateSession(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionResumeCandidates(w http.ResponseWriter, req *http.Request) {
	offset, err := queryInt(req, "offset")
	if err != nil {
		writeAppError(w, err)
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	backend := strings.TrimSpace(req.URL.Query().Get("backend"))
	if backend == "" {
		backend = strings.TrimSpace(req.URL.Query().Get("agent_backend"))
	}
	scanOffset, err := queryInt(req, "scan_offset")
	if err != nil {
		writeAppError(w, err)
		return
	}
	scanLimit, err := queryInt(req, "scan_limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.SessionResumeCandidates(req.Context(), app.SessionResumeCandidatesRequest{
		CWD:          strings.TrimSpace(req.URL.Query().Get("cwd")),
		AgentBackend: backend,
		Offset:       offset,
		Limit:        limit,
		ScanOffset:   scanOffset,
		ScanLimit:    scanLimit,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) supervisorProvider(w http.ResponseWriter, req *http.Request) {
	payload, err := r.app.SupervisorProvider(req.Context(), app.SupervisorProviderRequest{})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) updateSupervisorProvider(w http.ResponseWriter, req *http.Request) {
	var body supervisorProviderRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.UpdateSupervisorProvider(req.Context(), app.UpdateSupervisorProviderRequest{
		BaseURL: body.BaseURL,
		APIKey:  body.APIKey,
		Model:   body.Model,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionDetails(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.SessionDetails(req.Context(), app.SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionMessages(w http.ResponseWriter, req *http.Request) {
	ctx, span := otel.Tracer("actrail/http").Start(req.Context(), "http.sessionMessages")
	defer span.End()
	req = req.WithContext(ctx)
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	afterSeq, err := queryUint64(req, "after_seq")
	if err != nil {
		writeAppError(w, err)
		return
	}
	beforeSeq, err := queryUint64(req, "before_seq")
	if err != nil {
		writeAppError(w, err)
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	init, err := queryBool(req, "init")
	if err != nil {
		writeAppError(w, err)
		return
	}
	deferred, err := queryBool(req, "deferred")
	if err != nil {
		writeAppError(w, err)
		return
	}
	activeTurnStartSeq, err := queryUint(req, "active_turn_start_seq")
	if err != nil {
		writeAppError(w, err)
		return
	}
	includeToolDetails, err := queryBool(req, "include_tool_details")
	if err != nil {
		writeAppError(w, err)
		return
	}
	eventID := strings.TrimSpace(req.URL.Query().Get("event_id"))
	toolCallID := strings.TrimSpace(req.URL.Query().Get("tool_call_id"))
	span.SetAttributes(
		attribute.String("session.id", sessionID.String()),
		attribute.Int("messages.limit", limit),
		attribute.Bool("messages.init", init),
		attribute.Bool("messages.deferred", deferred),
	)
	payload, err := r.app.SessionMessages(req.Context(), app.SessionMessagesRequest{
		SessionID:          sessionID,
		AfterSeq:           afterSeq,
		BeforeSeq:          beforeSeq,
		Limit:              limit,
		Init:               init,
		Deferred:           deferred,
		ActiveTurnStartSeq: activeTurnStartSeq,
		IncludeToolDetails: includeToolDetails,
		EventID:            eventID,
		ToolCallID:         toolCallID,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionSupervisor(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.SessionSupervisor(req.Context(), app.SessionSupervisorRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) updateSessionSupervisor(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body sessionSupervisorRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.UpdateSessionSupervisor(req.Context(), app.UpdateSessionSupervisorRequest{
		SessionID:                sessionID,
		Enabled:                  body.Enabled,
		IdleAfterMinutes:         body.IdleAfterMinutes,
		MaxConsecutiveInjections: body.MaxConsecutiveInjections,
		ConsecutiveInjections:    body.ConsecutiveInjections,
		Goal:                     body.Goal,
		AcceptanceCriteria:       body.AcceptanceCriteria,
		ContextFiles:             body.ContextFiles,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) supervisorRuns(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.SupervisorRuns(req.Context(), app.SupervisorRunsRequest{SessionID: sessionID, Limit: limit})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) runSupervisorOnce(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body supervisorRunOnceRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.RunSupervisorOnce(req.Context(), app.SupervisorRunOnceRequest{SessionID: sessionID, DryRun: body.DryRun})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionState(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.SessionState(req.Context(), app.SessionStateRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) probeSessionState(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.ProbeSessionState(req.Context(), app.ProbeSessionStateRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionWorkspace(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.SessionWorkspace(req.Context(), app.SessionWorkspaceRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) updateSessionWorkspace(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body app.UpdateSessionWorkspaceRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.SessionID = sessionID
	payload, err := r.app.UpdateSessionWorkspace(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) workspaceFileList(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	pathValue, err := queryRelativePath(req, "path", false)
	if err != nil {
		writeAppError(w, err)
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.WorkspaceFileList(req.Context(), app.WorkspaceFileListRequest{
		SessionID: sessionID,
		Path:      pathValue,
		Search:    strings.TrimSpace(req.URL.Query().Get("search")),
		Limit:     limit,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) workspaceFileRead(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	pathValue, err := queryRelativePath(req, "path", true)
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.WorkspaceFileRead(req.Context(), app.WorkspaceFileReadRequest{
		SessionID: sessionID,
		Path:      pathValue,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) gitFileVersions(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	pathValue, err := queryRelativePath(req, "path", true)
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.GitFileVersions(req.Context(), app.GitFileVersionsRequest{
		SessionID: sessionID,
		Path:      pathValue,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionCommands(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.SessionCommands(req.Context(), app.SessionCommandsRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) executeSessionCommand(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body executeSessionCommandRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = strings.TrimSpace(body.Command)
	}
	payload, err := r.app.ExecuteSessionCommand(req.Context(), app.ExecuteSessionCommandRequest{SessionID: sessionID, Name: name, Args: body.Args})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) waitInbox(w http.ResponseWriter, req *http.Request) {
	payload, err := r.app.WaitInbox(req.Context())
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) waitThreads(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.WaitThreads(req.Context(), app.WaitThreadsRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) waitThread(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.WaitThread(req.Context(), app.WaitThreadRequest{SessionID: sessionID, ThreadID: strings.TrimSpace(req.PathValue("thread_id"))})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) createWait(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body app.CreateWaitRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	body.SessionID = sessionID
	payload, err := r.app.CreateWait(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) claimWait(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.ClaimWait(req.Context(), app.WaitLifecycleRequest{SessionID: sessionID, WaitID: strings.TrimSpace(req.PathValue("wait_id"))})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) answerWait(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body answerWaitRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.AnswerWait(req.Context(), app.WaitLifecycleRequest{SessionID: sessionID, WaitID: strings.TrimSpace(req.PathValue("wait_id")), Answer: body.Answer})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) cancelWait(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.CancelWait(req.Context(), app.WaitLifecycleRequest{SessionID: sessionID, WaitID: strings.TrimSpace(req.PathValue("wait_id"))})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) renameSession(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body renameSessionRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	if body.Name == nil || strings.TrimSpace(*body.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name required", "name")
		return
	}
	payload, err := r.app.RenameSession(req.Context(), app.RenameSessionRequest{SessionID: sessionID, Name: *body.Name})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) focusSession(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body focusSessionRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	if body.Focused == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "focused required", "focused")
		return
	}
	payload, err := r.app.FocusSession(req.Context(), app.FocusSessionRequest{SessionID: sessionID, Focused: *body.Focused})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) editSession(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	body, err := decodeRawJSONBody(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	name, err := stringPatch(body, "name")
	if err != nil {
		writeAppError(w, err)
		return
	}
	priorityOffset, err := float64Patch(body, "priority_offset")
	if err != nil {
		writeAppError(w, err)
		return
	}
	snoozeUntil, err := int64Patch(body, "snooze_until")
	if err != nil {
		writeAppError(w, err)
		return
	}
	dependencySessionID, err := stringPatch(body, "dependency_session_id")
	if err != nil {
		writeAppError(w, err)
		return
	}
	iodMode, err := stringPatch(body, "iod_mode")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.EditSession(req.Context(), app.EditSessionRequest{
		SessionID:           sessionID,
		Name:                name,
		PriorityOffset:      priorityOffset,
		SnoozeUntil:         snoozeUntil,
		DependencySessionID: dependencySessionID,
		IODMode:             iodMode,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) editCwdGroup(w http.ResponseWriter, req *http.Request) {
	var body app.EditCwdGroupRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.EditCwdGroup(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) switchSessionModel(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	body, err := decodeRawJSONBody(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	model, err := stringPatch(body, "model")
	if err != nil {
		writeAppError(w, err)
		return
	}
	provider, err := stringPatch(body, "provider")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.SwitchSessionModel(req.Context(), app.SwitchSessionModelRequest{SessionID: sessionID, Model: model, Provider: provider})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) deleteSession(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.DeleteSession(req.Context(), app.DeleteSessionRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) restartSession(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.RestartSession(req.Context(), app.RestartSessionRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) handoffSession(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.HandoffSession(req.Context(), app.HandoffSessionRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) notImplemented(message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "unsupported", message, "")
	}
}

func writeAppError(w http.ResponseWriter, err error) {
	var appErr *app.Error
	if errors.As(err, &appErr) {
		status := http.StatusInternalServerError
		switch appErr.Code {
		case "invalid_request":
			status = http.StatusBadRequest
		case "unauthorized":
			status = http.StatusUnauthorized
		case "forbidden":
			status = http.StatusForbidden
		case "not_found":
			status = http.StatusNotFound
		case "conflict", "transport_reset_required":
			status = http.StatusConflict
		case "unsupported", "unsupported_backend":
			status = http.StatusNotImplemented
		}
		writeError(w, status, appErr.Code, appErr.Message, appErr.Field)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
}

func decodeJSONBody(req *http.Request, dst any) error {
	dec := json.NewDecoder(req.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func decodeRawJSONBody(req *http.Request) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := decodeJSONBody(req, &body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]json.RawMessage{}
	}
	return body, nil
}

func stringPatch(body map[string]json.RawMessage, key string) (app.StringPatch, error) {
	raw, ok := body[key]
	if !ok {
		return app.StringPatch{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return app.StringPatch{Present: true}, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return app.StringPatch{}, app.Invalid(key, key+" must be a string")
	}
	return app.StringPatch{Present: true, Value: &value}, nil
}

func float64Patch(body map[string]json.RawMessage, key string) (app.Float64Patch, error) {
	raw, ok := body[key]
	if !ok {
		return app.Float64Patch{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return app.Float64Patch{Present: true}, nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return app.Float64Patch{}, app.Invalid(key, key+" must be a number")
	}
	return app.Float64Patch{Present: true, Value: &value}, nil
}

func int64Patch(body map[string]json.RawMessage, key string) (app.Int64Patch, error) {
	raw, ok := body[key]
	if !ok {
		return app.Int64Patch{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return app.Int64Patch{Present: true}, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return app.Int64Patch{}, app.Invalid(key, key+" must be an integer")
	}
	return app.Int64Patch{Present: true, Value: &value}, nil
}

func routeSessionID(w http.ResponseWriter, req *http.Request) (session.SessionID, bool) {
	value := strings.TrimSpace(req.PathValue("session_id"))
	sessionID, err := session.ParseSessionID(value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), "session_id")
		return "", false
	}
	return sessionID, true
}

func queryInt(req *http.Request, key string) (int, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, app.Invalid(key, key+" must be an integer")
	}
	if n < 0 {
		return 0, app.Invalid(key, key+" must be non-negative")
	}
	return n, nil
}

func queryUint(req *http.Request, key string) (uint64, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, app.Invalid(key, key+" must be an unsigned integer")
	}
	return n, nil
}

func queryUint64(req *http.Request, key string) (*uint64, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		return nil, nil
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil, app.Invalid(key, key+" must be an unsigned integer")
	}
	return &n, nil
}

func queryBool(req *http.Request, key string) (bool, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(value)
	if err != nil {
		return false, app.Invalid(key, key+" must be a boolean")
	}
	return v, nil
}

func queryRelativePath(req *http.Request, key string, required bool) (string, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		if required {
			return "", app.Invalid(key, key+" required")
		}
		return "", nil
	}
	if strings.HasPrefix(value, "/") {
		return "", app.Invalid(key, key+" must be relative")
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		if required {
			return "", app.Invalid(key, key+" must identify a file")
		}
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", app.Invalid(key, key+" escapes workspace root")
	}
	return strings.TrimPrefix(cleaned, "./"), nil
}

func (r Router) schedulerSnapshot(w http.ResponseWriter, req *http.Request) {
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.SchedulerSnapshot(req.Context(), app.SchedulerSnapshotRequest{Limit: limit})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) updateSchedulerSettings(w http.ResponseWriter, req *http.Request) {
	var body schedulerSettingsRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.UpdateSchedulerSettings(req.Context(), app.UpdateSchedulerSettingsRequest{IdleBeforeDeliverySeconds: body.IdleBeforeDeliverySeconds})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionInbox(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.SessionInbox(req.Context(), app.SessionInboxRequest{SessionID: sessionID, Limit: limit})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) setSessionAlarm(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	var body setAlarmRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.SetAlarm(req.Context(), app.SetAlarmRequest{SessionID: sessionID, DurationSeconds: body.DurationSeconds, Title: body.Title, Message: body.Message, CreatedBy: "api"})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}
