package agenttools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestQuestionValidation(t *testing.T) {
	for _, raw := range []string{
		`{"session_id":"s","question":""}`, `{"session_id":"s","question":"q","options":null}`,
		`{"session_id":"s","question":"q","options":[null]}`, `{"session_id":"s","question":"q","options":["a"," a "]}`,
		`{"session_id":"s","question":"q","options":[" "]}`, `{"session_id":"s","question":"q","timeout_seconds":14}`,
		`{"session_id":"s","question":"q","timeout_seconds":301}`, `{"session_id":"s","question":"q","timeout_seconds":null}`,
		`{"session_id":"s","question":"q","options":{}}`, `{"session_id":"s","question":"q","title":"system"}`,
	} {
		if err := validateInput(json.RawMessage(raw), AskUser); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	raw, _ := json.Marshal(map[string]any{"session_id": "s", "question": strings.Repeat("😀", 1000), "options": []string{"中文", "other"}, "timeout_seconds": 15})
	if err := validateInput(raw, AskUser); err != nil {
		t.Fatal(err)
	}
	if response := NewCore(nil).Call(context.Background(), GetUserReply, json.RawMessage(`{"session_id":"s","request_id":"r"}`)); response.Code != "NOT_CONFIGURED" {
		t.Fatal(response)
	}
}
