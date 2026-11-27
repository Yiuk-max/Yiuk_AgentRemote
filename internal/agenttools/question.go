package agenttools

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func validateQuestionInput(fields map[string]json.RawMessage) error {
	var text string
	if err := json.Unmarshal(fields["question"], &text); err != nil || strings.TrimSpace(text) == "" || utf8.RuneCountInString(strings.TrimSpace(text)) > 1000 {
		return fmt.Errorf("question must contain 1–1000 characters")
	}
	if raw, ok := fields["options"]; ok {
		var items []json.RawMessage
		if string(raw) == "null" || json.Unmarshal(raw, &items) != nil || len(items) > 6 {
			return fmt.Errorf("options must be an array of at most six strings")
		}
		seen := map[string]bool{}
		for _, item := range items {
			var option string
			if string(item) == "null" || json.Unmarshal(item, &option) != nil {
				return fmt.Errorf("options must contain strings")
			}
			option = strings.TrimSpace(option)
			if option == "" || utf8.RuneCountInString(option) > 100 || seen[option] {
				return fmt.Errorf("options must be nonempty, unique and at most 100 characters each")
			}
			seen[option] = true
		}
	}
	if raw, ok := fields["timeout_seconds"]; ok {
		var timeout int
		if string(raw) == "null" || json.Unmarshal(raw, &timeout) != nil || timeout < 15 || timeout > 300 {
			return fmt.Errorf("timeout_seconds must be between 15 and 300")
		}
	}
	return nil
}
