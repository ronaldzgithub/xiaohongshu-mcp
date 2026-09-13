package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validRequest() adapterRequest {
	return adapterRequest{
		SchemaVersion: requestSchema,
		OperationID:   "op-account-status-1",
		Capability:    "account.status",
		AccountRef:    "opaque-account-ref",
	}
}

func TestExecuteAccountStatusUsesNativeReadOnlyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/login/status", r.URL.Path)
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{"is_logged_in": true, "user_id": "native-user"},
		})
	}))
	defer server.Close()

	result := execute(context.Background(), server.Client(), server.URL, "secret", validRequest())

	assert.Equal(t, "READY", result.Status)
	assert.Equal(t, "read_only", result.SideEffect)
	assert.True(t, result.RetrySafe)
	assert.False(t, result.ExternalActionPerformed)
	assert.NotEmpty(t, result.ObjectRef)
	assert.NotEmpty(t, result.RequestHash)
	assert.NotContains(t, fmt.Sprint(result.Details), "native-user")
}

func TestExecuteRejectsRemoteHTTPBeforeSendingCredential(t *testing.T) {
	result := execute(context.Background(), http.DefaultClient, "http://example.com", "secret", validRequest())

	require.NotNil(t, result.Error)
	assert.Equal(t, "REJECTED", result.Status)
	assert.Equal(t, "CONFIGURATION_INVALID", result.Error.Code)
}

func TestExecuteRejectsOutboundCapabilityWithoutCallingNativeService(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	req := validRequest()
	req.Capability = "content.publish"
	result := execute(context.Background(), server.Client(), server.URL, "secret", req)

	require.NotNil(t, result.Error)
	assert.Equal(t, "REJECTED", result.Status)
	assert.Equal(t, "CAPABILITY_NOT_AVAILABLE", result.Error.Code)
	assert.False(t, called)
}

func TestExecuteFailsClosedWithoutAdapterCredential(t *testing.T) {
	result := execute(context.Background(), http.DefaultClient, "http://127.0.0.1:1", "", validRequest())

	require.NotNil(t, result.Error)
	assert.Equal(t, "UNKNOWN", result.Status)
	assert.Equal(t, "CONFIGURATION_REQUIRED", result.Error.Code)
}

func TestExecuteUnreadChecksExactAccountThenReadsWithoutSideEffect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"is_logged_in": true, "user_id": "native-user"},
			})
		case "/api/v1/notifications/unread":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"mentions": 2, "likes": 3, "connections": 1},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	req := validRequest()
	req.Capability = "notifications.unread"
	result := execute(context.Background(), server.Client(), server.URL, "secret", req)

	assert.Equal(t, "READY", result.Status)
	assert.Equal(t, "read_only", result.SideEffect)
	assert.True(t, result.RetrySafe)
	assert.False(t, result.ExternalActionPerformed)
	assert.NotContains(t, fmt.Sprint(result.Details), "native-user")
	assert.Contains(t, fmt.Sprint(result.Details), "native_readback_hash")
}

func TestExecuteNotificationListDeclaresUnreadMutationAndNoRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/login/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"is_logged_in": true, "user_id": "native-user"},
			})
			return
		}
		assert.Equal(t, "/api/v1/notifications/list", r.URL.Path)
		assert.Equal(t, "mentions", r.URL.Query().Get("tab"))
		assert.Equal(t, "20", r.URL.Query().Get("limit"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"tab":   "mentions",
				"items": []map[string]any{{"comment_id": "stable-comment-1"}},
			},
		})
	}))
	defer server.Close()

	req := validRequest()
	req.Capability = "notifications.list"
	req.Tab = "mentions"
	req.Limit = 20
	result := execute(context.Background(), server.Client(), server.URL, "secret", req)

	assert.Equal(t, "READY", result.Status)
	assert.Equal(t, "read_marks_selected_notifications_seen", result.SideEffect)
	assert.False(t, result.RetrySafe)
	assert.False(t, result.ExternalActionPerformed)
	assert.Equal(t, 2, requests)
}

func TestExecuteNotificationListRejectsAmbiguousScopeBeforeNativeCall(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	req := validRequest()
	req.Capability = "notifications.list"
	result := execute(context.Background(), server.Client(), server.URL, "secret", req)

	require.NotNil(t, result.Error)
	assert.Equal(t, "REJECTED", result.Status)
	assert.Equal(t, "INVALID_NOTIFICATION_TAB", result.Error.Code)
	assert.False(t, result.RetrySafe)
	assert.False(t, called)
}

func TestExecuteNotificationListUnknownNeverBecomesRetrySafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/login/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"is_logged_in": true, "user_id": "native-user"},
			})
			return
		}
		http.Error(w, "temporary", http.StatusBadGateway)
	}))
	defer server.Close()

	req := validRequest()
	req.Capability = "notifications.list"
	req.Tab = "mentions"
	req.Limit = 20
	result := execute(context.Background(), server.Client(), server.URL, "secret", req)

	require.NotNil(t, result.Error)
	assert.Equal(t, "UNKNOWN", result.Status)
	assert.Equal(t, "NATIVE_HTTP_ERROR", result.Error.Code)
	assert.False(t, result.RetrySafe)
}

func TestDescriptorKeepsNotificationSideEffectsVersioned(t *testing.T) {
	capabilities := descriptor()["capabilities"].([]map[string]any)
	require.Len(t, capabilities, 3)
	assert.Equal(t, "1.0.0", capabilities[0]["version"])
	assert.Equal(t, "read_only", capabilities[1]["side_effect"])
	assert.True(t, capabilities[1]["retry_safe"].(bool))
	assert.Equal(t, "read_marks_selected_notifications_seen", capabilities[2]["side_effect"])
	assert.False(t, capabilities[2]["retry_safe"].(bool))
}
