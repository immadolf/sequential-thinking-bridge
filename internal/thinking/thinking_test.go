package thinking

import (
	"encoding/json"
	"io"
	"os"
	"testing"
)

func TestProcessThoughtAdjustsTotalAndTracksBranches(t *testing.T) {
	server := NewServer(false)

	result, err := server.Process(RawArguments{
		"thought":           "branch decision",
		"thoughtNumber":     3,
		"totalThoughts":     2,
		"nextThoughtNeeded": false,
		"branchFromThought": 1,
		"branchId":          "option-a",
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if result.ThoughtNumber != 3 {
		t.Fatalf("ThoughtNumber = %d, want 3", result.ThoughtNumber)
	}
	if result.TotalThoughts != 3 {
		t.Fatalf("TotalThoughts = %d, want adjusted value 3", result.TotalThoughts)
	}
	if result.NextThoughtNeeded {
		t.Fatalf("NextThoughtNeeded = true, want false")
	}
	if result.ThoughtHistoryLength != 1 {
		t.Fatalf("ThoughtHistoryLength = %d, want 1", result.ThoughtHistoryLength)
	}
	if len(result.Branches) != 1 || result.Branches[0] != "option-a" {
		t.Fatalf("Branches = %#v, want [option-a]", result.Branches)
	}
}

func TestProcessThoughtCoercesStringBooleansAndNumbers(t *testing.T) {
	server := NewServer(true)

	result, err := server.Process(RawArguments{
		"thought":           "coerce fields",
		"thoughtNumber":     "2",
		"totalThoughts":     "4",
		"nextThoughtNeeded": "false",
		"isRevision":        "true",
		"revisesThought":    "1",
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if result.ThoughtNumber != 2 || result.TotalThoughts != 4 {
		t.Fatalf("result = %+v, want coerced numeric fields", result)
	}
	if result.NextThoughtNeeded {
		t.Fatalf("NextThoughtNeeded = true, want coerced false")
	}
}

func TestProcessThoughtCoercesMixedCaseStringBooleansLikeOriginal(t *testing.T) {
	server := NewServer(true)

	result, err := server.Process(RawArguments{
		"thought":           "coerce mixed case",
		"thoughtNumber":     "1",
		"totalThoughts":     "1",
		"nextThoughtNeeded": "FaLsE",
		"isRevision":        "TrUe",
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if result.NextThoughtNeeded {
		t.Fatalf("NextThoughtNeeded = true, want mixed-case string false")
	}
}

func TestProcessThoughtReturnsBranchesInInsertionOrder(t *testing.T) {
	server := NewServer(true)
	for _, branchID := range []string{"beta", "alpha"} {
		_, err := server.Process(RawArguments{
			"thought":           "branch",
			"thoughtNumber":     1,
			"totalThoughts":     1,
			"nextThoughtNeeded": false,
			"branchFromThought": 1,
			"branchId":          branchID,
		})
		if err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
	}

	result, err := server.Process(RawArguments{
		"thought":           "read branches",
		"thoughtNumber":     1,
		"totalThoughts":     1,
		"nextThoughtNeeded": false,
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if got, want := result.Branches, []string{"beta", "alpha"}; got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Branches = %#v, want insertion order %#v", got, want)
	}
}

func TestProcessThoughtLogsToStderrByDefault(t *testing.T) {
	server := NewServer(false)
	output := captureStderr(t, func() {
		_, err := server.Process(RawArguments{
			"thought":           "visible thought",
			"thoughtNumber":     1,
			"totalThoughts":     1,
			"nextThoughtNeeded": false,
		})
		if err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
	})

	if output == "" {
		t.Fatal("stderr output is empty, want formatted thought")
	}
}

func TestProcessThoughtCanDisableStderrLogging(t *testing.T) {
	server := NewServer(true)
	output := captureStderr(t, func() {
		_, err := server.Process(RawArguments{
			"thought":           "hidden thought",
			"thoughtNumber":     1,
			"totalThoughts":     1,
			"nextThoughtNeeded": false,
		})
		if err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
	})

	if output != "" {
		t.Fatalf("stderr output = %q, want empty output", output)
	}
}

func TestProcessThoughtRejectsInvalidMinimums(t *testing.T) {
	server := NewServer(true)

	_, err := server.Process(RawArguments{
		"thought":           "bad number",
		"thoughtNumber":     0,
		"totalThoughts":     1,
		"nextThoughtNeeded": true,
	})
	if err == nil {
		t.Fatal("Process returned nil error, want validation error")
	}
}

func TestToolTextPayloadMatchesOfficialShape(t *testing.T) {
	server := NewServer(true)

	result, err := server.Process(RawArguments{
		"thought":           "payload",
		"thoughtNumber":     1,
		"totalThoughts":     1,
		"nextThoughtNeeded": false,
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	text, err := result.JSONText()
	if err != nil {
		t.Fatalf("JSONText returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("text is not JSON: %v", err)
	}
	if decoded["thoughtHistoryLength"].(float64) != 1 {
		t.Fatalf("thoughtHistoryLength = %v, want 1", decoded["thoughtHistoryLength"])
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = old
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(data)
}
