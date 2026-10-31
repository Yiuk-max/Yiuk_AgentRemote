// Command platform runs the AgentRemote remote-assist platform server.
//
// The server is intentionally in-memory: restarting it invalidates every
// device password, connection, session and task.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"agentremote/internal/platform/config"
	"agentremote/internal/platform/server"
)

func main() {
	configPath := flag.String("config", "platform.yaml", "path to the platform YAML configuration")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform config error:", err)
		os.Exit(1)
	}

	closeLog, logger, err := setupLogging(cfg.LogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform log error:", err)
		os.Exit(1)
	}
	defer closeLog()
	slog.SetDefault(logger)

	srv, err := server.New(cfg, logger)

	if err != nil {
		logger.Error("could not start platform listener", "error", err)
		os.Exit(1)
	}
	if len(cfg.PlayerPassword) < config.RecommendedPlayerPasswordLen {
		logger.Warn("player_password is shorter than recommended; use a longer random secret for public deployments",
			"length", len(cfg.PlayerPassword), "recommended", config.RecommendedPlayerPasswordLen)
	}
	logger.Info("platform server listening", "listen", cfg.Listen, "tls", cfg.TLSCert != "", "insecure", cfg.AllowInsecure)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) //优雅退出，context程序退出
	defer stop()

	if err := srv.Run(ctx); err != nil { //开始运行服务器
		logger.Error("platform server stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("platform server stopped")
}

// 建立日志记录器，并返回关闭函数、日志记录器和错误。
func setupLogging(path string) (func(), *slog.Logger, error) {
	handler := slog.NewJSONHandler(os.Stderr, nil)
	if path == "" { //没有指定日志文件路径，直接输出到终端
		return func() {}, slog.New(handler), nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	//                              没有就创建，追加写入，只写模式，0600 是新创建文件时使用的权限
	if err != nil {
		return nil, nil, err
	}
	handler = slog.NewJSONHandler(file, nil)
	closeFn := func() { // Sync 尝试将文件当前缓冲或等待写入的数据同步到底层存储
		if err := file.Sync(); err != nil {
			fmt.Fprintln(os.Stderr, "platform log sync error:", err)
		}
		_ = file.Close()
	}
	return closeFn, slog.New(handler), nil
}
