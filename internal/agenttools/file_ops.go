package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

// 本文件：list_dir / stat_path——把「列目录、看属性」固化成工具，省掉 Agent 自己写 PowerShell。

const (
	listDirTimeoutSeconds = 30
	defaultListDirLimit   = 200
)

func (c *Core) listDir(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Limit == 0 {
		input.Limit = defaultListDirLimit
	}
	script := strings.NewReplacer(
		"{{PATH_BASE64}}", base64.StdEncoding.EncodeToString([]byte(input.Path)),
		"{{LIMIT}}", strconv.Itoa(input.Limit),
	).Replace(listDirScript)
	output, err := c.runDesktopScript(ctx, input.SessionID, script, listDirTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	var result struct {
		Kind      string `json:"kind"`
		Target    string `json:"target"`
		Truncated bool   `json:"truncated"`
		Returned  int    `json:"returned"`
		Entries   []struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Size     *int64 `json:"size"`
			Modified string `json:"modified"`
			Target   string `json:"target"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, &Error{Code: "FILE_LIST_FAILED", Message: "device returned an unexpected list_dir result"}
	}
	switch result.Kind {
	case "ok":
	case "not_directory":
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "path is not a directory", Hint: "use stat_path to inspect a file"}
	case "symlink_root":
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "path is a symbolic link",
			Hint: "list the resolved path instead: " + result.Target}
	default:
		return nil, &Error{Code: "FILE_LIST_FAILED", Message: "device returned an unknown list_dir result"}
	}
	entries := make([]map[string]any, 0, len(result.Entries))
	for _, entry := range result.Entries {
		item := map[string]any{
			"name":     entry.Name,
			"type":     entry.Type,
			"modified": entry.Modified,
		}
		if entry.Size != nil {
			item["size"] = *entry.Size
		}
		if entry.Target != "" {
			item["target"] = entry.Target
		}
		entries = append(entries, item)
	}
	return map[string]any{
		"session_id": input.SessionID,
		"path":       input.Path,
		"entries":    entries,
		"returned":   result.Returned,
		"truncated":  result.Truncated,
	}, nil
}

func (c *Core) statPath(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	script := strings.NewReplacer(
		"{{PATH_BASE64}}", base64.StdEncoding.EncodeToString([]byte(input.Path)),
	).Replace(statPathScript)
	output, err := c.runDesktopScript(ctx, input.SessionID, script, listDirTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	var result struct {
		Kind     string `json:"kind"`
		Type     string `json:"type"`
		Size     *int64 `json:"size"`
		Modified string `json:"modified"`
		Target   string `json:"target"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, &Error{Code: "FILE_LIST_FAILED", Message: "device returned an unexpected stat_path result"}
	}
	if result.Kind != "ok" {
		return nil, &Error{Code: "FILE_NOT_FOUND", Message: "path does not exist",
			Hint: "check the spelling with list_dir on the parent directory"}
	}
	data := map[string]any{
		"session_id": input.SessionID,
		"path":       input.Path,
		"type":       result.Type,
		"modified":   result.Modified,
	}
	if result.Size != nil {
		data["size"] = *result.Size
	}
	if result.Target != "" {
		data["target"] = result.Target
	}
	return data, nil
}
