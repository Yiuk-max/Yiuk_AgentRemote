package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

func (c *Core) waitFor(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		Condition string `json:"condition"`
		Value     string `json:"value"`
		Timeout   int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Timeout == 0 {
		input.Timeout = 10
	}
	script := strings.NewReplacer(
		"{{CONDITION}}", input.Condition,
		"{{VALUE_BASE64}}", base64.StdEncoding.EncodeToString([]byte(input.Value)),
		"{{TIMEOUT}}", strconv.Itoa(input.Timeout),
	).Replace(waitForScript)
	// 设备侧最多轮询 timeout 秒，命令超时留 10 秒余量给提交与取回结果。
	var result struct {
		Matched   bool   `json:"matched"`
		TimedOut  bool   `json:"timed_out"`
		ElapsedMS int    `json:"elapsed_ms"`
		Evidence  string `json:"evidence"`
	}
	if err := c.runDesktopJSON(ctx, input.SessionID, script, input.Timeout+10, &result); err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id": input.SessionID,
		"condition":  input.Condition,
		"value":      input.Value,
		"matched":    result.Matched,
		"timed_out":  result.TimedOut,
		"elapsed_ms": result.ElapsedMS,
		"evidence":   result.Evidence,
	}, nil
}

// wait 在 MCP 本地睡眠，不接触设备。被取消时返回实际等待时长，由调用方自行判断。
func (c *Core) wait(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		MS int `json:"ms"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	started := time.Now()
	if input.MS > 0 {
		timer := time.NewTimer(time.Duration(input.MS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return map[string]any{"waited_ms": int(time.Since(started).Milliseconds()), "cancelled": true}, nil
		}
	}
	return map[string]any{"waited_ms": int(time.Since(started).Milliseconds())}, nil
}
