// Package logging 提供平台各进程共用的日志初始化。
//
// 日志写 JSON 行；指定了文件就写文件（0600），否则写 stderr。
// 平台日志只记录脱敏 ID、状态与错误码，不记录临时密码、命令正文与完整输出。
package logging

import (
	"fmt"
	"log/slog"
	"os"
)

// Setup 建立日志记录器，返回关闭函数。
func Setup(path string) (func(), *slog.Logger, error) {
	handler := slog.NewJSONHandler(os.Stderr, nil)
	if path == "" {
		return func() {}, slog.New(handler), nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	handler = slog.NewJSONHandler(file, nil)
	closeFn := func() {
		if err := file.Sync(); err != nil {
			fmt.Fprintln(os.Stderr, "platform log sync error:", err)
		}
		_ = file.Close()
	}
	return closeFn, slog.New(handler), nil
}
