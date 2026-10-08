package thinking

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// RawArguments contains the untyped JSON arguments from tools/call.
type RawArguments map[string]any

// ThoughtData is the normalized payload accepted by the sequentialthinking tool.
type ThoughtData struct {
	Thought           string `json:"thought"`
	ThoughtNumber     int    `json:"thoughtNumber"`
	TotalThoughts     int    `json:"totalThoughts"`
	IsRevision        bool   `json:"isRevision,omitempty"`
	RevisesThought    int    `json:"revisesThought,omitempty"`
	BranchFromThought int    `json:"branchFromThought,omitempty"`
	BranchID          string `json:"branchId,omitempty"`
	NeedsMoreThoughts bool   `json:"needsMoreThoughts,omitempty"`
	NextThoughtNeeded bool   `json:"nextThoughtNeeded"`
}

// Result is the structured response returned by the tool.
type Result struct {
	ThoughtHandle        string   `json:"thoughtHandle,omitempty"`
	ThoughtNumber        int      `json:"thoughtNumber"`
	TotalThoughts        int      `json:"totalThoughts"`
	NextThoughtNeeded    bool     `json:"nextThoughtNeeded"`
	Branches             []string `json:"branches"`
	ThoughtHistoryLength int      `json:"thoughtHistoryLength"`
}

// JSONText serializes Result exactly as MCP text content.
func (r Result) JSONText() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Server stores one isolated sequential-thinking state.
type Server struct {
	mu                    sync.Mutex
	thoughtHistory        []ThoughtData
	branches              map[string][]ThoughtData
	branchOrder           []string
	disableThoughtLogging bool
	marks                 []string
}

// NewServer creates an isolated sequential-thinking state machine.
func NewServer(disableThoughtLogging bool) *Server {
	return &Server{
		branches:              make(map[string][]ThoughtData),
		disableThoughtLogging: disableThoughtLogging,
	}
}

// NewServerFromEnv creates a server that follows DISABLE_THOUGHT_LOGGING.
func NewServerFromEnv() *Server {
	return NewServer(DisableThoughtLoggingFromEnv())
}

// DisableThoughtLoggingFromEnv mirrors the original TypeScript constructor.
func DisableThoughtLoggingFromEnv() bool {
	return strings.ToLower(os.Getenv("DISABLE_THOUGHT_LOGGING")) == "true"
}

// Process validates, stores, and summarizes one sequential thought.
func (s *Server) Process(args RawArguments) (Result, error) {
	input, err := normalize(args)
	if err != nil {
		return Result{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if input.ThoughtNumber > input.TotalThoughts {
		input.TotalThoughts = input.ThoughtNumber
	}

	s.thoughtHistory = append(s.thoughtHistory, input)
	if input.BranchFromThought > 0 && input.BranchID != "" {
		if _, ok := s.branches[input.BranchID]; !ok {
			s.branchOrder = append(s.branchOrder, input.BranchID)
		}
		s.branches[input.BranchID] = append(s.branches[input.BranchID], input)
	}

	if !s.disableThoughtLogging {
		fmt.Fprintln(os.Stderr, formatThought(input))
	}

	branches := make([]string, len(s.branchOrder))
	copy(branches, s.branchOrder)

	return Result{
		ThoughtNumber:        input.ThoughtNumber,
		TotalThoughts:        input.TotalThoughts,
		NextThoughtNeeded:    input.NextThoughtNeeded,
		Branches:             branches,
		ThoughtHistoryLength: len(s.thoughtHistory),
	}, nil
}

// Mark is a tiny observable hook used by session-store tests.
func (s *Server) Mark(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks = append(s.marks, value)
}

// Marks returns values stored through Mark.
func (s *Server) Marks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.marks))
	copy(out, s.marks)
	return out
}

func normalize(args RawArguments) (ThoughtData, error) {
	thought, err := requireString(args, "thought")
	if err != nil {
		return ThoughtData{}, err
	}
	thoughtNumber, err := requireInt(args, "thoughtNumber")
	if err != nil {
		return ThoughtData{}, err
	}
	totalThoughts, err := requireInt(args, "totalThoughts")
	if err != nil {
		return ThoughtData{}, err
	}
	nextThoughtNeeded, err := requireBool(args, "nextThoughtNeeded")
	if err != nil {
		return ThoughtData{}, err
	}
	if thoughtNumber < 1 {
		return ThoughtData{}, fmt.Errorf("thoughtNumber must be >= 1")
	}
	if totalThoughts < 1 {
		return ThoughtData{}, fmt.Errorf("totalThoughts must be >= 1")
	}

	isRevision, err := optionalBool(args, "isRevision")
	if err != nil {
		return ThoughtData{}, err
	}
	revisesThought, err := optionalInt(args, "revisesThought")
	if err != nil {
		return ThoughtData{}, err
	}
	branchFromThought, err := optionalInt(args, "branchFromThought")
	if err != nil {
		return ThoughtData{}, err
	}
	branchID, err := optionalString(args, "branchId")
	if err != nil {
		return ThoughtData{}, err
	}
	needsMoreThoughts, err := optionalBool(args, "needsMoreThoughts")
	if err != nil {
		return ThoughtData{}, err
	}

	if revisesThought < 0 || branchFromThought < 0 {
		return ThoughtData{}, fmt.Errorf("optional numeric fields must be >= 1 when provided")
	}

	return ThoughtData{
		Thought:           thought,
		ThoughtNumber:     thoughtNumber,
		TotalThoughts:     totalThoughts,
		IsRevision:        isRevision,
		RevisesThought:    revisesThought,
		BranchFromThought: branchFromThought,
		BranchID:          branchID,
		NeedsMoreThoughts: needsMoreThoughts,
		NextThoughtNeeded: nextThoughtNeeded,
	}, nil
}

func requireString(args RawArguments, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	return asString(value, key)
}

func optionalString(args RawArguments, key string) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", nil
	}
	return asString(value, key)
}

func asString(value any, key string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return text, nil
}

func requireBool(args RawArguments, key string) (bool, error) {
	value, ok := args[key]
	if !ok {
		return false, fmt.Errorf("%s is required", key)
	}
	return asBool(value, key)
}

func optionalBool(args RawArguments, key string) (bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return false, nil
	}
	return asBool(value, key)
}

func asBool(value any, key string) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		switch strings.ToLower(v) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return false, fmt.Errorf("%s must be a boolean", key)
		}
	default:
		return false, fmt.Errorf("%s must be a boolean", key)
	}
}

func requireInt(args RawArguments, key string) (int, error) {
	value, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	return asInt(value, key)
}

func optionalInt(args RawArguments, key string) (int, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, nil
	}
	return asInt(value, key)
}

func asInt(value any, key string) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int(v), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int(n), nil
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
}

func formatThought(input ThoughtData) string {
	prefix := "Thought"
	context := ""
	switch {
	case input.IsRevision:
		prefix = "Revision"
		context = fmt.Sprintf(" revising thought %d", input.RevisesThought)
	case input.BranchFromThought > 0:
		prefix = "Branch"
		context = fmt.Sprintf(" from thought %d, ID: %s", input.BranchFromThought, input.BranchID)
	}
	return fmt.Sprintf("%s %d/%d%s: %s", prefix, input.ThoughtNumber, input.TotalThoughts, context, input.Thought)
}
