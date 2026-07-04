package gopay

import (
	"errors"
	"fmt"
	"strings"
)

// Err 业务错误基类。所有非网络/系统错误都包成 *Err，dispatcher 拿 errors.As 分类。
type Err struct {
	// Code 业务码，便于 dispatcher 决定是否重试 / 上报 / 扣资源。
	Code ErrorCode
	// Step 出错的流程步骤名（"chatgpt_create_checkout" / "midtrans_init_linking" 等）。
	Step string
	// HTTP 触发该错误的 HTTP 状态码（0 表示非 HTTP 错误）。
	HTTP int
	// Cause 底层 error。
	Cause error
	// Detail 给人读的简述。
	Detail string
}

// Error 实现 error。
func (e *Err) Error() string {
	prefix := fmt.Sprintf("[gopay/%s]", e.Step)
	if e.HTTP > 0 {
		prefix += fmt.Sprintf(" http=%d", e.HTTP)
	}
	if e.Code != "" {
		prefix += fmt.Sprintf(" code=%s", e.Code)
	}
	msg := e.Detail
	if e.Cause != nil {
		if msg != "" {
			msg = msg + ": " + e.Cause.Error()
		} else {
			msg = e.Cause.Error()
		}
	}
	if msg == "" {
		msg = "unknown error"
	}
	return prefix + " " + msg
}

// Unwrap 配合 errors.Is/As。
func (e *Err) Unwrap() error { return e.Cause }

// Is 允许 errors.Is(e, ErrCodeOTPCancelled) 之类语义。
func (e *Err) Is(target error) bool {
	t, ok := target.(*Err)
	if !ok {
		return false
	}
	return e.Code == t.Code && (t.Step == "" || t.Step == e.Step)
}

// ErrorCode 业务错误码。dispatcher 据此分类：
//
//   - PINRejected / OTPCancelled / WalletExhausted ← 标钱包 banned
//   - RateLimited / ProxyBanned                    ← 标代理 banned，换一个
//   - VerifyTimeout                                 ← 已扣款但 verify 超时；兜底 polling
//   - Unrecoverable                                 ← 致命错，整个 task 失败
type ErrorCode string

const (
	ErrCodeUnknown        ErrorCode = ""
	ErrCodeNetwork        ErrorCode = "network"
	ErrCodeRateLimited    ErrorCode = "rate_limited"
	ErrCodeProxyBanned    ErrorCode = "proxy_banned"
	ErrCodePINRejected    ErrorCode = "pin_rejected"
	ErrCodeOTPCancelled   ErrorCode = "otp_cancelled"
	ErrCodeOTPTimeout     ErrorCode = "otp_timeout"
	ErrCodeMidtransLink   ErrorCode = "midtrans_link"
	// ErrCodeChargeRejected：Midtrans /charge 返回 HTTP 200 但 body
	// status_code != 200/201（典型 status_message="Transaksi Anda ditolak"）。
	// 表示该钱包+IP 组合被 Midtrans/GoPay 反欺诈或余额校验拒绝；继续重试无效，
	// 应让 dispatcher 把钱包做长冷却（同一钱包近期内大概率仍被拒）。
	ErrCodeChargeRejected ErrorCode = "charge_rejected"
	ErrCodeStripeConfirm  ErrorCode = "stripe_confirm"
	ErrCodeChatGPTApprove ErrorCode = "chatgpt_approve"
	ErrCodeVerifyTimeout  ErrorCode = "verify_timeout"
	ErrCodeUnrecoverable  ErrorCode = "unrecoverable"
)

// 一些哨兵实例供 errors.Is/As 比对用（不带 Step）。
var (
	ErrPINRejected   = &Err{Code: ErrCodePINRejected, Detail: "PIN rejected"}
	ErrRateLimited   = &Err{Code: ErrCodeRateLimited, Detail: "rate limited"}
	ErrOTPCancelled  = &Err{Code: ErrCodeOTPCancelled, Detail: "OTP cancelled"}
	ErrOTPTimeout    = &Err{Code: ErrCodeOTPTimeout, Detail: "OTP timeout"}
	ErrVerifyTimeout = &Err{Code: ErrCodeVerifyTimeout, Detail: "verify timeout"}
)

// newErr 帮助构造，避免每次都写 &Err{...}。
func newErr(step string, code ErrorCode, http int, format string, args ...any) *Err {
	return &Err{Step: step, Code: code, HTTP: http, Detail: fmt.Sprintf(format, args...)}
}

// wrapErr 同上但带底层 cause。
func wrapErr(step string, code ErrorCode, http int, cause error, format string, args ...any) *Err {
	return &Err{Step: step, Code: code, HTTP: http, Detail: fmt.Sprintf(format, args...), Cause: cause}
}

// IsCode 判断 err 是否为指定 code（即使被 fmt.Errorf wrap 过）。
func IsCode(err error, code ErrorCode) bool {
	var e *Err
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == code
}

// isProbableRateLimitNetwork 判断网络层错误是否更可能是"限流的早期表现"
// 而非随机抖动。Midtrans / Stripe / CF 在限频时常常先 RST 掉 TLS 握手，
// 表现为 "tls handshake ... EOF" / "connection reset" / "broken pipe"，
// 用同一 IP 立刻重试 99% 还是 fail —— 这类错误更该走 backoff 重试 + swap proxy，
// 而不是当成纯网络抖动直接 abort。
func isProbableRateLimitNetwork(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{
		"tls handshake",
		"eof",
		"connection reset",
		"broken pipe",
		"connection refused",
		"i/o timeout",
		"unexpected eof",
		"client connection",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
