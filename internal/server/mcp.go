package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"sequential-thinking-bridge/internal/session"
	"sequential-thinking-bridge/internal/thinking"
)

const (
	jsonrpcVersion           = "2.0"
	mcpProtocolVersion       = "2026-07-28"
	legacyMCPProtocolVersion = "2025-06-18"
	serverName               = "sequential-thinking-bridge"
	serverVersion            = "0.1.0"
	cacheTTLMillis           = 60 * 60 * 1000
	sessionHeader            = "Mcp-Session-Id"

	errCodeParseError                 = -32700
	errCodeInvalidRequest             = -32600
	errCodeMethodNotFound             = -32601
	errCodeInvalidParams              = -32602
	errCodeInternalError              = -32603
	errCodeHeaderMismatch             = -32020
	errCodeUnsupportedProtocolVersion = -32022
)

// Config controls the HTTP MCP handler.
type Config struct {
	Store *session.Store
	Token string
}

// Server implements MCP 2026-07-28 with a 2025-06-18 compatibility path.
type Server struct {
	store *session.Store
	token string
}

// New creates an MCP HTTP handler for sequentialthinking.
func New(cfg Config) *Server {
	store := cfg.Store
	if store == nil {
		store = session.NewStore(2 * time.Hour)
	}
	return &Server{store: store, token: cfg.Token}
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type protocolError struct {
	code       int
	message    string
	data       any
	httpStatus int
}

type requestMetaEnvelope struct {
	Meta *requestMeta `json:"_meta"`
}

type requestMeta struct {
	ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion"`
	ClientInfo         json.RawMessage `json:"io.modelcontextprotocol/clientInfo,omitempty"`
	ClientCapabilities json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities"`
}

type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type callToolParams struct {
	Name      string                `json:"name"`
	Arguments thinking.RawArguments `json:"arguments,omitempty"`
}

// ServeHTTP handles one JSON-RPC request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !validOrigin(r.Header.Get("Origin")) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/healthz" {
		s.handleHealthz(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONRPCError(w, nil, protocolError{
			code:       errCodeInvalidRequest,
			message:    "only POST is allowed",
			httpStatus: http.StatusMethodNotAllowed,
		})
		return
	}
	if !s.authorized(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: missing or invalid Bearer token"})
		return
	}

	started := time.Now()
	method := "unknown"
	requestID := "null"
	outcome := "rejected"
	defer func() {
		fmt.Fprintf(os.Stderr, "INFO mcp request method=%s id=%s outcome=%s duration=%s\n", method, requestID, outcome, time.Since(started))
	}()

	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var req jsonRPCRequest
	if err := decoder.Decode(&req); err != nil {
		writeJSONRPCError(w, nil, protocolError{
			code:       errCodeParseError,
			message:    "parse error",
			httpStatus: http.StatusBadRequest,
		})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONRPCError(w, nil, protocolError{
			code:       errCodeParseError,
			message:    "parse error: request body must contain one JSON value",
			httpStatus: http.StatusBadRequest,
		})
		return
	}

	method = req.Method
	requestID = requestIDForLog(req.ID)
	fmt.Fprintf(
		os.Stderr,
		"DEBUG mcp request method=%s id=%s protocol=%q hasMethodHeader=%t hasLegacySession=%t\n",
		method,
		requestID,
		r.Header.Get("MCP-Protocol-Version"),
		r.Header.Get("Mcp-Method") != "",
		r.Header.Get(sessionHeader) != "",
	)

	if req.JSONRPC != jsonrpcVersion || req.Method == "" {
		writeJSONRPCError(w, req.ID, protocolError{
			code:       errCodeInvalidRequest,
			message:    "jsonrpc must be \"2.0\" and method must be present",
			httpStatus: http.StatusBadRequest,
		})
		return
	}
	if len(req.ID) == 0 {
		if strings.HasPrefix(req.Method, "notifications/") {
			outcome = "accepted"
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSONRPCError(w, nil, protocolError{
			code:       errCodeInvalidRequest,
			message:    "request id must be a string or integer",
			httpStatus: http.StatusBadRequest,
		})
		return
	}
	if !validRequestID(req.ID) {
		writeJSONRPCError(w, nil, protocolError{
			code:       errCodeInvalidRequest,
			message:    "request id must be a string or integer",
			httpStatus: http.StatusBadRequest,
		})
		return
	}
	legacy := isLegacyRequest(r, req)
	if !legacy {
		if err := validateModernRequest(r, req); err != nil {
			writeJSONRPCError(w, req.ID, *err)
			return
		}
	}

	result, responseSessionID, err := s.dispatch(r, req, legacy)
	if responseSessionID != "" {
		w.Header().Set(sessionHeader, responseSessionID)
	}
	if err != nil {
		writeJSONRPCError(w, req.ID, *err)
		return
	}
	outcome = "ok"
	writeJSONRPCResult(w, req.ID, result)
}

func (s *Server) dispatch(r *http.Request, req jsonRPCRequest, legacy bool) (any, string, *protocolError) {
	switch req.Method {
	case "initialize":
		return s.initialize(req.Params)
	case "server/discover":
		return discoverResult(), "", nil
	case "tools/list":
		return completeResult(map[string]any{
			"tools":      []any{sequentialThinkingTool()},
			"ttlMs":      cacheTTLMillis,
			"cacheScope": "public",
		}), "", nil
	case "tools/call":
		legacySessionID := ""
		if legacy {
			legacySessionID = r.Header.Get(sessionHeader)
		}
		result, err := s.callTool(req.Params, legacySessionID, legacy)
		return result, legacySessionID, err
	default:
		return nil, "", &protocolError{
			code:       errCodeMethodNotFound,
			message:    fmt.Sprintf("unknown method %q", req.Method),
			httpStatus: http.StatusNotFound,
		}
	}
}

func (s *Server) initialize(_ json.RawMessage) (any, string, *protocolError) {
	id, err := session.NewSessionID()
	if err != nil {
		return nil, "", &protocolError{
			code:       errCodeInternalError,
			message:    "create legacy session",
			httpStatus: http.StatusInternalServerError,
		}
	}
	s.store.Get(id, time.Now())
	return map[string]any{
		"protocolVersion": legacyMCPProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": serverInfo(),
	}, id, nil
}

func (s *Server) callTool(params json.RawMessage, legacySessionID string, legacy bool) (any, *protocolError) {
	var call callToolParams
	if len(params) == 0 {
		return nil, invalidParams("missing tools/call params")
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, invalidParams("invalid tools/call params")
	}
	if call.Name != "sequentialthinking" {
		return nil, invalidParams(fmt.Sprintf("unknown tool %q", call.Name))
	}
	if call.Arguments == nil {
		call.Arguments = thinking.RawArguments{}
	}

	var state *session.Session
	created := false
	if legacy {
		if legacySessionID == "" {
			return nil, invalidParams("missing Mcp-Session-Id header")
		}
		state = s.store.Get(legacySessionID, time.Now())
	} else {
		handle, err := thoughtHandle(call.Arguments)
		if err != nil {
			return toolErrorResult(err), nil
		}
		state, created, err = s.store.ResolveHandle(handle, time.Now())
		if err != nil {
			return toolErrorResult(err), nil
		}
	}

	started := time.Now()
	result, err := state.State.Process(call.Arguments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "INFO sequentialthinking handle=%s created=%t outcome=error duration=%s error=%q\n", shortHandle(state.ID), created, time.Since(started), err)
		return toolErrorResult(err), nil
	}
	result.ThoughtHandle = state.ID
	text, err := result.JSONText()
	if err != nil {
		return nil, &protocolError{
			code:       errCodeInternalError,
			message:    "encode tool result",
			httpStatus: http.StatusInternalServerError,
		}
	}
	fmt.Fprintf(
		os.Stderr,
		"INFO sequentialthinking handle=%s created=%t thought=%d/%d history=%d next=%t duration=%s\n",
		shortHandle(state.ID),
		created,
		result.ThoughtNumber,
		result.TotalThoughts,
		result.ThoughtHistoryLength,
		result.NextThoughtNeeded,
		time.Since(started),
	)
	return completeResult(map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": text},
		},
		"structuredContent": result,
		"isError":           false,
	}), nil
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+s.token
}

func (s *Server) handleHealthz(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"handles": s.store.Count(),
	})
}

func discoverResult() map[string]any {
	return completeResult(map[string]any{
		"supportedVersions": []string{mcpProtocolVersion, legacyMCPProtocolVersion},
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"ttlMs":      cacheTTLMillis,
		"cacheScope": "public",
	})
}

func completeResult(result map[string]any) map[string]any {
	result["resultType"] = "complete"
	result["_meta"] = map[string]any{
		"io.modelcontextprotocol/serverInfo": serverInfo(),
	}
	return result
}

func serverInfo() map[string]string {
	return map[string]string{
		"name":    serverName,
		"version": serverVersion,
	}
}

func sequentialThinkingTool() map[string]any {
	return map[string]any{
		"name":        "sequentialthinking",
		"title":       "Sequential Thinking",
		"description": sequentialThinkingDescription,
		"annotations": map[string]any{
			"readOnlyHint":    true,
			"destructiveHint": false,
			"openWorldHint":   false,
		},
		"inputSchema": sequentialThinkingInputSchema(),
		"outputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required": []string{
				"thoughtHandle",
				"thoughtNumber",
				"totalThoughts",
				"nextThoughtNeeded",
				"branches",
				"thoughtHistoryLength",
			},
			"properties": map[string]any{
				"thoughtHandle":        map[string]any{"type": "string"},
				"thoughtNumber":        map[string]any{"type": "integer"},
				"totalThoughts":        map[string]any{"type": "integer"},
				"nextThoughtNeeded":    map[string]any{"type": "boolean"},
				"branches":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"thoughtHistoryLength": map[string]any{"type": "integer"},
			},
		},
	}
}

func sequentialThinkingInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"thought", "nextThoughtNeeded", "thoughtNumber", "totalThoughts"},
		"properties": map[string]any{
			"thought":           map[string]any{"type": "string", "description": "Your current thinking step"},
			"nextThoughtNeeded": map[string]any{"type": "boolean", "description": "Whether another thought step is needed"},
			"thoughtNumber":     map[string]any{"type": "integer", "minimum": 1, "description": "Current thought number"},
			"totalThoughts":     map[string]any{"type": "integer", "minimum": 1, "description": "Estimated total thoughts needed"},
			"isRevision":        map[string]any{"type": "boolean", "description": "Whether this revises previous thinking"},
			"revisesThought":    map[string]any{"type": "integer", "minimum": 1, "description": "Which thought is being reconsidered"},
			"branchFromThought": map[string]any{"type": "integer", "minimum": 1, "description": "Branching point thought number"},
			"branchId":          map[string]any{"type": "string", "description": "Branch identifier"},
			"needsMoreThoughts": map[string]any{"type": "boolean", "description": "If more thoughts are needed"},
			"thoughtHandle": map[string]any{
				"type":        "string",
				"pattern":     "^[0-9a-f]{32}$",
				"description": "Opaque handle returned by the first call; pass it unchanged to continue the same thought sequence",
			},
		},
	}
}

const sequentialThinkingDescription = `A detailed tool for dynamic and reflective problem-solving through thoughts.
This tool helps analyze problems through a flexible thinking process that can adapt and evolve.
Each thought can build on, question, or revise previous insights as understanding deepens.

When to use this tool:
- Breaking down complex problems into steps
- Planning and design with room for revision
- Analysis that might need course correction
- Problems where the full scope might not be clear initially
- Problems that require a multi-step solution
- Tasks that need to maintain context over multiple steps
- Situations where irrelevant information needs to be filtered out

Key features:
- You can adjust total_thoughts up or down as you progress
- You can question or revise previous thoughts
- You can add more thoughts even after reaching what seemed like the end
- You can express uncertainty and explore alternative approaches
- Not every thought needs to build linearly - you can branch or backtrack
- Generates a solution hypothesis
- Verifies the hypothesis based on the Chain of Thought steps
- Repeats the process until satisfied
- Provides a correct answer

Parameters explained:
- thought: Your current thinking step, which can include:
  * Regular analytical steps
  * Revisions of previous thoughts
  * Questions about previous decisions
  * Realizations about needing more analysis
  * Changes in approach
  * Hypothesis generation
  * Hypothesis verification
- nextThoughtNeeded: True if you need more thinking, even if at what seemed like the end
- thoughtNumber: Current number in sequence (can go beyond initial total if needed)
- totalThoughts: Current estimate of thoughts needed (can be adjusted up/down)
- isRevision: A boolean indicating if this thought revises previous thinking
- revisesThought: If is_revision is true, which thought number is being reconsidered
- branchFromThought: If branching, which thought number is the branching point
- branchId: Identifier for the current branch (if any)
- needsMoreThoughts: If reaching end but realizing more thoughts needed
- thoughtHandle: Omit it on the first call. Copy the returned handle into later calls to continue the same thought sequence. Omitting it starts a new isolated sequence.

You should:
1. Start with an initial estimate of needed thoughts, but be ready to adjust
2. Feel free to question or revise previous thoughts
3. Don't hesitate to add more thoughts if needed, even at the "end"
4. Express uncertainty when present
5. Mark thoughts that revise previous thinking or branch into new paths
6. Ignore information that is irrelevant to the current step
7. Generate a solution hypothesis when appropriate
8. Verify the hypothesis based on the Chain of Thought steps
9. Repeat the process until satisfied with the solution
10. Provide a single, ideally correct answer as the final output
11. Only set nextThoughtNeeded to false when truly done and a satisfactory answer is reached`

func isLegacyRequest(r *http.Request, req jsonRPCRequest) bool {
	if req.Method == "initialize" {
		return true
	}
	if req.Method == "server/discover" {
		return false
	}
	if r.Header.Get("MCP-Protocol-Version") == legacyMCPProtocolVersion {
		return true
	}
	return r.Header.Get("MCP-Protocol-Version") == "" && r.Header.Get("Mcp-Method") == "" && r.Header.Get(sessionHeader) != ""
}

func validateModernRequest(r *http.Request, req jsonRPCRequest) *protocolError {
	protocolHeader := r.Header.Get("MCP-Protocol-Version")
	if protocolHeader == "" {
		message := "missing MCP-Protocol-Version header"
		if req.Method == "initialize" {
			message += "; supported protocol version is " + mcpProtocolVersion
		}
		return headerMismatch(message)
	}
	if methodHeader := r.Header.Get("Mcp-Method"); methodHeader == "" || methodHeader != req.Method {
		return headerMismatch(fmt.Sprintf("Mcp-Method header %q does not match body method %q", methodHeader, req.Method))
	}

	meta, err := requestMetadata(req.Params)
	if err != nil {
		return &protocolError{
			code:       errCodeInvalidParams,
			message:    err.Error(),
			httpStatus: http.StatusBadRequest,
		}
	}
	if protocolHeader != meta.ProtocolVersion {
		return headerMismatch(fmt.Sprintf("MCP-Protocol-Version header %q does not match request metadata %q", protocolHeader, meta.ProtocolVersion))
	}
	if meta.ProtocolVersion != mcpProtocolVersion {
		return &protocolError{
			code:    errCodeUnsupportedProtocolVersion,
			message: "Unsupported protocol version",
			data: map[string]any{
				"supported": []string{mcpProtocolVersion, legacyMCPProtocolVersion},
				"requested": meta.ProtocolVersion,
			},
			httpStatus: http.StatusBadRequest,
		}
	}

	if req.Method == "tools/call" {
		nameHeader := r.Header.Get("Mcp-Name")
		if nameHeader == "" {
			return headerMismatch("missing Mcp-Name header")
		}
		decodedName, err := decodeMCPHeaderValue(nameHeader)
		if err != nil {
			return headerMismatch("malformed Mcp-Name header")
		}
		var call callToolParams
		if err := json.Unmarshal(req.Params, &call); err != nil || call.Name == "" {
			return &protocolError{
				code:       errCodeInvalidParams,
				message:    "tools/call requires params.name",
				httpStatus: http.StatusBadRequest,
			}
		}
		if decodedName != call.Name {
			return headerMismatch(fmt.Sprintf("Mcp-Name header value %q does not match body value %q", decodedName, call.Name))
		}
	}
	return nil
}

func requestMetadata(params json.RawMessage) (*requestMeta, error) {
	if len(params) == 0 {
		return nil, fmt.Errorf("invalid params: missing _meta")
	}
	var envelope requestMetaEnvelope
	if err := json.Unmarshal(params, &envelope); err != nil || envelope.Meta == nil {
		return nil, fmt.Errorf("invalid params: missing or malformed _meta")
	}
	if envelope.Meta.ProtocolVersion == "" {
		return nil, fmt.Errorf("invalid params: missing io.modelcontextprotocol/protocolVersion")
	}
	var capabilities map[string]json.RawMessage
	if len(envelope.Meta.ClientCapabilities) == 0 || json.Unmarshal(envelope.Meta.ClientCapabilities, &capabilities) != nil || capabilities == nil {
		return nil, fmt.Errorf("invalid params: missing or malformed io.modelcontextprotocol/clientCapabilities")
	}
	if len(envelope.Meta.ClientInfo) > 0 {
		var info implementation
		if err := json.Unmarshal(envelope.Meta.ClientInfo, &info); err != nil || info.Name == "" || info.Version == "" {
			return nil, fmt.Errorf("invalid params: malformed io.modelcontextprotocol/clientInfo")
		}
	}
	return envelope.Meta, nil
}

func thoughtHandle(arguments thinking.RawArguments) (string, error) {
	value, ok := arguments["thoughtHandle"]
	if !ok || value == nil {
		return "", nil
	}
	handle, ok := value.(string)
	if !ok || len(handle) != 32 {
		return "", fmt.Errorf("thoughtHandle must be an opaque handle returned by this server")
	}
	for _, char := range handle {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return "", fmt.Errorf("thoughtHandle must be an opaque handle returned by this server")
		}
	}
	return handle, nil
}

func toolErrorResult(err error) map[string]any {
	return completeResult(map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": "sequentialthinking error: " + err.Error()},
		},
		"isError": true,
	})
}

func invalidParams(message string) *protocolError {
	return &protocolError{
		code:       errCodeInvalidParams,
		message:    message,
		httpStatus: http.StatusOK,
	}
}

func headerMismatch(message string) *protocolError {
	return &protocolError{
		code:       errCodeHeaderMismatch,
		message:    "Header mismatch: " + message,
		httpStatus: http.StatusBadRequest,
	}
}

func decodeMCPHeaderValue(value string) (string, error) {
	if strings.HasPrefix(value, "=?base64?") && strings.HasSuffix(value, "?=") {
		encoded := strings.TrimSuffix(strings.TrimPrefix(value, "=?base64?"), "?=")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || !utf8.Valid(decoded) {
			return "", fmt.Errorf("invalid base64 header value")
		}
		return string(decoded), nil
	}
	if value == "" || value != strings.TrimSpace(value) {
		return "", fmt.Errorf("invalid plain header value")
	}
	for _, char := range []byte(value) {
		if char < 0x20 || char > 0x7e {
			return "", fmt.Errorf("invalid plain header value")
		}
	}
	return value, nil
}

func validOrigin(raw string) bool {
	if raw == "" {
		return true
	}
	origin, err := url.Parse(raw)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.Path != "" {
		return false
	}
	host := origin.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validRequestID(id json.RawMessage) bool {
	if len(id) == 0 || string(id) == "null" {
		return false
	}
	if id[0] == '"' {
		var value string
		return json.Unmarshal(id, &value) == nil
	}
	var number json.Number
	return json.Unmarshal(id, &number) == nil && !strings.ContainsAny(number.String(), ".eE")
}

func requestIDForLog(id json.RawMessage) string {
	if len(id) == 0 {
		return "null"
	}
	value := string(id)
	if len(value) > 64 {
		value = value[:64] + "..."
	}
	return value
}

func shortHandle(handle string) string {
	if len(handle) <= 8 {
		return handle
	}
	return handle[:8]
}

func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	resp := jsonRPCResponse{JSONRPC: jsonrpcVersion, ID: id, Result: result}
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, err protocolError) {
	w.Header().Set("Content-Type", "application/json")
	if err.httpStatus != 0 {
		w.WriteHeader(err.httpStatus)
	}
	resp := jsonRPCResponse{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Error:   &jsonRPCError{Code: err.code, Message: err.message, Data: err.data},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
