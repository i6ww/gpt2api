package gopay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kleinai/backend/pkg/geelark"
)

// OTPStage 标识 OTP 触发的业务阶段（linking 还是 charge）。
type OTPStage string

const (
	OTPStageLinking OTPStage = "linking"
	OTPStageCharge  OTPStage = "charge"
)

// OTPRequest OTPProvider.Wait 的输入参数。
type OTPRequest struct {
	// ReferenceID GoPay 当前流程的 reference_id，便于幂等 / 调试。
	ReferenceID string
	// Phone 印尼手机号 E.164 不带 +，例如 "8389026XXXXX"。
	Phone string
	// CountryCode 国家码不带 +，例如 "62"。
	CountryCode string
	// StartedAt OTP 触发请求开始的时间。Provider 应忽略 < StartedAt 的旧通知。
	StartedAt time.Time
	// Stage 当前是 linking 还是 charge 阶段；某些 provider 可能区分。
	Stage OTPStage
}

// OTPProvider Charger 等待 WhatsApp OTP 的抽象。
//
// 实现：
//   - GeeLarkOTPProvider     生产环境：直接调 pkg/geelark 取手机通知
//   - StaticOTPProvider      调试 / 单测：返回预设的 OTP
//   - ChannelOTPProvider     人工模式：dispatcher 把后台收到的 OTP 推到 channel
type OTPProvider interface {
	Wait(ctx context.Context, req OTPRequest) (string, error)
}

// 业务错误：让 dispatcher 拿到能区分的语义。
var (
	// ErrProviderOTPTimeout OTPProvider 因超时未拿到 OTP。
	ErrProviderOTPTimeout = errors.New("otp provider: timeout waiting for OTP")
	// ErrProviderOTPCancelled 上游主动取消（用户在云手机上拒绝、流程中断等）。
	ErrProviderOTPCancelled = errors.New("otp provider: OTP cancelled by user/upstream")
)

// mapOTPProviderError 把 OTPProvider 返回的 error 翻译成 *Err 业务错误码。
// stageLabel 用来填 Step 字段。
func mapOTPProviderError(err error, stageLabel string) error {
	if err == nil {
		return nil
	}
	step := "otp_wait_" + stageLabel
	switch {
	case errors.Is(err, ErrProviderOTPTimeout):
		return wrapErr(step, ErrCodeOTPTimeout, 0, err, "OTP wait timeout")
	case errors.Is(err, ErrProviderOTPCancelled):
		return wrapErr(step, ErrCodeOTPCancelled, 0, err, "OTP cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return wrapErr(step, ErrCodeOTPTimeout, 0, err, "ctx deadline")
	case errors.Is(err, context.Canceled):
		return wrapErr(step, ErrCodeOTPCancelled, 0, err, "ctx canceled")
	default:
		return wrapErr(step, ErrCodeOTPCancelled, 0, err, "OTP provider error")
	}
}

// maskOTP 调试日志打印用，遮掉中间位（123***6）。
func maskOTP(otp string) string {
	if len(otp) <= 2 {
		return "***"
	}
	if len(otp) == 6 {
		return otp[:2] + "***" + otp[5:]
	}
	return otp[:1] + "***" + otp[len(otp)-1:]
}

// ───── GeeLarkOTPProvider ──────────────────────────────────────────

// GeeLarkOTPProvider 生产环境：直接拉云手机的 WhatsApp 通知。
type GeeLarkOTPProvider struct {
	Client  *geelark.Client
	Token   string // GeeLark API token（X-API-Token 头）
	PhoneID string // GeeLark 云手机 ID
	Timeout time.Duration

	// EnsureOnline 是否在 Wait 之前先 ping 云手机（PhoneStart + Ping），避免
	// 云手机已停机导致 dumpsys 全空轮询到超时。生产推荐 true。
	EnsureOnline bool

	// OnLog 透传给 geelark 内部的日志回调，可空。
	OnLog func(format string, args ...any)

	// Snapshot 在 user-consent **之前** 调用 SnapshotExistingOTPs 拿到的旧 OTP 集合。
	// 由 dispatcher 提前快照后塞进来；为空时 charger 内部会再 snapshot 一次（但
	// 那时已经晚了，会读到旧推送）。
	Snapshot map[string]struct{}
}

// Wait 实现 OTPProvider。
func (p *GeeLarkOTPProvider) Wait(ctx context.Context, req OTPRequest) (string, error) {
	if p.Client == nil || p.Token == "" || p.PhoneID == "" {
		return "", fmt.Errorf("geelark provider not configured: client=%v token=%t phone=%t",
			p.Client != nil, p.Token != "", p.PhoneID != "")
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}

	if p.EnsureOnline {
		if err := p.Client.EnsureOnline(ctx, p.Token, p.PhoneID, 30*time.Second); err != nil {
			return "", fmt.Errorf("ensure online: %w", err)
		}
	}

	seen := p.Snapshot
	if seen == nil {
		seen = map[string]struct{}{}
		// 临时快照：现在拍一张避免拿到当前已有的 OTP（保险）。
		if snap, err := p.Client.SnapshotExistingOTPs(ctx, p.Token, p.PhoneID); err == nil {
			seen = snap
		}
	}

	otp, err := p.Client.FetchWhatsAppOTP(ctx, p.Token, p.PhoneID, geelark.OTPOptions{
		Timeout:     timeout,
		IssuedAfter: req.StartedAt,
		OnLog:       p.OnLog,
	}, seen)
	if err != nil {
		// geelark 包对超时返回 "timeout"/"deadline" 类 err；这里不强求精准翻译。
		if errors.Is(err, context.DeadlineExceeded) {
			return "", ErrProviderOTPTimeout
		}
		return "", err
	}
	return otp, nil
}

// ───── StaticOTPProvider (debug) ─────────────────────────────────

// StaticOTPProvider 单测/调试用：直接返回预设 OTP。
type StaticOTPProvider struct {
	OTP string
	Err error
}

func (p *StaticOTPProvider) Wait(ctx context.Context, req OTPRequest) (string, error) {
	if p.Err != nil {
		return "", p.Err
	}
	return p.OTP, nil
}

// ───── ChannelOTPProvider (人工/二次审批模式) ─────────────────────

// ChannelOTPProvider 人工模式：dispatcher 把外部输入的 OTP 推到 channel，
// charger 阻塞读。Timeout 表示总等待上限。
type ChannelOTPProvider struct {
	C       <-chan string
	Timeout time.Duration
}

func (p *ChannelOTPProvider) Wait(ctx context.Context, req OTPRequest) (string, error) {
	to := p.Timeout
	if to <= 0 {
		to = 5 * time.Minute
	}
	select {
	case otp, ok := <-p.C:
		if !ok {
			return "", ErrProviderOTPCancelled
		}
		return otp, nil
	case <-time.After(to):
		return "", ErrProviderOTPTimeout
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
