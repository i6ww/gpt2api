package geelark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
)

// Client GeeLark OpenAPI 客户端。
//
// 设计要点：
//   - 不持有 token，所有 method 第一个参数是 token + phoneID
//   - 支持 BaseURL 覆盖（私有部署 / 反向代理）
//   - 单实例 resty.Client，复用 keep-alive 连接
//   - 不携带任何业务依赖（DB / Redis），dispatcher 负责装配
type Client struct {
	baseURL string
	hc      *resty.Client
}

// Options 构造选项。
type Options struct {
	BaseURL string        // 默认 https://openapi.geelark.cn/open/v1
	Timeout time.Duration // 默认 20s
}

// New 构造客户端。
func New(opt Options) *Client {
	base := strings.TrimRight(opt.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	to := opt.Timeout
	if to <= 0 {
		to = DefaultRequestTimeout
	}
	hc := resty.New().
		SetTimeout(to).
		SetHeader("Content-Type", "application/json").
		SetHeader("User-Agent", "KleinAI-Backend/1.0 (geelark)")
	return &Client{baseURL: base, hc: hc}
}

func (c *Client) headers(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"traceId":       strings.ToUpper(uuid.NewString()),
		"Content-Type":  "application/json",
	}
}

// post 通用 POST：解析 {code, msg, data} 外层后把 data 反序列化到 out。
//
// out 可以是 *struct（业务 data）或 nil（不需要解析 data）。
//
// 失败：
//   - HTTP 非 2xx → 返回 *Error{Phase=http}
//   - JSON 解析失败 → 返回 *Error{Phase=parse}
//   - GeeLark code != 0 → 返回 *Error{Phase=api, Code=..., Msg=...}
func (c *Client) post(ctx context.Context, token, path string, body any, out any) error {
	if token == "" {
		return &Error{Phase: "auth", Msg: "missing GeeLark token"}
	}
	resp, err := c.hc.R().
		SetContext(ctx).
		SetHeaders(c.headers(token)).
		SetBody(body).
		Post(c.baseURL + path)
	if err != nil {
		return &Error{Phase: "http", Msg: err.Error(), Cause: err}
	}
	if resp.IsError() {
		return &Error{
			Phase:  "http",
			Code:   resp.StatusCode(),
			Msg:    fmt.Sprintf("HTTP %d %s", resp.StatusCode(), truncate(resp.String(), 200)),
			TraceID: resp.Header().Get("traceId"),
		}
	}

	raw := resp.Body()
	var head genericResp
	if err := json.Unmarshal(raw, &head); err != nil {
		return &Error{Phase: "parse", Msg: "decode resp head: " + err.Error(), Cause: err}
	}
	if head.Code != 0 {
		return &Error{
			Phase:   "api",
			Code:    head.Code,
			Msg:     head.Msg,
			TraceID: head.TraceID,
		}
	}
	if out == nil {
		return nil
	}

	// 重新解析 data 字段。先包一层 envelope 把 head 跟 data 分开。
	envelope := struct {
		Data json.RawMessage `json:"data"`
	}{}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &Error{Phase: "parse", Msg: "decode resp envelope: " + err.Error(), Cause: err}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return &Error{Phase: "parse", Msg: "decode resp data: " + err.Error(), Cause: err}
	}
	return nil
}

// PhoneStatus 查云手机状态。一次可查多个 ID。
func (c *Client) PhoneStatus(ctx context.Context, token string, ids []string) (*PhoneStatusData, error) {
	if len(ids) == 0 {
		return nil, &Error{Phase: "param", Msg: "ids empty"}
	}
	var out PhoneStatusData
	if err := c.post(ctx, token, "/phone/status", map[string]any{"ids": ids}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PhoneStart 启动云手机。
func (c *Client) PhoneStart(ctx context.Context, token string, ids []string) error {
	if len(ids) == 0 {
		return &Error{Phase: "param", Msg: "ids empty"}
	}
	return c.post(ctx, token, "/phone/start", map[string]any{"ids": ids}, nil)
}

// PhoneStop 停止云手机（按使用时长收费的服务，任务结束建议主动停机）。
func (c *Client) PhoneStop(ctx context.Context, token string, ids []string) error {
	if len(ids) == 0 {
		return &Error{Phase: "param", Msg: "ids empty"}
	}
	return c.post(ctx, token, "/phone/stop", map[string]any{"ids": ids}, nil)
}

// ShellExecute 在指定云手机里执行 adb shell 命令，返回 stdout。
//
// 主要用法：
//   dumpsys notification --noredact 2>/dev/null | grep -A 80 com.whatsapp
//
// 注意：单条命令不要超过 ~4KB；Geelark 服务器超时 ~15s。
func (c *Client) ShellExecute(ctx context.Context, token, phoneID, cmd string) (*ShellExecuteData, error) {
	if phoneID == "" || cmd == "" {
		return nil, &Error{Phase: "param", Msg: "phoneID or cmd empty"}
	}
	var out ShellExecuteData
	if err := c.post(ctx, token, "/shell/execute", map[string]any{"id": phoneID, "cmd": cmd}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ScreenShot 触发截图任务，返回 taskID。
func (c *Client) ScreenShot(ctx context.Context, token, phoneID string) (string, error) {
	var out ScreenShotData
	if err := c.post(ctx, token, "/phone/screenShot", map[string]any{"id": phoneID}, &out); err != nil {
		return "", err
	}
	if out.TaskID == "" {
		return "", &Error{Phase: "api", Msg: "no taskId returned"}
	}
	return out.TaskID, nil
}

// ScreenShotResult 轮询截图任务结果。
func (c *Client) ScreenShotResult(ctx context.Context, token, taskID string) (*ScreenShotResultData, error) {
	var out ScreenShotResultData
	if err := c.post(ctx, token, "/phone/screenShot/result", map[string]any{"taskId": taskID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ScreenShotWait 一站式截图：触发 + 轮询直到成功，返回下载链接。
func (c *Client) ScreenShotWait(ctx context.Context, token, phoneID string) (string, error) {
	taskID, err := c.ScreenShot(ctx, token, phoneID)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(ScreenShotMaxWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(ScreenShotPollInterval):
		}
		res, err := c.ScreenShotResult(ctx, token, taskID)
		if err != nil {
			return "", err
		}
		switch res.Status {
		case ScreenShotStatusSuccess:
			if res.DownloadLink == "" {
				return "", &Error{Phase: "api", Msg: "screenshot ok but no downloadLink"}
			}
			return res.DownloadLink, nil
		case ScreenShotStatusFailed:
			return "", &Error{Phase: "api", Msg: "screenshot task failed"}
		}
	}
	return "", &Error{Phase: "timeout", Msg: "screenshot timeout"}
}

// ADBSetStatus 开/关 ADB 隧道。
func (c *Client) ADBSetStatus(ctx context.Context, token string, ids []string, open bool) error {
	if len(ids) == 0 {
		return &Error{Phase: "param", Msg: "ids empty"}
	}
	return c.post(ctx, token, "/adb/setStatus", map[string]any{"ids": ids, "open": open}, nil)
}

// ADBGetData 拿 ADB 隧道连接信息（IP:Port:Password）。
func (c *Client) ADBGetData(ctx context.Context, token string, ids []string) (*ADBDataResp, error) {
	if len(ids) == 0 {
		return nil, &Error{Phase: "param", Msg: "ids empty"}
	}
	var out ADBDataResp
	if err := c.post(ctx, token, "/adb/getData", map[string]any{"ids": ids}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EnsureOnline 确保云手机在线：
//  1. /phone/status 看当前状态
//  2. running → 直接返回
//  3. stopped/starting → /phone/start，轮询直到 running 或超时
//  4. expired/不存在 → 返回错误（云手机已过期/被删）
func (c *Client) EnsureOnline(ctx context.Context, token, phoneID string, waitTimeout time.Duration) error {
	if waitTimeout <= 0 {
		waitTimeout = PhoneStartWaitTimeout
	}

	check := func() (int, error) {
		st, err := c.PhoneStatus(ctx, token, []string{phoneID})
		if err != nil {
			return -1, err
		}
		for _, item := range st.SuccessDetails {
			if item.ID == phoneID {
				return item.Status, nil
			}
		}
		for _, fail := range st.FailDetails {
			if fail.ID == phoneID {
				return -1, &Error{Phase: "api", Msg: "phone status fail: " + fail.Msg}
			}
		}
		return -1, &Error{Phase: "api", Msg: "phone status: id not found in response"}
	}

	status, err := check()
	if err != nil {
		return err
	}
	if status == PhoneStatusRunning {
		return nil
	}
	if status == PhoneStatusExpired {
		return &Error{Phase: "api", Msg: "phone expired"}
	}

	// stopped or starting: 触发启动 + 轮询
	if status == PhoneStatusStopped {
		if err := c.PhoneStart(ctx, token, []string{phoneID}); err != nil {
			return err
		}
	}

	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(PhoneStartPollInterval):
		}
		st, err := check()
		if err != nil {
			return err
		}
		if st == PhoneStatusRunning {
			return nil
		}
		if st == PhoneStatusExpired {
			return &Error{Phase: "api", Msg: "phone expired during wait"}
		}
	}
	return &Error{Phase: "timeout", Msg: "wait phone online timeout"}
}

// Ping 在云手机上跑 `echo ok` 验证 shell 通路（连通性检测）。
func (c *Client) Ping(ctx context.Context, token, phoneID string) error {
	out, err := c.ShellExecute(ctx, token, phoneID, "echo ok")
	if err != nil {
		return err
	}
	if !out.Status {
		return &Error{Phase: "api", Msg: "shell exec status=false"}
	}
	if !strings.Contains(strings.TrimSpace(out.Output), "ok") {
		return &Error{Phase: "api", Msg: "shell echo unexpected: " + truncate(out.Output, 80)}
	}
	return nil
}

// Error 客户端错误。
type Error struct {
	Phase   string // "param" | "auth" | "http" | "parse" | "api" | "timeout"
	Code    int    // GeeLark code 或 HTTP status
	Msg     string
	TraceID string
	Cause   error
}

// Error 实现 error。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{"[geelark/" + e.Phase + "]"}
	if e.Code != 0 {
		parts = append(parts, fmt.Sprintf("code=%d", e.Code))
	}
	if e.TraceID != "" {
		parts = append(parts, "traceId="+e.TraceID)
	}
	parts = append(parts, e.Msg)
	return strings.Join(parts, " ")
}

// Unwrap 暴露底层。
func (e *Error) Unwrap() error { return e.Cause }

// IsTimeout 用于 dispatcher 区分 timeout vs hard fail。
func IsTimeout(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Phase == "timeout"
	}
	return false
}

// IsAPIError 是否 GeeLark 业务错误（非网络/解析错误）。
func IsAPIError(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Phase == "api"
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
