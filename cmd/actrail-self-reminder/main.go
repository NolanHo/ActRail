package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "http://127.0.0.1:18743"

type client struct {
	baseURL    string
	cookie     string
	httpClient *http.Client
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	if err := runE(args, stdout); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func runE(args []string, stdout io.Writer) error {
	baseURLValue, rest, err := parseGlobalArgs(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 || rest[0] == "-h" || rest[0] == "--help" {
		writeUsage(stdout)
		return nil
	}

	baseURL, err := normalizeBaseURL(baseURLValue)
	if err != nil {
		return err
	}
	c, err := newClient(baseURL)
	if err != nil {
		return err
	}

	command, commandArgs := rest[0], rest[1:]
	switch command {
	case "sessions":
		return cmdSessions(c, commandArgs, stdout)
	case "list":
		return cmdList(c, commandArgs, stdout)
	case "create":
		return cmdCreate(c, commandArgs, stdout)
	case "cancel":
		return cmdCancel(c, commandArgs, stdout)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func parseGlobalArgs(args []string) (string, []string, error) {
	baseURLValue := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--base-url":
			i++
			if i >= len(args) {
				return "", nil, errors.New("missing value for --base-url")
			}
			baseURLValue = args[i]
		case strings.HasPrefix(arg, "--base-url="):
			baseURLValue = strings.TrimPrefix(arg, "--base-url=")
		default:
			rest = append(rest, arg)
		}
	}
	return baseURLValue, rest, nil
}

func writeUsage(w io.Writer) {
	fmt.Fprintf(w, `Operate ActRail Scheduler self-reminders.

Usage:
  actrail-self-reminder [--base-url URL] sessions [--limit N]
  actrail-self-reminder [--base-url URL] list [--limit N] [--session-id ID] [--state scheduled|delivered|cancelled|error|unsupported]
  actrail-self-reminder [--base-url URL] create [--session-id ID] (--after 10m | --duration-seconds N) [--title TITLE] --message TEXT
  actrail-self-reminder [--base-url URL] cancel ITEM_ID

Environment:
  ACTRAIL_BASE_URL             ActRail API base URL. Default: %s
  ACTRAIL_SESSION_ID           Default target session id for create.
  ACTRAIL_AUTH_PASSWORD        Optional password used to call /api/login.
  ACTRAIL_AUTH_COOKIE_HEADER   Optional raw Cookie header.
  ACTRAIL_AUTH_TOKEN           Optional auth token cookie value.
  ACTRAIL_AUTH_COOKIE_NAME     Optional token cookie name. Default: actrail_auth
  ACTRAIL_AUTH_COOKIE          Legacy token cookie name fallback.
`, defaultBaseURL)
}

func normalizeBaseURL(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("ACTRAIL_BASE_URL"))
	}
	if raw == "" {
		raw = defaultBaseURL
	}
	raw = strings.TrimRight(raw, "/")
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return "", errors.New("ACTRAIL_BASE_URL must start with http:// or https://")
	}
	return raw, nil
}

func newClient(baseURL string) (*client, error) {
	c := &client{
		baseURL: baseURL,
		cookie:  envCookie(),
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
	password := os.Getenv("ACTRAIL_AUTH_PASSWORD")
	if password != "" && c.cookie == "" {
		if err := c.login(password); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func envCookie() string {
	if raw := strings.TrimSpace(os.Getenv("ACTRAIL_AUTH_COOKIE_HEADER")); raw != "" {
		return raw
	}
	token := strings.TrimSpace(os.Getenv("ACTRAIL_AUTH_TOKEN"))
	if token == "" {
		return ""
	}
	cookieName := strings.TrimSpace(os.Getenv("ACTRAIL_AUTH_COOKIE_NAME"))
	if cookieName == "" {
		cookieName = strings.TrimSpace(os.Getenv("ACTRAIL_AUTH_COOKIE"))
	}
	if cookieName == "" {
		cookieName = "actrail_auth"
	}
	return cookieName + "=" + token
}

func (c *client) login(password string) error {
	_, setCookie, err := c.request("POST", "/api/login", map[string]any{"password": password}, false)
	if err != nil {
		return err
	}
	if setCookie == "" {
		return errors.New("login succeeded but no auth cookie was returned")
	}
	c.cookie = setCookie
	return nil
}

func (c *client) request(method string, path string, body any, attachCookie bool) (map[string]any, string, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if attachCookie && c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%s %s failed: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "", readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%s %s failed with HTTP %d: %s", method, path, resp.StatusCode, string(raw))
	}

	payload := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, "", fmt.Errorf("%s %s returned invalid JSON: %w", method, path, err)
		}
	}

	setCookie := cookieHeaderFromResponse(resp)
	if setCookie != "" {
		payload["_set_cookie"] = setCookie
	}
	return payload, setCookie, nil
}

func cookieHeaderFromResponse(resp *http.Response) string {
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func printJSON(w io.Writer, payload any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func cmdSessions(c *client, args []string, stdout io.Writer) error {
	fs := newFlagSet("sessions")
	limit := fs.Int("limit", 100, "maximum sessions to return")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("sessions does not accept positional arguments")
	}
	query := url.Values{"limit": []string{strconv.Itoa(*limit)}}.Encode()
	payload, _, err := c.request("GET", "/api/sessions?"+query, nil, true)
	if err != nil {
		return err
	}
	return printJSON(stdout, payload)
}

func cmdList(c *client, args []string, stdout io.Writer) error {
	fs := newFlagSet("list")
	limit := fs.Int("limit", 100, "maximum Scheduler items to inspect")
	sessionID := fs.String("session-id", "", "session id filter")
	state := fs.String("state", "", "state filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("list does not accept positional arguments")
	}
	if *state != "" && !validState(*state) {
		return fmt.Errorf("state must be one of scheduled, delivered, cancelled, error, unsupported")
	}

	query := url.Values{"limit": []string{strconv.Itoa(*limit)}}.Encode()
	payload, _, err := c.request("GET", "/api/scheduler?"+query, nil, true)
	if err != nil {
		return err
	}

	items := make([]any, 0)
	if rawItems, ok := payload["items"].([]any); ok {
		for _, item := range rawItems {
			if itemString(item, "kind") != "self_reminder" {
				continue
			}
			if *sessionID != "" && itemString(item, "session_id") != *sessionID {
				continue
			}
			if *state != "" && itemString(item, "state") != *state {
				continue
			}
			items = append(items, item)
		}
	}

	ok := any(true)
	if rawOK, exists := payload["ok"]; exists {
		ok = rawOK
	}
	return printJSON(stdout, map[string]any{
		"ok":             ok,
		"self_reminders": items,
	})
}

func validState(state string) bool {
	switch state {
	case "scheduled", "delivered", "cancelled", "error", "unsupported":
		return true
	default:
		return false
	}
}

func itemString(item any, key string) string {
	object, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := object[key].(string)
	return value
}

func cmdCreate(c *client, args []string, stdout io.Writer) error {
	fs := newFlagSet("create")
	sessionID := fs.String("session-id", "", "target session id")
	after := fs.String("after", "", "delay such as 30s, 10m, 2h, 1d, or raw seconds")
	durationSeconds := fs.Int("duration-seconds", -1, "delay in seconds")
	title := fs.String("title", "", "optional reminder title")
	message := fs.String("message", "", "reminder message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("create does not accept positional arguments")
	}

	targetSessionID := strings.TrimSpace(*sessionID)
	if targetSessionID == "" {
		targetSessionID = strings.TrimSpace(os.Getenv("ACTRAIL_SESSION_ID"))
	}
	if targetSessionID == "" {
		return errors.New("missing --session-id or ACTRAIL_SESSION_ID")
	}
	if *message == "" {
		return errors.New("missing --message")
	}

	duration := *durationSeconds
	if duration < 0 {
		if *after == "" {
			return errors.New("missing --after or --duration-seconds")
		}
		parsed, err := parseDuration(*after)
		if err != nil {
			return err
		}
		duration = parsed
	}

	payload := map[string]any{
		"session_id":       targetSessionID,
		"duration_seconds": duration,
		"message":          *message,
		"created_by":       "agent",
	}
	if *title != "" {
		payload["title"] = *title
	}

	response, _, err := c.request("POST", "/api/scheduler/self-reminders", payload, true)
	if err != nil {
		return err
	}
	return printJSON(stdout, response)
}

func parseDuration(value string) (int, error) {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" {
		return 0, errors.New("duration is required")
	}
	if seconds, err := strconv.Atoi(text); err == nil {
		return seconds, nil
	}

	if len(text) < 2 {
		return 0, errors.New("duration must look like 30s, 10m, 2h, 1d, or raw seconds")
	}
	amountText, unit := text[:len(text)-1], text[len(text)-1:]
	amount, err := strconv.Atoi(amountText)
	if err != nil {
		return 0, errors.New("duration must look like 30s, 10m, 2h, 1d, or raw seconds")
	}
	switch unit {
	case "s":
		return amount, nil
	case "m":
		return amount * 60, nil
	case "h":
		return amount * 3600, nil
	case "d":
		return amount * 86400, nil
	default:
		return 0, errors.New("duration must look like 30s, 10m, 2h, 1d, or raw seconds")
	}
}

func cmdCancel(c *client, args []string, stdout io.Writer) error {
	fs := newFlagSet("cancel")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("cancel requires ITEM_ID")
	}
	itemID := url.PathEscape(fs.Arg(0))
	response, _, err := c.request("POST", "/api/scheduler/self-reminders/"+itemID+"/cancel", map[string]any{}, true)
	if err != nil {
		return err
	}
	return printJSON(stdout, response)
}
