// Package deckclient 是连 deckd 的小客户端（面板、侧栏、deck agent 命令共用）。
package deckclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Client 经 unix socket 调 deckd。
type Client struct {
	hc *http.Client
	tr http.RoundTripper
	// neutral 为真：请求带 i18n.MarkHeader，deckd 在线路上留着中性打码标记，界面按当前语言翻译。
	neutral bool
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

// NewUI 建给界面用的客户端。界面会把线路上的中性打码标记按当前语言翻成「[已打码]」或「[redacted]」，
// 所以请求带上 i18n.MarkHeader。deck agent 命令、模型、其他工具用 New：deckd 给它们的永远是中文「[已打码]」。
func NewUI(socket string) *Client {
	c := New(socket)
	c.neutral = true
	return c
}

// DefaultTimeout 是一般请求等 deckd 答复的上限。
const DefaultTimeout = 30 * time.Second

// LongTimeout 给开 agent、起主 agent、给子 agent 发话这类要等上游 CLI 就绪的请求（一次启动加送 brief 可能超过一分钟）。
const LongTimeout = 5 * time.Minute

// WithTimeout 返回一个等答复上限为 d 的副本（同一个 socket 与连接池）。
func (c *Client) WithTimeout(d time.Duration) *Client {
	return &Client{hc: &http.Client{Transport: c.tr, Timeout: d}, tr: c.tr, Socket: c.Socket, neutral: c.neutral}
}

// Error 是 deckd 返回的错误。
// Code 和 Args 是给界面翻译用的。旧客户端只读 Message。
type Error struct {
	Status  int
	Message string
	Code    string
	Args    []any
}

func (e *Error) Error() string { return i18n.E("err.deckclient.status", e.Status, e.Message).Error() }

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
	if c.neutral {
		req.Header.Set(i18n.MarkHeader, i18n.MarkNeutral)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ne net.Error
		var op *net.OpError
		dial := errors.As(err, &op) && op.Op == "dial"
		if !dial && (errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded)) {
			// 已经连上、只是等答复超时：操作可能还在 deckd 里进行，不能当成没发生
			return i18n.E("err.deckclient.timeout", c.hc.Timeout, err)
		}
		return i18n.E("err.deckclient.dial", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
			Args  []any  `json:"args"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		_ = dec.Decode(&e)
		if e.Error == "" {
			e.Error = string(data)
		}
		return &Error{Status: resp.StatusCode, Message: e.Error, Code: e.Code, Args: e.Args}
	}
	if out != nil {
		// UseNumber 会把这段 JSON 里所有 interface{} 数字解成 json.Number，不单是错误参数。
		// 有具体类型的字段（int、int64）仍按字段类型解，不受影响。
		// 这样 args 再按 %d 打印时仍是整数，也不会丢过 2^53 的精度。
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		return dec.Decode(out)
	}
	return nil
}

// Get 是 GET 的简写。
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}
