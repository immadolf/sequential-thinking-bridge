package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sequential-thinking-bridge/internal/session"
	"sequential-thinking-bridge/internal/thinking"
)

const (
	jsonrpcVersion     = "2.0"
	mcpProtocolVersion = "2025-06-18"
	sessionHeader      = "Mcp-Session-Id"

	errCodeParseError     = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternalError  = -32603
)

// Config controls the HTTP MCP handler.
type Config struct {
	Store *session.Store
	Token string
}

// Server implements the minimal Streamable HTTP JSON-RPC surface required by MCP.
type Server struct {
	store *session.Store
	token string
}

// New creates an MCP HTTP handler for sequentialthinking.
func New(cfg Config) *Server {
	store := cfg.Store
	if store == nil {
		store = session.NewStore(2*time.Hour, false)
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
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type callToolParams struct {
	Name      string                `json:"name"`
	Arguments thinking.RawArguments `json:"arguments,omitempty"`
}

// ServeHTTP handles one JSON-RPC request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		s.handleHealthz(w)
		return
	}
	if r.Method != http.MethodPost {
		writeJSONRPCError(w, nil, errCodeInvalidRequest, "only POST is allowed", nil)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: missing or invalid Bearer token"})
		return
	}

	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var req jsonRPCRequest
	if err := decoder.Decode(&req); err != nil {
		writeJSONRPCError(w, nil, errCodeParseError, "parse error", err.Error())
		return
	}
	if req.JSONRPC != jsonrpcVersion {
		writeJSONRPCError(w, req.ID, errCodeInvalidRequest, "jsonrpc must be \"2.0\"", nil)
		return
	}

	if len(req.ID) == 0 && strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, sessionID, err := s.dispatch(r, req)
	if sessionID != "" {
		w.Header().Set(sessionHeader, sessionID)
	}
	if err != nil {
		code := errCodeInvalidParams
		if strings.HasPrefix(err.Error(), "unknown") {
			code = errCodeMethodNotFound
		}
		writeJSONRPCError(w, req.ID, code, err.Error(), nil)
		return
	}
	writeJSONRPCResult(w, req.ID, result)
}

func (s *Server) dispatch(r *http.Request, req jsonRPCRequest) (any, string, error) {
	switch req.Method {
	case "initialize":
		id := r.Header.Get(sessionHeader)
		if id == "" {
			generated, err := session.NewSessionID()
			if err != nil {
				return nil, "", err
			}
			id = generated
		}
		s.store.Get(id, time.Now())
		return initializeResult(), id, nil
	case "notifications/initialized":
		return nil, "", nil
	case "tools/list":
		return map[string]any{"tools": []any{sequentialThinkingTool()}}, "", nil
	case "tools/call":
		sessionState, err := s.store.Resolve(r.Header.Get(sessionHeader), time.Now())
		if err != nil {
			return nil, "", err
		}
		result, err := s.callTool(sessionState, req.Params)
		return result, sessionState.ID, err
	default:
		return nil, "", fmt.Errorf("unknown method %q", req.Method)
	}
}

func (s *Server) callTool(sessionState *session.Session, params json.RawMessage) (any, error) {
	var call callToolParams
	if len(params) == 0 {
		return nil, fmt.Errorf("invalid params: missing tools/call params")
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if call.Name != "sequentialthinking" {
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}
	if call.Arguments == nil {
		call.Arguments = thinking.RawArguments{}
	}

	result, err := sessionState.State.Process(call.Arguments)
	if err != nil {
		return nil, err
	}
	text, err := result.JSONText()
	if err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	return map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": text},
		},
		"structuredContent": result,
		"isError":           false,
	}, nil
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
		"status":   "ok",
		"sessions": s.store.Count(),
	})
}

func initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]string{
			"name":    "sequential-thinking-bridge",
			"version": "0.1.0",
		},
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
			"idempotentHint":  true,
			"openWorldHint":   false,
		},
		"inputSchema": sequentialThinkingInputSchema(),
		"outputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thoughtNumber":        map[string]any{"type": "number"},
				"totalThoughts":        map[string]any{"type": "number"},
				"nextThoughtNeeded":    map[string]any{"type": "boolean"},
				"branches":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"thoughtHistoryLength": map[string]any{"type": "number"},
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

func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	resp := jsonRPCResponse{JSONRPC: jsonrpcVersion, ID: id, Result: result}
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	if code == errCodeParseError {
		w.WriteHeader(http.StatusBadRequest)
	}
	resp := jsonRPCResponse{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: message, Data: data},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
