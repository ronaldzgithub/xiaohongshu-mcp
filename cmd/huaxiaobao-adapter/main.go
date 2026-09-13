package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	requestSchema  = "foundry.huaxiaobao.tool-request.v1"
	responseSchema = "foundry.huaxiaobao.tool-result.v1"
	executorID     = "xiaohongshu-mcp"
)

type adapterRequest struct {
	SchemaVersion string `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	Capability    string `json:"capability"`
	AccountRef    string `json:"account_ref"`
	Tab           string `json:"tab,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type adapterError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type adapterResponse struct {
	SchemaVersion           string        `json:"schema_version"`
	OperationID             string        `json:"operation_id"`
	RequestHash             string        `json:"request_hash"`
	Capability              string        `json:"capability"`
	ExecutorID              string        `json:"executor_id"`
	ExecutorRole            string        `json:"executor_role"`
	SideEffect              string        `json:"side_effect"`
	Status                  string        `json:"status"`
	AccountRef              string        `json:"account_ref,omitempty"`
	ObjectRef               string        `json:"object_ref,omitempty"`
	RetrySafe               bool          `json:"retry_safe"`
	ExternalActionPerformed bool          `json:"external_action_performed"`
	Details                 any           `json:"details,omitempty"`
	Error                   *adapterError `json:"error,omitempty"`
}

type nativeStatusEnvelope struct {
	Success bool `json:"success"`
	Data    struct {
		IsLoggedIn bool   `json:"is_logged_in"`
		UserID     string `json:"user_id"`
	} `json:"data"`
}

type nativeDataEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

func hashRequest(req adapterRequest) string {
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func stableAccountObjectRef(accountRef string) string {
	sum := sha256.Sum256([]byte(accountRef))
	return "xiaohongshu:account:" + hex.EncodeToString(sum[:8])
}

func stableNativeSubjectRef(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return "xiaohongshu:subject:" + hex.EncodeToString(sum[:12])
}

func stableNativeObjectRef(accountRef, object string) string {
	sum := sha256.Sum256([]byte(accountRef + "\x00" + object))
	return "xiaohongshu:object:" + hex.EncodeToString(sum[:12])
}

func jsonHash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validatedBaseURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("XHS_ADAPTER_BASE_URL is invalid")
	}
	loopbackHTTP := parsed.Scheme == "http" && net.ParseIP(parsed.Hostname()) != nil && net.ParseIP(parsed.Hostname()).IsLoopback()
	if parsed.Scheme != "https" && !loopbackHTTP {
		return "", fmt.Errorf("XHS_ADAPTER_BASE_URL must be HTTPS or literal loopback HTTP")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func baseResponse(req adapterRequest) adapterResponse {
	return adapterResponse{
		SchemaVersion:           responseSchema,
		OperationID:             req.OperationID,
		RequestHash:             hashRequest(req),
		Capability:              req.Capability,
		ExecutorID:              executorID,
		ExecutorRole:            "primary",
		SideEffect:              "read_only",
		Status:                  "UNKNOWN",
		AccountRef:              req.AccountRef,
		RetrySafe:               true,
		ExternalActionPerformed: false,
	}
}

func execute(ctx context.Context, client *http.Client, baseURL, authToken string, req adapterRequest) adapterResponse {
	result := baseResponse(req)
	if req.SchemaVersion != requestSchema {
		result.Status = "REJECTED"
		result.Error = &adapterError{Code: "SCHEMA_VERSION_UNSUPPORTED", Message: "unsupported request schema"}
		return result
	}
	if strings.TrimSpace(req.OperationID) == "" || strings.TrimSpace(req.AccountRef) == "" {
		result.Status = "REJECTED"
		result.Error = &adapterError{Code: "INVALID_REQUEST", Message: "operation_id and account_ref are required"}
		return result
	}
	if req.Capability != "account.status" && req.Capability != "notifications.unread" && req.Capability != "notifications.list" {
		result.Status = "REJECTED"
		result.Error = &adapterError{Code: "CAPABILITY_NOT_AVAILABLE", Message: "capability is not available on the safe adapter"}
		return result
	}
	if req.Capability != "notifications.list" && (strings.TrimSpace(req.Tab) != "" || req.Limit != 0) {
		result.Status = "REJECTED"
		result.Error = &adapterError{Code: "INVALID_REQUEST", Message: "tab and limit are only valid for notifications.list"}
		return result
	}
	if req.Capability == "notifications.list" {
		result.SideEffect = "read_marks_selected_notifications_seen"
		result.RetrySafe = false
		if req.Tab != "mentions" && req.Tab != "likes" && req.Tab != "connections" {
			result.Status = "REJECTED"
			result.Error = &adapterError{Code: "INVALID_NOTIFICATION_TAB", Message: "tab must be mentions, likes, or connections"}
			return result
		}
		if req.Limit < 1 || req.Limit > 100 {
			result.Status = "REJECTED"
			result.Error = &adapterError{Code: "INVALID_NOTIFICATION_LIMIT", Message: "limit must be between 1 and 100"}
			return result
		}
	}
	if strings.TrimSpace(authToken) == "" {
		result.Error = &adapterError{Code: "CONFIGURATION_REQUIRED", Message: "XHS_ADAPTER_AUTH_TOKEN is required"}
		return result
	}
	validatedURL, err := validatedBaseURL(baseURL)
	if err != nil {
		result.Status = "REJECTED"
		result.Error = &adapterError{Code: "CONFIGURATION_INVALID", Message: err.Error()}
		return result
	}

	var native nativeStatusEnvelope
	if nativeErr := nativeGET(ctx, client, validatedURL, authToken, "/api/v1/login/status", &native); nativeErr != nil {
		result.Error = nativeErr
		return result
	}
	if !native.Success {
		result.Error = &adapterError{Code: "NATIVE_STATUS_FAILED", Message: "native account check did not succeed"}
		return result
	}

	result.ObjectRef = stableAccountObjectRef(req.AccountRef)
	result.Details = map[string]any{
		"is_logged_in": native.Data.IsLoggedIn,
	}
	if !native.Data.IsLoggedIn {
		result.Status = "BLOCKED"
		result.Error = &adapterError{Code: "ACCOUNT_LOGIN_REQUIRED", Message: "account owner login is required"}
		return result
	}
	if strings.TrimSpace(native.Data.UserID) == "" {
		result.Error = &adapterError{Code: "NATIVE_IDENTITY_MISSING", Message: "logged-in account lacks a verifiable subject"}
		return result
	}
	subjectRef := stableNativeSubjectRef(native.Data.UserID)
	if req.Capability == "account.status" {
		result.Details = map[string]any{
			"is_logged_in":       true,
			"native_subject_ref": subjectRef,
		}
		result.Status = "READY"
		return result
	}

	path := "/api/v1/notifications/unread"
	objectKey := "notifications:unread"
	if req.Capability == "notifications.list" {
		query := url.Values{}
		query.Set("tab", req.Tab)
		query.Set("limit", fmt.Sprintf("%d", req.Limit))
		path = "/api/v1/notifications/list?" + query.Encode()
		objectKey = "notifications:list:" + req.Tab
	}
	var envelope nativeDataEnvelope
	if nativeErr := nativeGET(ctx, client, validatedURL, authToken, path, &envelope); nativeErr != nil {
		result.Error = nativeErr
		return result
	}
	if !envelope.Success || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		result.Error = &adapterError{Code: "NATIVE_STATUS_FAILED", Message: "native notification read did not succeed"}
		return result
	}
	var data any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		result.Error = &adapterError{Code: "NATIVE_RESPONSE_INVALID", Message: "native notification response is invalid"}
		return result
	}
	result.ObjectRef = stableNativeObjectRef(req.AccountRef, objectKey)
	result.Details = map[string]any{
		"native_subject_ref":   subjectRef,
		"native_readback":      data,
		"native_readback_hash": jsonHash(data),
	}
	result.Status = "READY"
	return result
}

func nativeGET(ctx context.Context, client *http.Client, baseURL, authToken, path string, target any) *adapterError {
	nativeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return &adapterError{Code: "NATIVE_REQUEST_FAILED", Message: "could not construct native request"}
	}
	nativeReq.Header.Set("Authorization", "Bearer "+authToken)
	resp, err := client.Do(nativeReq)
	if err != nil {
		return &adapterError{Code: "NATIVE_UNAVAILABLE", Message: "native service is unavailable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &adapterError{Code: "NATIVE_HTTP_ERROR", Message: fmt.Sprintf("native service returned HTTP %d", resp.StatusCode)}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target); err != nil {
		return &adapterError{Code: "NATIVE_RESPONSE_INVALID", Message: "native response is invalid"}
	}
	return nil
}

func descriptor() map[string]any {
	return map[string]any{
		"schema_version": "foundry.huaxiaobao.capability-descriptor.v1",
		"executor_id":    executorID,
		"executor_role":  "primary",
		"capabilities": []map[string]any{
			{
				"name":        "account.status",
				"version":     "1.0.0",
				"side_effect": "read_only",
				"retry_safe":  true,
			},
			{
				"name":        "notifications.unread",
				"version":     "1.0.0",
				"side_effect": "read_only",
				"retry_safe":  true,
			},
			{
				"name":        "notifications.list",
				"version":     "1.0.0",
				"side_effect": "read_marks_selected_notifications_seen",
				"retry_safe":  false,
			},
		},
		"external_actions_available": false,
		"fallback_executor_id":       "social-auto-upload/xiaohongshu",
	}
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func run(args []string, input io.Reader) int {
	if len(args) != 1 {
		_ = writeJSON(map[string]any{"error": "usage: huaxiaobao-adapter describe|execute"})
		return 2
	}
	if args[0] == "describe" {
		if err := writeJSON(descriptor()); err != nil {
			return 1
		}
		return 0
	}
	if args[0] != "execute" {
		_ = writeJSON(map[string]any{"error": "unknown command"})
		return 2
	}

	var req adapterRequest
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		_ = writeJSON(adapterResponse{
			SchemaVersion: responseSchema,
			ExecutorID:    executorID,
			ExecutorRole:  "primary",
			SideEffect:    "read_only",
			Status:        "REJECTED",
			RetrySafe:     true,
			Error:         &adapterError{Code: "INVALID_JSON", Message: err.Error()},
		})
		return 2
	}

	client := &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	result := execute(context.Background(), client,
		func() string {
			if value := strings.TrimSpace(os.Getenv("XHS_ADAPTER_BASE_URL")); value != "" {
				return value
			}
			return "http://127.0.0.1:18060"
		}(),
		os.Getenv("XHS_ADAPTER_AUTH_TOKEN"), req)
	if err := writeJSON(result); err != nil {
		return 1
	}
	if result.Status == "REJECTED" {
		return 2
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin))
}
