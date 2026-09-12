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
