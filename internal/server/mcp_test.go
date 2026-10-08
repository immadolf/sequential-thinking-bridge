package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sequential-thinking-bridge/internal/session"
)

func TestMCPServerDiscoverAdvertises20260728(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	rec := serve(t, srv, modernRPCRequest(t, "server/discover", map[string]any{}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("Mcp-Session-Id = %q, want empty", got)
	}

	result := responseResult(t, rec)
	if result["resultType"] != "complete" {
		t.Fatalf("resultType = %v, want complete", result["resultType"])
	}
	versions := result["supportedVersions"].([]any)
	if len(versions) != 2 || versions[0] != mcpProtocolVersion || versions[1] != legacyMCPProtocolVersion {
		t.Fatalf("supportedVersions = %#v, want [%s %s]", versions, mcpProtocolVersion, legacyMCPProtocolVersion)
	}
	if result["ttlMs"].(float64) < 0 || result["cacheScope"] != "public" {
		t.Fatalf("invalid cache fields: %#v", result)
	}
	meta := result["_meta"].(map[string]any)
	if meta["io.modelcontextprotocol/serverInfo"] == nil {
		t.Fatalf("missing serverInfo: %#v", meta)
	}
}

func TestMCPLegacy20250618InitializeAndCall(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	initRec := serve(t, srv, rpcRequest(t, "initialize", map[string]any{
		"protocolVersion": legacyMCPProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "legacy-test", "version": "0"},
	}))
	if initRec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d; body=%s", initRec.Code, initRec.Body.String())
	}
	handle := initRec.Header().Get("Mcp-Session-Id")
	if handle == "" {
		t.Fatal("legacy initialize did not return Mcp-Session-Id")
	}

	call := func(number int) map[string]any {
		t.Helper()
		req := rpcRequest(t, "tools/call", map[string]any{
			"name":      "sequentialthinking",
			"arguments": validThoughtArguments(number, "legacy"),
		})
		req.Header.Set("MCP-Protocol-Version", legacyMCPProtocolVersion)
		req.Header.Set("Mcp-Session-Id", handle)
		rec := serve(t, srv, req)
		return responseResult(t, rec)["structuredContent"].(map[string]any)
	}

	if got := call(1)["thoughtHistoryLength"].(float64); got != 1 {
		t.Fatalf("first legacy history length = %v, want 1", got)
	}
	if got := call(2)["thoughtHistoryLength"].(float64); got != 2 {
		t.Fatalf("second legacy history length = %v, want 2", got)
	}
}

func TestMCPLegacyInitializeAcceptsNewerClientVersion(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	rec := serve(t, srv, rpcRequest(t, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "pi", "version": "1.1.0"},
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d; body=%s", rec.Code, rec.Body.String())
	}
	result := responseResult(t, rec)
	if got := result["protocolVersion"]; got != legacyMCPProtocolVersion {
		t.Fatalf("selected protocolVersion = %v, want %s", got, legacyMCPProtocolVersion)
	}
	if got := rec.Header().Get("Mcp-Session-Id"); got == "" {
		t.Fatal("legacy initialize did not return Mcp-Session-Id")
	}
}

func TestMCPToolsListUsesModernResultAndExplicitThoughtHandle(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	rec := serve(t, srv, modernRPCRequest(t, "tools/list", map[string]any{}))

	result := responseResult(t, rec)
	if result["resultType"] != "complete" || result["cacheScope"] != "public" {
		t.Fatalf("invalid tools/list result: %#v", result)
	}
	tool := result["tools"].([]any)[0].(map[string]any)
	properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if properties["thoughtHandle"] == nil {
		t.Fatalf("inputSchema missing thoughtHandle: %#v", properties)
	}
	description := tool["description"].(string)
	for _, required := range []string{
		"When to use this tool:",
		"Key features:",
		"Parameters explained:",
		"thoughtHandle",
		"You should:",
		"Only set nextThoughtNeeded to false",
	} {
		if !strings.Contains(description, required) {
			t.Fatalf("description missing %q", required)
		}
	}
}

func TestMCPToolsCallUsesExplicitThoughtHandle(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})

	first := callThought(t, srv, "", 1, "first")
	handle := first["thoughtHandle"].(string)
	if handle == "" {
		t.Fatal("first call did not mint thoughtHandle")
	}
	if first["thoughtHistoryLength"].(float64) != 1 {
		t.Fatalf("first history length = %v, want 1", first["thoughtHistoryLength"])
	}

	second := callThought(t, srv, handle, 2, "second")
	if second["thoughtHandle"] != handle {
		t.Fatalf("second handle = %v, want %s", second["thoughtHandle"], handle)
	}
	if second["thoughtHistoryLength"].(float64) != 2 {
		t.Fatalf("second history length = %v, want 2", second["thoughtHistoryLength"])
	}

	isolated := callThought(t, srv, "", 1, "isolated")
	if isolated["thoughtHandle"] == handle || isolated["thoughtHistoryLength"].(float64) != 1 {
		t.Fatalf("isolated result = %#v", isolated)
	}
}

func TestMCPToolValidationReturnsToolError(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	rec := serve(t, srv, modernRPCRequest(t, "tools/call", map[string]any{
		"name": "sequentialthinking",
		"arguments": map[string]any{
			"thoughtNumber":     1,
			"totalThoughts":     1,
			"nextThoughtNeeded": false,
		},
	}))

	result := responseResult(t, rec)
	if result["resultType"] != "complete" || result["isError"] != true {
		t.Fatalf("invalid tool error result: %#v", result)
	}
}

func TestMCPRejectsInvalidModernRequestMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
		code   int
	}{
		{
			name: "missing protocol header",
			mutate: func(req *http.Request) {
				req.Header.Del("MCP-Protocol-Version")
			},
			code: errCodeHeaderMismatch,
		},
		{
			name: "method header mismatch",
			mutate: func(req *http.Request) {
				req.Header.Set("Mcp-Method", "tools/list")
			},
			code: errCodeHeaderMismatch,
		},
		{
			name: "name header mismatch",
			mutate: func(req *http.Request) {
				req.Header.Set("Mcp-Name", "other")
			},
			code: errCodeHeaderMismatch,
		},
		{
			name: "unsupported protocol version",
			mutate: func(req *http.Request) {
				req.Header.Set("MCP-Protocol-Version", "1900-01-01")
				body := map[string]any{
					"jsonrpc": "2.0",
					"id":      1,
					"method":  "tools/call",
					"params": map[string]any{
						"name":      "sequentialthinking",
						"arguments": validThoughtArguments(1, "version"),
						"_meta":     modernMeta("1900-01-01"),
					},
				}
				data, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				req.Body = io.NopCloser(bytes.NewReader(data))
			},
			code: errCodeUnsupportedProtocolVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(Config{Store: session.NewStore(time.Hour)})
			req := modernRPCRequest(t, "tools/call", map[string]any{
				"name":      "sequentialthinking",
				"arguments": validThoughtArguments(1, tt.name),
			})
			tt.mutate(req)
			rec := serve(t, srv, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if got := responseErrorCode(t, rec); got != tt.code {
				t.Fatalf("error code = %d, want %d", got, tt.code)
			}
		})
	}
}

func TestMCPRejectsMissingRequestMeta(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	req := rpcRequest(t, "tools/list", map[string]any{})
	setModernHeaders(req, "tools/list", "")
	rec := serve(t, srv, req)

	if rec.Code != http.StatusBadRequest || responseErrorCode(t, rec) != errCodeInvalidParams {
		t.Fatalf("status/body = %d/%s, want 400 invalid params", rec.Code, rec.Body.String())
	}
}

func TestMCPAcceptsEncodedMcpNameHeader(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	req := modernRPCRequest(t, "tools/call", map[string]any{
		"name":      "sequentialthinking",
		"arguments": validThoughtArguments(1, "encoded header"),
	})
	req.Header.Set("Mcp-Name", "=?base64?"+base64.StdEncoding.EncodeToString([]byte("sequentialthinking"))+"?=")
	rec := serve(t, srv, req)

	if rec.Code != http.StatusOK || responseResult(t, rec)["isError"] == true {
		t.Fatalf("status/body = %d/%s, want successful tool call", rec.Code, rec.Body.String())
	}
}

func TestMCPUnknownMethodUsesHTTP404(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	rec := serve(t, srv, modernRPCRequest(t, "unknown/method", map[string]any{}))

	if rec.Code != http.StatusNotFound || responseErrorCode(t, rec) != errCodeMethodNotFound {
		t.Fatalf("status/body = %d/%s, want 404 method not found", rec.Code, rec.Body.String())
	}
}

func TestMCPRejectsExternalOrigin(t *testing.T) {
	srv := New(Config{Store: session.NewStore(time.Hour)})
	req := modernRPCRequest(t, "tools/list", map[string]any{})
	req.Header.Set("Origin", "https://example.com")
	rec := serve(t, srv, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func callThought(t *testing.T, srv http.Handler, handle string, number int, thought string) map[string]any {
	t.Helper()
	arguments := validThoughtArguments(number, thought)
	if handle != "" {
		arguments["thoughtHandle"] = handle
	}
	rec := serve(t, srv, modernRPCRequest(t, "tools/call", map[string]any{
		"name":      "sequentialthinking",
		"arguments": arguments,
	}))
	if got := rec.Header().Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("Mcp-Session-Id = %q, want empty", got)
	}
	result := responseResult(t, rec)
	if result["isError"] == true {
		t.Fatalf("tools/call returned tool error: %#v", result)
	}
	return result["structuredContent"].(map[string]any)
}

func validThoughtArguments(number int, thought string) map[string]any {
	return map[string]any{
		"thought":           thought,
		"thoughtNumber":     number,
		"totalThoughts":     3,
		"nextThoughtNeeded": number < 3,
	}
}

func modernRPCRequest(t *testing.T, method string, params map[string]any) *http.Request {
	t.Helper()
	params["_meta"] = modernMeta(mcpProtocolVersion)
	req := rpcRequest(t, method, params)
	name, _ := params["name"].(string)
	setModernHeaders(req, method, name)
	return req
}

func modernMeta(protocolVersion string) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    protocolVersion,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		"io.modelcontextprotocol/clientInfo": map[string]any{
			"name":    "test",
			"version": "0",
		},
	}
}

func setModernHeaders(req *http.Request, method, name string) {
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
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

func serve(t *testing.T, srv http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func responseResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	decodeResponse(t, rec, &body)
	if body["error"] != nil {
		t.Fatalf("JSON-RPC error: %#v", body["error"])
	}
	return body["result"].(map[string]any)
}

func responseErrorCode(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var body map[string]any
	decodeResponse(t, rec, &body)
	return int(body["error"].(map[string]any)["code"].(float64))
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}
