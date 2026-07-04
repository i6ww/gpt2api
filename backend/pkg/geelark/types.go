// Package geelark 是 GeeLark 云手机 OpenAPI 的纯 Go 客户端。
//
// 文档：https://help.geelark.cn/openapi/
// 主要给 Plus 升级 dispatcher 调用，用于：
//  1. 启动/查询/停止云手机（按使用时长收费，需要主动控制开关）
//  2. /shell/execute 直接在云手机里跑 adb shell 命令
//     主要用法：dumpsys notification 抓 WhatsApp 推送，提取 6 位 OTP
//  3. /phone/screenShot 异步截图 + /phone/screenShot/result 轮询，OCR 备用方案
//
// 注意：每个云手机 (`cloud_phone_pool` 一行) 自带一个 GeeLark Bearer Token，
// 因为不同 GeeLark 子账号之间的 token 不通用。所以 client 构造时不持有 token，
// 每次调用 method 第一个参数都是 token + phone_id。
package geelark

import "time"

// 默认值。
const (
	DefaultBaseURL        = "https://openapi.geelark.cn/open/v1"
	DefaultRequestTimeout = 20 * time.Second
	// PhoneStartWaitTimeout 默认等待云手机开机的超时（GeeLark 实测 30~60s）。
	PhoneStartWaitTimeout = 90 * time.Second
	// PhoneStartPollInterval 等开机时轮询 /phone/status 的间隔。
	PhoneStartPollInterval = 3 * time.Second
	// ScreenShotPollInterval 截图任务轮询间隔（GeeLark 截图是异步任务）。
	ScreenShotPollInterval = 1 * time.Second
	// ScreenShotMaxWait 截图最大等待。
	ScreenShotMaxWait = 15 * time.Second
)

// 云手机状态码（来自 /phone/status data.successDetails[].status）。
const (
	PhoneStatusRunning  = 0 // 在线，可执行 shell
	PhoneStatusStarting = 1 // 开机中
	PhoneStatusStopped  = 2 // 已关机
	PhoneStatusExpired  = 3 // 已过期
)

// PhoneStatusName 状态码 → 可读名。
func PhoneStatusName(code int) string {
	switch code {
	case PhoneStatusRunning:
		return "running"
	case PhoneStatusStarting:
		return "starting"
	case PhoneStatusStopped:
		return "stopped"
	case PhoneStatusExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// 截图任务状态码（/phone/screenShot/result data.status）。
const (
	ScreenShotStatusPending = 0
	ScreenShotStatusRunning = 1
	ScreenShotStatusSuccess = 2
	ScreenShotStatusFailed  = 3
)

// 通用响应外层。所有 GeeLark 接口都包成这个壳：{code, msg, traceId, data}.
type genericResp struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	TraceID string `json:"traceId,omitempty"`
}

// PhoneStatusItem /phone/status 单条 successDetails。
type PhoneStatusItem struct {
	ID         string `json:"id"`
	SerialName string `json:"serialName,omitempty"`
	Status     int    `json:"status"`
}

// PhoneFailItem 通用失败明细。
type PhoneFailItem struct {
	ID  string `json:"id"`
	Msg string `json:"msg"`
}

// PhoneStatusData /phone/status 的 data 部分。
type PhoneStatusData struct {
	SuccessDetails []PhoneStatusItem `json:"successDetails"`
	FailDetails    []PhoneFailItem   `json:"failDetails"`
}

// ShellExecuteData /shell/execute data。
type ShellExecuteData struct {
	Status bool   `json:"status"` // false 表示命令执行失败/被拒（仍 code=0）
	Output string `json:"output"`
}

// ScreenShotData /phone/screenShot data。
type ScreenShotData struct {
	TaskID string `json:"taskId"`
}

// ScreenShotResultData /phone/screenShot/result data。
type ScreenShotResultData struct {
	Status       int    `json:"status"`
	DownloadLink string `json:"downloadLink"`
}

// ADBDataItem /adb/getData data.items 单条。
type ADBDataItem struct {
	ID       string `json:"id"`
	Code     int    `json:"code"`
	Msg      string `json:"msg,omitempty"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Password string `json:"pwd"`
}

// ADBDataResp /adb/getData data。
type ADBDataResp struct {
	Items []ADBDataItem `json:"items"`
	// GeeLark 老接口可能放在 successDetails；做兼容用
	SuccessDetails []ADBDataItem   `json:"successDetails,omitempty"`
	FailDetails    []PhoneFailItem `json:"failDetails,omitempty"`
}
