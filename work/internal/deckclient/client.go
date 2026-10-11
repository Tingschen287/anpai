// Package deckclient 是连 deckd 的小客户端（面板、侧栏、deck agent 命令共用）。
package deckclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client 经 unix socket 调 deckd。
type Client struct {
	hc *http.Client
	tr http.RoundTripper
	// Socket 是 deckd 的 unix socket 路径（deck center 的栅栏据此找到同目录的单实例锁）。
	Socket string
}

// New 建客户端。
func New(socket string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &Client{hc: &http.Client{Transport: tr, Timeout: DefaultTimeout}, tr: tr, Socket: socket}
}

// DefaultTimeout 是一般请求等 deckd 答复的上限。
const DefaultTimeout = 30 * time.Second

// LongTimeout 给开 agent、起主 agent、给子 agent 发话这类要等上游 CLI 就绪的请求（一次启动加送 brief 可能超过一分钟）。
const LongTimeout = 5 * time.Minute

// WithTimeout 返回一个等答复上限为 d 的副本（同一个 socket 与连接池）。
func (c *Client) WithTimeout(d time.Duration) *Client {
	return &Client{hc: &http.Client{Transport: c.tr, Timeout: d}, tr: c.tr, Socket: c.Socket}
}

// Error 是 deckd 返回的错误。
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("deckd %d：%s", e.Status, e.Message) }

// Do 发一个请求，把 JSON 结果解到 out（可为 nil）。
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://deckd"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ne net.Error
		var op *net.OpError
		dial := errors.As(err, &op) && op.Op == "dial"
		if !dial && (errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded)) {
			// 已经连上、只是等答复超时：操作可能还在 deckd 里进行，不能当成没发生
			return fmt.Errorf("deckd 在 %s 内没有答复（%w）：操作可能仍在进行，先用 deck agent list 核对，不要直接重试", c.hc.Timeout, err)
		}
		return fmt.Errorf("连不上 deckd（%w）：先运行 deck daemon", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = string(data)
		}
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Get 是 GET 的简写。
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}
