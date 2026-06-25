package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sequential-thinking-bridge/internal/session"
)

func TestMCPInitializeListsToolAndSetsSessionHeader(t *testing.T) {
	srv := New(Config{
		Store: session.NewStore(time.Hour, false),
	})

	rec := httptest.NewRecorder()
	req := rpcRequest(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Mcp-Session-Id") == "" {
		t.Fatal("missing Mcp-Session-Id response header")
	}
	var body map[string]any
	decodeResponse(t, rec, &body)
	result := body["result"].(map[string]any)
	if result["protocolVersion"] == "" {
		t.Fatalf("initialize result missing protocolVersion: %#v", result)
	}
}

func TestMCPToolsListUsesFullOriginalDescription(t *testing.T) {
	srv := New(Config{
		Store: session.NewStore(time.Hour, false),
	})

	sessionID := initializeSession(t, srv)
	rec := httptest.NewRecorder()
	req := rpcRequest(t, "tools/list", map[string]any{})
	req.Header.Set("Mcp-Session-Id", sessionID)
	srv.ServeHTTP(rec, req)

	var body map[string]any
	decodeResponse(t, rec, &body)
	tool := body["result"].(map[string]any)["tools"].([]any)[0].(map[string]any)
	description := tool["description"].(string)
	for _, required := range []string{
		"When to use this tool:",
		"Key features:",
		"Parameters explained:",
		"You should:",
		"Only set nextThoughtNeeded to false",
	} {
		if !strings.Contains(description, required) {
			t.Fatalf("description missing %q", required)
		}
	}
}

func TestMCPToolsCallKeepsSessionsIsolated(t *testing.T) {
	srv := New(Config{
		Store: session.NewStore(time.Hour, false),
	})

	sessionA := initializeSession(t, srv)
	sessionB := initializeSession(t, srv)

	callThought(t, srv, sessionA, 1, "a")
	callThought(t, srv, sessionB, 1, "b")
	aSecond := callThought(t, srv, sessionA, 2, "a2")

	content := aSecond["content"].([]any)[0].(map[string]any)
	var payload map[string]any
	if err := json.Unmarshal([]byte(content["text"].(string)), &payload); err != nil {
		t.Fatalf("decode tool text: %v", err)
	}
	if payload["thoughtHistoryLength"].(float64) != 2 {
		t.Fatalf("session A history length = %v, want 2", payload["thoughtHistoryLength"])
	}
}

func TestMCPToolsCallRequiresSessionWhenDefaultDisabled(t *testing.T) {
	srv := New(Config{
		Store: session.NewStore(time.Hour, false),
	})

	rec := httptest.NewRecorder()
	req := rpcRequest(t, "tools/call", map[string]any{
		"name": "sequentialthinking",
		"arguments": map[string]any{
			"thought":           "missing session",
			"thoughtNumber":     1,
			"totalThoughts":     1,
			"nextThoughtNeeded": false,
		},
	})
	srv.ServeHTTP(rec, req)

	var body map[string]any
	decodeResponse(t, rec, &body)
	if body["error"] == nil {
		t.Fatalf("body=%#v, want JSON-RPC error", body)
	}
}

func initializeSession(t *testing.T, srv http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := rpcRequest(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d", rec.Code)
	}
	id := rec.Header().Get("Mcp-Session-Id")
	if id == "" {
		t.Fatal("initialize did not return session id")
	}
	return id
}

func callThought(t *testing.T, srv http.Handler, sessionID string, number int, thought string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := rpcRequest(t, "tools/call", map[string]any{
		"name": "sequentialthinking",
		"arguments": map[string]any{
			"thought":           thought,
			"thoughtNumber":     number,
			"totalThoughts":     3,
			"nextThoughtNeeded": number < 3,
		},
	})
	req.Header.Set("Mcp-Session-Id", sessionID)
	srv.ServeHTTP(rec, req)

	var body map[string]any
	decodeResponse(t, rec, &body)
	if body["error"] != nil {
		t.Fatalf("tools/call error: %#v", body["error"])
	}
	return body["result"].(map[string]any)
}

func rpcRequest(t *testing.T, method string, params any) *http.Request {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}
