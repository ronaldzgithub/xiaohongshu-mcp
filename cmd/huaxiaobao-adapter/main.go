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
	if req.Capability != "account.status" {
		result.Status = "REJECTED"
		result.Error = &adapterError{Code: "CAPABILITY_NOT_AVAILABLE", Message: "only account.status is available on the safe adapter"}
		return result
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

	nativeReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		validatedURL+"/api/v1/login/status", nil)
	if err != nil {
		result.Error = &adapterError{Code: "NATIVE_REQUEST_FAILED", Message: "could not construct native account request"}
		return result
	}
	nativeReq.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := client.Do(nativeReq)
	if err != nil {
		result.Error = &adapterError{Code: "NATIVE_UNAVAILABLE", Message: "native account service is unavailable"}
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		result.Error = &adapterError{Code: "NATIVE_HTTP_ERROR", Message: fmt.Sprintf("native service returned HTTP %d", resp.StatusCode)}
		return result
	}

	var native nativeStatusEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&native); err != nil {
		result.Error = &adapterError{Code: "NATIVE_RESPONSE_INVALID", Message: "native account response is invalid"}
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
	if native.Data.IsLoggedIn {
		if strings.TrimSpace(native.Data.UserID) == "" {
			result.Error = &adapterError{Code: "NATIVE_IDENTITY_MISSING", Message: "logged-in account lacks a verifiable subject"}
			return result
		}
		result.Details = map[string]any{
			"is_logged_in":       true,
			"native_subject_ref": stableNativeSubjectRef(native.Data.UserID),
		}
		result.Status = "READY"
	} else {
		result.Status = "BLOCKED"
		result.Error = &adapterError{Code: "ACCOUNT_LOGIN_REQUIRED", Message: "account owner login is required"}
	}
	return result
}

func descriptor() map[string]any {
	return map[string]any{
		"schema_version": "foundry.huaxiaobao.capability-descriptor.v1",
		"executor_id":    executorID,
		"executor_role":  "primary",
		"capabilities": []map[string]any{
			{
				"name":        "account.status",
				"side_effect": "read_only",
				"retry_safe":  true,
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
