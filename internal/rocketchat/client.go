package rocketchat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxRateLimitAttempts = 4

type Room struct {
	ID    string `json:"_id"`
	Name  string `json:"name,omitempty"`
	FName string `json:"fname,omitempty"`
	Type  string `json:"t,omitempty"`
}

type ClientOptions struct {
	BaseURL     string
	UserID      string
	AuthToken   string
	Timeout     time.Duration
	HTTPClient  *http.Client
	Debug       bool
	DebugWriter io.Writer
}

type CleanRoomHistoryOptions struct {
	Oldest           string
	Latest           string
	IgnoreDiscussion bool
	IgnoreThreads    bool
	ExcludePinned    bool
}

type Message struct {
	ID     string `json:"_id"`
	RoomID string `json:"rid,omitempty"`
	UserID string
	User   struct {
		ID string `json:"_id"`
	} `json:"u,omitempty"`
}

type Page struct {
	Offset int
	Count  int
	Total  int
}

type ListMessagesOptions struct {
	Offset             int
	Count              int
	IncludeThreads     bool
	IncludeDiscussions bool
}

type Client struct {
	baseURL     string
	userID      string
	authToken   string
	timeout     time.Duration
	httpClient  *http.Client
	debug       bool
	debugWriter io.Writer
}

type APIError struct {
	Method     string
	Endpoint   string
	StatusCode int
	Detail     string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Rocket.Chat %s %s failed with status %d: %s", e.Method, e.Endpoint, e.StatusCode, e.Detail)
}

func New(options ClientOptions) *Client {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:     strings.TrimRight(options.BaseURL, "/"),
		userID:      options.UserID,
		authToken:   options.AuthToken,
		timeout:     options.Timeout,
		httpClient:  httpClient,
		debug:       options.Debug,
		debugWriter: options.DebugWriter,
	}
}

func (c *Client) ListRooms(ctx context.Context) ([]Room, error) {
	var response struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
		Message string `json:"message,omitempty"`
		Status  string `json:"status,omitempty"`
		Update  []Room `json:"update,omitempty"`
		Rooms   []Room `json:"rooms,omitempty"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/v1/rooms.get", nil, &response); err != nil {
		return nil, err
	}
	if response.Update != nil {
		return response.Update, nil
	}
	return response.Rooms, nil
}

func (c *Client) CleanRoomHistory(ctx context.Context, roomID string, options CleanRoomHistoryOptions) error {
	body := map[string]any{
		"roomId":           roomID,
		"oldest":           options.Oldest,
		"latest":           options.Latest,
		"inclusive":        true,
		"limit":            0,
		"ignoreDiscussion": options.IgnoreDiscussion,
		"ignoreThreads":    options.IgnoreThreads,
		"excludePinned":    options.ExcludePinned,
	}
	var response struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
		Message any    `json:"message,omitempty"`
		Status  string `json:"status,omitempty"`
	}
	return c.request(ctx, http.MethodPost, "/api/v1/rooms.cleanHistory", body, &response)
}

func (c *Client) ListMessages(ctx context.Context, room Room, options ListMessagesOptions) ([]Message, Page, error) {
	endpoint, err := historyEndpoint(room)
	if err != nil {
		return nil, Page{}, err
	}
	query := url.Values{}
	query.Set("roomId", room.ID)
	query.Set("offset", strconv.Itoa(options.Offset))
	query.Set("count", strconv.Itoa(options.Count))
	query.Set("inclusive", "true")
	if options.IncludeThreads {
		query.Set("showThreadMessages", "true")
	}
	if options.IncludeDiscussions {
		query.Set("showDiscussion", "true")
	}

	var response struct {
		Success  bool      `json:"success"`
		Error    string    `json:"error,omitempty"`
		Message  string    `json:"message,omitempty"`
		Status   string    `json:"status,omitempty"`
		Messages []Message `json:"messages,omitempty"`
		Offset   int       `json:"offset,omitempty"`
		Count    int       `json:"count,omitempty"`
		Total    int       `json:"total,omitempty"`
	}
	if err := c.request(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, &response); err != nil {
		return nil, Page{}, err
	}
	return response.Messages, Page{Offset: response.Offset, Count: response.Count, Total: response.Total}, nil
}

func (c *Client) DeleteMessage(ctx context.Context, roomID string, messageID string) error {
	c.debugDeleteStart(roomID, messageID)
	body := map[string]any{
		"roomId": roomID,
		"msgId":  messageID,
		"asUser": true,
	}
	var response struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
		Message any    `json:"message,omitempty"`
		Status  string `json:"status,omitempty"`
	}
	return c.request(ctx, http.MethodPost, "/api/v1/chat.delete", body, &response)
}

func (c *Client) MessageExists(ctx context.Context, messageID string) (bool, error) {
	query := url.Values{}
	query.Set("msgId", messageID)
	var response struct {
		Success bool     `json:"success"`
		Error   string   `json:"error,omitempty"`
		Message *Message `json:"message,omitempty"`
		Status  string   `json:"status,omitempty"`
	}
	err := c.request(ctx, http.MethodGet, "/api/v1/chat.getMessage?"+query.Encode(), nil, &response)
	if err != nil {
		apiErr, ok := err.(*APIError)
		if ok && apiErr.StatusCode == http.StatusBadRequest {
			return false, nil
		}
		return false, err
	}
	return response.Success && response.Message != nil && response.Message.ID != "", nil
}

func historyEndpoint(room Room) (string, error) {
	switch room.Type {
	case "c":
		return "/api/v1/channels.history", nil
	case "p":
		return "/api/v1/groups.history", nil
	case "d":
		return "/api/v1/im.history", nil
	default:
		return "", fmt.Errorf("message mode does not support room type %q for room %s", room.Type, room.ID)
	}
}

func (c *Client) request(ctx context.Context, method string, endpoint string, body any, target any) error {
	var encodedBody []byte
	var err error
	if body != nil {
		encodedBody, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}

	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	var lastAPIError *APIError
	for attempt := 1; attempt <= maxRateLimitAttempts; attempt++ {
		var reader io.Reader
		if encodedBody != nil {
			reader = bytes.NewReader(encodedBody)
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, reader)
		if err != nil {
			return err
		}
		req.Header.Set("X-Auth-Token", c.authToken)
		req.Header.Set("X-User-Id", c.userID)
		req.Header.Set("Content-Type", "application/json")

		c.debugRequest(method, endpoint)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			c.debugTransportError(method, endpoint, err)
			return err
		}

		var raw map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			raw = map[string]any{}
		}
		_ = resp.Body.Close()

		success, hasSuccess := raw["success"].(bool)
		responseDetail, hasDetail := responseDetail(raw)
		c.debugResponse(method, endpoint, resp.StatusCode, success, hasSuccess, responseDetail, hasDetail)
		if resp.StatusCode == http.StatusTooManyRequests {
			lastAPIError = &APIError{
				Method:     method,
				Endpoint:   endpoint,
				StatusCode: resp.StatusCode,
				Detail:     detail(raw),
			}
			if attempt < maxRateLimitAttempts {
				if err := sleepBeforeRetry(ctx, retryAfter(resp.Header.Get("Retry-After"))); err != nil {
					return err
				}
				continue
			}
			return lastAPIError
		}
		if !respOK(resp.StatusCode) || (hasSuccess && !success) {
			return &APIError{
				Method:     method,
				Endpoint:   endpoint,
				StatusCode: resp.StatusCode,
				Detail:     detail(raw),
			}
		}

		if target == nil {
			return nil
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	}

	return lastAPIError
}

func retryAfter(value string) time.Duration {
	if value == "" {
		return time.Second
	}
	seconds, err := strconv.Atoi(value)
	if err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return time.Second
	}
	delay := time.Until(when)
	if delay < 0 {
		return 0
	}
	return delay
}

func sleepBeforeRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) debugRequest(method string, endpoint string) {
	if !c.shouldDebugEndpoint(endpoint) {
		return
	}
	fmt.Fprintf(c.debugWriter, "[debug] request %s %s\n", method, endpointOnly(endpoint))
}

func (c *Client) debugTransportError(method string, endpoint string, err error) {
	if !c.shouldDebugEndpoint(endpoint) {
		return
	}
	fmt.Fprintf(c.debugWriter, "[debug] transport-error %s %s error=%q\n", method, endpoint, err.Error())
}

func (c *Client) debugResponse(method string, endpoint string, status int, success bool, hasSuccess bool, detail string, hasDetail bool) {
	if !c.shouldDebugEndpoint(endpoint) {
		return
	}
	if hasSuccess && hasDetail {
		fmt.Fprintf(c.debugWriter, "[debug] response %s %s status=%d success=%t detail=%q\n", method, endpointOnly(endpoint), status, success, detail)
		return
	}
	if hasSuccess {
		fmt.Fprintf(c.debugWriter, "[debug] response %s %s status=%d success=%t\n", method, endpointOnly(endpoint), status, success)
		return
	}
	if hasDetail {
		fmt.Fprintf(c.debugWriter, "[debug] response %s %s status=%d detail=%q\n", method, endpointOnly(endpoint), status, detail)
		return
	}
	fmt.Fprintf(c.debugWriter, "[debug] response %s %s status=%d\n", method, endpointOnly(endpoint), status)
}

func (c *Client) debugDeleteStart(roomID string, messageID string) {
	if !c.debug || c.debugWriter == nil {
		return
	}
	fmt.Fprintf(c.debugWriter, "[debug] delete start roomId=%s msgId=%s\n", roomID, messageID)
}

func (c *Client) shouldDebugEndpoint(endpoint string) bool {
	return c.debug && c.debugWriter != nil && endpointOnly(endpoint) == "/api/v1/chat.delete"
}

func endpointOnly(endpoint string) string {
	if before, _, found := strings.Cut(endpoint, "?"); found {
		return before
	}
	return endpoint
}

func respOK(status int) bool {
	return status >= 200 && status < 300
}

func detail(raw map[string]any) string {
	if value, ok := responseDetail(raw); ok {
		return value
	}
	return "Unknown error"
}

func responseDetail(raw map[string]any) (string, bool) {
	for _, key := range []string{"error", "message", "status"} {
		value, ok := raw[key].(string)
		if ok && value != "" {
			return value, true
		}
	}
	return "", false
}
