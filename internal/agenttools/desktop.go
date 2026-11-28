package agenttools

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// 本文件：组合工具的公共骨架——超时参数、分发与脚本执行。

const (
	defaultScreenshotMaxWidth = 1568
	screenshotTimeoutSeconds  = 60
	clickTimeoutSeconds       = 30
	commandPollInterval       = 500 * time.Millisecond
)

// 组合工具：都不新增协议帧，靠 execute_command + get_command_result 在设备上跑
// scripts.go 里的 PowerShell。它们和 wait 的区别是需要后端，所以登记在单独的表里。
//
// 加一个组合工具 = 目录里加一条 schema + 这里加一行；TestEveryToolHasExactlyOneHandler
// 会检查两边没有漏项。
var composedTools = map[string]func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error){
	TakeScreenshot: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		return core.takeScreenshot(ctx, raw)
	},
	FindOnScreen: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.findOnScreen(ctx, raw)
		return data, nil, err
	},
	ClickTarget: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.clickTarget(ctx, raw)
		return data, nil, err
	},
	Click: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.click(ctx, raw)
		return data, nil, err
	}, TypeText: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.typeText(ctx, raw)
		return data, nil, err
	},
	PressKeys: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.pressKeys(ctx, raw)
		return data, nil, err
	},
	Drag: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.dragOnScreen(ctx, raw)
		return data, nil, err
	},
	MoveMouse: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.moveMouse(ctx, raw)
		return data, nil, err
	},
	WaitFor: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.waitFor(ctx, raw)
		return data, nil, err
	},
	ListDir: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.listDir(ctx, raw)
		return data, nil, err
	},
	StatPath: func(ctx context.Context, core *Core, raw json.RawMessage) (any, []Image, error) {
		data, err := core.statPath(ctx, raw)
		return data, nil, err
	},
}

// runDesktopJSON 运行组合脚本，并把 stdout 最后一行解析到 out。
// 所有输入类工具共用，避免每个 handler 重复“解析失败就报 DESKTOP_COMMAND_FAILED”。
func (c *Core) runDesktopJSON(ctx context.Context, sessionID, script string, timeoutSeconds int, out any) error {
	output, err := c.runDesktopScript(ctx, sessionID, script, timeoutSeconds)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(output), out); err != nil {
		return &Error{Code: "DESKTOP_COMMAND_FAILED", Message: "device returned an unexpected script result"}
	}
	return nil
}

// runDesktopScript 提交脚本并等待结束，返回 stdout 最后一个非空行。
func (c *Core) runDesktopScript(ctx context.Context, sessionID, script string, timeoutSeconds int) (string, error) {
	submitted, err := c.backend.Invoke(ctx, Request{Name: ExecuteCommand, Input: mustJSON(map[string]any{
		"session_id": sessionID, "command": script, "timeout_seconds": timeoutSeconds,
	})})
	if err != nil {
		return "", err
	}
	var task struct {
		RequestID string `json:"request_id"`
	}
	if err := decodeResult(submitted, &task); err != nil || task.RequestID == "" {
		return "", &Error{Code: "DESKTOP_COMMAND_FAILED", Message: "command submission returned no request_id"}
	}

	deadline := time.Now().Add(time.Duration(timeoutSeconds)*time.Second + 15*time.Second)
	query := mustJSON(map[string]any{"session_id": sessionID, "request_id": task.RequestID})
	for {
		raw, err := c.backend.Invoke(ctx, Request{Name: GetCommandResult, Input: query})
		if err != nil {
			return "", err
		}
		var result struct {
			Status string `json:"status"`
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Chunks []struct {
				Stream string `json:"stream"`
				Data   string `json:"data"`
			} `json:"chunks"`
		}
		if err := decodeResult(raw, &result); err != nil {
			return "", &Error{Code: "DESKTOP_COMMAND_FAILED", Message: "unexpected command result"}
		}
		for _, chunk := range result.Chunks {
			if chunk.Stream == "stderr" {
				result.Stderr += chunk.Data
			} else {
				result.Stdout += chunk.Data
			}
		}
		switch result.Status {
		case "", "queued", "running":
		case "succeeded":
			return lastNonEmptyLine(result.Stdout), nil
		default:
			return "", &Error{Code: "DESKTOP_COMMAND_FAILED", Message: result.Status + ": " + truncateText(strings.TrimSpace(result.Stderr), 800)}
		}
		if time.Now().After(deadline) {
			return "", &Error{Code: "DESKTOP_COMMAND_TIMEOUT", Message: "timed out waiting for the device"}
		}
		timer := time.NewTimer(commandPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", &Error{Code: "CANCELLED", Message: "tool call was cancelled"}
		case <-timer.C:
		}
	}
}
