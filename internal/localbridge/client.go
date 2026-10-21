package localbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/userconfig"
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
