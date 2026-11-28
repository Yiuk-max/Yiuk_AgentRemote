package agenttools

import (
	"encoding/json"
	"strings"
)

// 本文件：通用小工具（JSON 编解码、文本截断）。

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// decodeResult 将后端返回的任意结构转换为目标类型；两种后端的结果字段名一致，但具体 Go 类型不同。
func decodeResult(value any, out any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
