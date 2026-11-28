package localbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/userconfig"
)

const configFileName = "local-api.json"

type Config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type Client struct {
	url   string
	token string
	http  *http.Client
}

func ConfigPath() (string, error) {
	return userconfig.Path(configFileName)
}

func NewFromUserConfig() (*Client, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse local Qt bridge config: %w", err)
	}
	if !strings.HasPrefix(cfg.URL, "http://127.0.0.1:") || len(cfg.Token) < 32 {
		return nil, errors.New("invalid local Qt bridge configuration")
	}
	return New(cfg), nil
}

func New(cfg Config) *Client {
	return &Client{
		url:   strings.TrimRight(cfg.URL, "/"),
		token: cfg.Token,
		http:  &http.Client{Timeout: 10 * time.Minute},
	}
}

func (c *Client) Invoke(ctx context.Context, req agenttools.Request) (any, error) {
	body, err := json.Marshal(req.Input)
	if err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid tool input"}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/tool/"+req.Name, bytes.NewReader(body))
	if err != nil {
		return nil, &agenttools.Error{Code: "INTERNAL", Message: "could not create local bridge request"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(httpReq)
	if err != nil {
		return nil, &agenttools.Error{Code: "LOCAL_CLIENT_UNAVAILABLE", Message: "Yiuk Qt client is not running or its local bridge is unavailable"}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, &agenttools.Error{Code: "LOCAL_BRIDGE_ERROR", Message: "could not read response from Yiuk Qt client"}
	}
	var result struct {
		OK      bool            `json:"ok"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, &agenttools.Error{Code: "LOCAL_BRIDGE_ERROR", Message: "Yiuk Qt client returned an invalid response"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.OK {
		code := result.Code
		if code == "" {
			code = "LOCAL_BRIDGE_ERROR"
		}
		return nil, &agenttools.Error{Code: code, Message: result.Message}
	}
	var value any
	if len(result.Data) > 0 {
		if err := json.Unmarshal(result.Data, &value); err != nil {
			return nil, &agenttools.Error{Code: "LOCAL_BRIDGE_ERROR", Message: "Qt bridge returned malformed tool data"}
		}
	}
	return value, nil
}

// InvokeNative 让本地后端也支持原生操作（抓屏 / 鼠标 / 视觉定位）。
//
// 与平台链路共用同一套 operation 与业务信封，只是传输换成 loopback + token 的
// POST /native，并且客户端会自己做会话校验与 dispatcher 去重（关一次就够）。
func (c *Client) InvokeNative(ctx context.Context, sessionID, operation string, arguments json.RawMessage,
	timeoutSeconds int) (json.RawMessage, error) {
	sessionID = strings.TrimSpace(sessionID)
	operation = strings.TrimSpace(operation)
	if sessionID == "" || operation == "" {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "session_id and operation are required"}
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "arguments must be valid JSON"}
	}
	timeout := timeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	if timeout > 25 {
		timeout = 25
	}

	body, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"request_id":      randomRequestID(),
		"operation":       operation,
		"arguments":       arguments,
		"timeout_seconds": timeout,
	})
	if err != nil {
		return nil, &agenttools.Error{Code: "INTERNAL", Message: "could not encode the native request"}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/native", bytes.NewReader(body))
	if err != nil {
		return nil, &agenttools.Error{Code: "INTERNAL", Message: "could not create local bridge request"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(httpReq)
	if err != nil {
		return nil, &agenttools.Error{Code: "LOCAL_CLIENT_UNAVAILABLE",
			Message: "Yiuk Qt client is not running or its local bridge is unavailable"}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, &agenttools.Error{Code: "LOCAL_BRIDGE_ERROR",
			Message: "could not read the native result from Yiuk Qt client"}
	}

	var envelope struct {
		OK      bool            `json:"ok"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Hint    string          `json:"hint"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, &agenttools.Error{Code: "LOCAL_BRIDGE_ERROR",
			Message: "Yiuk Qt client returned an invalid native result"}
	}
	if !envelope.OK {
		code := envelope.Code
		if code == "" {
			code = "NATIVE_OPERATION_FAILED"
		}
		// 失败也带现场（例如 click_target 的候选列表），与平台链路一致。
		var failureData any
		if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
			if err := json.Unmarshal(envelope.Data, &failureData); err != nil {
				failureData = nil
			}
		}
		return nil, &agenttools.Error{Code: code, Message: envelope.Message, Hint: envelope.Hint, Data: failureData}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return json.RawMessage(`{}`), nil
	}
	return envelope.Data, nil
}

// randomRequestID 只用于本地桥的 request_id 去重（客户端按它挡重复）。
func randomRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("local-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
