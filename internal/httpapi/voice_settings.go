package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type voiceSettingsPayload struct {
	OK                         bool                      `json:"ok"`
	TTSEnabledForNarration     bool                      `json:"tts_enabled_for_narration"`
	TTSEnabledForFinalResponse bool                      `json:"tts_enabled_for_final_response"`
	TTSBaseURL                 string                    `json:"tts_base_url"`
	TTSAPIKey                  string                    `json:"tts_api_key"`
	Audio                      voiceSettingsAudio        `json:"audio"`
	Notifications              voiceSettingsNotification `json:"notifications"`
}

type voiceSettingsAudio struct {
	QueueDepth          int    `json:"queue_depth"`
	ActiveListenerCount int    `json:"active_listener_count"`
	SegmentCount        int    `json:"segment_count"`
	StreamURL           string `json:"stream_url"`
	LastError           string `json:"last_error"`
}

type voiceSettingsNotification struct {
	EnabledDevices int    `json:"enabled_devices"`
	TotalDevices   int    `json:"total_devices"`
	VAPIDPublicKey string `json:"vapid_public_key"`
}

type voiceProviderTestPayload struct {
	OK         bool   `json:"ok"`
	Status     string `json:"status"`
	StatusCode int    `json:"status_code,omitempty"`
}

func (r Router) voiceSettings(w http.ResponseWriter, _ *http.Request) {
	settings, err := r.loadVoiceSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (r Router) updateVoiceSettings(w http.ResponseWriter, req *http.Request) {
	current, err := r.loadVoiceSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
		return
	}
	var body voiceSettingsPayload
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	current.TTSEnabledForNarration = body.TTSEnabledForNarration
	current.TTSEnabledForFinalResponse = body.TTSEnabledForFinalResponse
	current.TTSBaseURL = strings.TrimSpace(body.TTSBaseURL)
	current.TTSAPIKey = strings.TrimSpace(body.TTSAPIKey)
	if err := r.saveVoiceSettings(current); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (r Router) testVoiceProvider(w http.ResponseWriter, req *http.Request) {
	current, err := r.loadVoiceSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
		return
	}
	var body voiceSettingsPayload
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	baseURL := strings.TrimSpace(body.TTSBaseURL)
	apiKey := strings.TrimSpace(body.TTSAPIKey)
	if baseURL == "" {
		baseURL = strings.TrimSpace(current.TTSBaseURL)
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(current.TTSAPIKey)
	}
	if baseURL == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "base URL required", "tts_base_url")
		return
	}
	statusCode, err := probeOpenAICompatibleProvider(req.Context(), baseURL, apiKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "provider_error", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, voiceProviderTestPayload{OK: true, Status: "provider reachable", StatusCode: statusCode})
}

func (r Router) loadVoiceSettings() (voiceSettingsPayload, error) {
	settings := defaultVoiceSettings()
	path := r.voiceSettingsPath()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return voiceSettingsPayload{}, fmt.Errorf("read voice settings: %w", err)
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		return voiceSettingsPayload{}, fmt.Errorf("parse voice settings: %w", err)
	}
	settings.OK = true
	settings.Audio.StreamURL = "/api/audio/live.m3u8"
	return settings, nil
}

func (r Router) saveVoiceSettings(settings voiceSettingsPayload) error {
	settings.OK = true
	settings.Audio.StreamURL = "/api/audio/live.m3u8"
	path := r.voiceSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create voice settings dir: %w", err)
	}
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal voice settings: %w", err)
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func (r Router) voiceSettingsPath() string {
	return filepath.Join(r.cfg.Storage.DataDir, "settings", "voice.json")
}

func defaultVoiceSettings() voiceSettingsPayload {
	return voiceSettingsPayload{
		OK:                         true,
		TTSEnabledForNarration:     false,
		TTSEnabledForFinalResponse: true,
		Audio: voiceSettingsAudio{
			StreamURL: "/api/audio/live.m3u8",
		},
	}
}

func probeOpenAICompatibleProvider(ctx context.Context, baseURL, apiKey string) (int, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(bytes.ReplaceAll(body, []byte("\n"), []byte(" "))))
		if msg == "" {
			msg = http.StatusText(res.StatusCode)
		}
		return res.StatusCode, fmt.Errorf("provider /models returned HTTP %d: %s", res.StatusCode, msg)
	}
	return res.StatusCode, nil
}
