// Package upgradeplus 实现 GPT 账号通过 GoPay 自动开通 ChatGPT Plus 的 dispatcher。
//
// 跟 adobe / grok / gpt 三家"注册 dispatcher"不一样，本 dispatcher 不创建新账号，
// 它接受一个**已存在**的 pool_gpt.id 作为输入，跑完 GoPay 15 步流程后给该账号
// 标 plan_type=plus，并写一行 gopay_wallet_binding 用于追踪取消订阅。
//
// 资源调度顺序：
//
//  1. 锁号：从 pool_gpt 取出账号 + 解密 AT/RT
//     （可选）force_refresh_token=true 时先用 RT 换一份新 AT
//  2. 抢钱包：FOR UPDATE SKIP LOCKED 锁一行 gopay_wallet_pool
//  3. 抢云手机：按 wallet.cloud_phone_id 取（或 payload.phone_id 覆盖）
//  4. 取支付代理：从 payment_proxy_pool 随机取一个 active 的（按 country=ID）
//  5. 取 GeeLark token：解密 cloud_phone.gl_token_enc
//  6. 在 user-consent 之前先 SnapshotExistingOTPs（避免拿到旧 OTP）
//  7. 调 gopay.New + Run
//  8. 处理结果：
//     - 成功      → CreateBindingAndMarkSuccess + MarkPlusUpgraded(account)
//     - PINRejected → MarkBanned wallet
//     - RateLimited → MarkFailed proxy（钱包/手机不消耗）
//     - OTPCancelled / OTPTimeout → MarkFailed wallet（cooldown）
//     - VerifyTimeout → 视作成功（已扣款），写 binding + MarkPlusUpgraded
//     - 其他       → MarkFailed wallet（短 cooldown）
//
// 失败时所有非"已消耗"资源都会自动 Release，下个任务能重新抢。
package upgradeplus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/dispatcher"
	"github.com/kleinai/backend/internal/regkit/gopay"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/geelark"
	"github.com/kleinai/backend/pkg/gptauth"
)

// Provider register_task.provider 的取值。
const Provider = "upgrade_plus"

// Dispatcher Plus 升级 dispatcher。
type Dispatcher struct {
	dispatcher.Deps

	PoolGpt      *service.PoolGptService
	Wallet       *service.GopayWalletService
	Phone        *service.CloudPhoneService
	PaymentProxy *service.PaymentProxyService
}

// Payload register_task.payload 的反序列化结构。
//
// 字段说明：
//   - pool_gpt_id    必填，目标账号 ID
//   - wallet_id      可选，0 或缺省 = 自动从池抢
//   - phone_id       可选，"" = 按 wallet.cloud_phone_id 决定
//   - ext_proxy_id   可选，0 = PickRandom 印尼支付池
//   - cs_proxy_url   可选，"" = 沿用 ext proxy（pool_gpt 表没存账号注册代理）
//   - force_refresh  可选，true 时跑流程前先 RT → AT 换一份新 AT
type Payload struct {
	PoolGptID    uint64 `json:"pool_gpt_id"`
	WalletID     uint64 `json:"wallet_id"`
	PhoneID      string `json:"phone_id"`
	ExtProxyID   uint64 `json:"ext_proxy_id"`
	CSProxyURL   string `json:"cs_proxy_url"`
	ForceRefresh bool   `json:"force_refresh"`
}

// runState 跑过程中累积的资源 + 状态，供 cleanup 用。
type runState struct {
	account     *model.PoolGpt
	accessToken string
	wallet      *model.GopayWalletPool
	phone       *model.CloudPhonePool

	extProxy    *model.PaymentProxyPool // Phase B（GoPay/Midtrans）印尼代理
	extProxyURL string
	csProxy     *model.PaymentProxyPool // Phase A（ChatGPT/Stripe）账号国 / 日本代理
	csProxyURL  string

	walletConsumed   bool // success 后置 true，cleanup 不再 Release
	extProxyConsumed bool // success / failed 都 MarkUsed/MarkFailed，cleanup 不动
	csProxyConsumed  bool
}

// Run 实现 service.RegisterDispatcher。
func (d *Dispatcher) Run(ctx context.Context, svc *service.RegisterTaskService, task *model.RegisterTask) error {
	if d.PoolGpt == nil || d.Wallet == nil || d.Phone == nil || d.PaymentProxy == nil {
		return errors.New("upgrade_plus dispatcher 依赖未注入完整（内部错误）")
	}
	cfg := d.SysCfg
	if cfg == nil {
		return errors.New("SystemConfigService 未注入（内部错误）")
	}
	if !cfg.PlusUpgradeEnabled(ctx) {
		return errors.New("Plus 升级功能在系统设置里未开启")
	}

	_ = svc.UpdateProgress(ctx, task.ID, "preflight", 5)

	payload, err := parsePayload(task.Payload)
	if err != nil {
		return err
	}
	if payload.PoolGptID == 0 {
		return errors.New("payload.pool_gpt_id 必填")
	}

	st := &runState{}
	defer d.cleanup(context.Background(), svc, task, st)

	// === 1) 锁号 + 解 AT ===
	_ = svc.UpdateProgress(ctx, task.ID, "load_account", 10)
	row, accessToken, refreshToken, err := d.PoolGpt.LoadCredentials(ctx, payload.PoolGptID)
	if err != nil {
		return fmt.Errorf("加载账号失败: %w", err)
	}
	st.account = row
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("已加载账号 #%d %s", row.ID, maskEmail(row.Email)))

	if payload.ForceRefresh && refreshToken != "" {
		_ = svc.UpdateProgress(ctx, task.ID, "refresh_token", 15)
		clientID := gptauth.PlatformClientID
		if row.OAuthClientID != nil && strings.TrimSpace(*row.OAuthClientID) != "" {
			clientID = strings.TrimSpace(*row.OAuthClientID)
		}
		ts, refErr := gptauth.RefreshAccessToken(ctx, refreshToken, clientID, "", 30*time.Second)
		if refErr != nil {
			svc.LogWarn(ctx, task.ID, fmt.Sprintf("force_refresh 失败：%v（继续用旧 AT 试）", refErr))
		} else {
			accessToken = ts.AccessToken
			svc.LogInfo(ctx, task.ID, "已用 RT 换得新 AT")
		}
	}
	st.accessToken = accessToken

	// === 2) 抢钱包 ===
	_ = svc.UpdateProgress(ctx, task.ID, "lease_wallet", 20)
	perWalletQuota := cfg.PlusUpgradePerWalletQuota(ctx)
	if payload.WalletID > 0 {
		w, gErr := d.Wallet.GetByID(ctx, payload.WalletID)
		if gErr != nil {
			return fmt.Errorf("指定钱包 #%d 取失败: %w", payload.WalletID, gErr)
		}
		if w == nil {
			return fmt.Errorf("指定钱包 #%d 不存在", payload.WalletID)
		}
		if w.Status != model.GopayWalletStatusAvailable {
			return fmt.Errorf("指定钱包 #%d 当前状态为 %q，不可用", w.ID, w.Status)
		}
		st.wallet = w
	} else {
		w, lErr := d.Wallet.LeaseAvailable(ctx, perWalletQuota)
		if lErr != nil {
			return fmt.Errorf("抢钱包失败: %w", lErr)
		}
		if w == nil {
			return errors.New("无可用钱包（钱包池为空 / 全部 exhausted-cooldown）")
		}
		st.wallet = w
	}
	pin, perr := d.Wallet.ResolvePIN(st.wallet)
	if perr != nil || pin == "" {
		return fmt.Errorf("解钱包 PIN 失败: %v", perr)
	}
	// 注意：钱包不再存手机号，号码 / 国家码全部从下面 cloud_phone 拿（一对一关系）。
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("已抢到钱包 #%d", st.wallet.ID))

	// === 3) 抢云手机 ===
	_ = svc.UpdateProgress(ctx, task.ID, "lease_phone", 25)
	phoneID := strings.TrimSpace(payload.PhoneID)
	if phoneID == "" {
		phoneID = strings.TrimSpace(st.wallet.CloudPhoneID)
	}
	if phoneID == "" {
		return errors.New("钱包未绑定云手机，且 payload 也没指定 phone_id")
	}
	phone, gErr := d.Phone.GetByID(ctx, phoneID)
	if gErr != nil {
		return fmt.Errorf("取云手机 %s 失败: %w", phoneID, gErr)
	}
	if phone == nil {
		return fmt.Errorf("云手机 %s 不存在", phoneID)
	}
	if phone.Status != model.CloudPhoneStatusOnline {
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("云手机 %s 当前状态 %s，仍尝试启动", phoneID, phone.Status))
	}
	if strings.TrimSpace(phone.PhoneNumber) == "" {
		return fmt.Errorf("云手机 %s 未填手机号（请去 Plus 升级资源池/云手机面板补 phone_number）", phone.ID)
	}
	st.phone = phone
	glToken, terr := d.Phone.ResolveToken(phone)
	if terr != nil || glToken == "" {
		return fmt.Errorf("解 GeeLark token 失败: %v", terr)
	}
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("已挂上云手机 %s (+%s%s)", phone.ID, phone.CountryCode, maskPhone(phone.PhoneNumber)))

	// === 4) 取支付代理（Phase B，印尼 / GoPay）===
	_ = svc.UpdateProgress(ctx, task.ID, "lease_ext_proxy", 28)
	extCountry := cfg.PlusUpgradeExtProxyCountry(ctx) // 默认 ID
	extProxy, perr2 := d.pickProxy(ctx, payload.ExtProxyID, extCountry)
	if perr2 != nil {
		return fmt.Errorf("取 Phase B 代理失败 (country=%s): %w", extCountry, perr2)
	}
	if extProxy == nil {
		return fmt.Errorf("无可用 Phase B 代理（country=%s，请去 Plus 升级资源池 → 支付代理池 添加）", extCountry)
	}
	st.extProxy = extProxy
	extProxyURL, urlErr := d.buildProxyURL(ctx, extProxy)
	if urlErr != nil {
		return fmt.Errorf("构建 Phase B 代理 URL 失败: %w", urlErr)
	}
	st.extProxyURL = extProxyURL
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("已挂上支付代理 #%d（%s, %s）", extProxy.ID, extCountry, dispatcher.MaskProxy(extProxyURL)))

	// === 4b) 取 CS 代理（Phase A，账号注册国 / ChatGPT-Stripe）===
	// payload 显式指定 cs_proxy_url 时直接用；否则按 system_config.plus_upgrade.cs_proxy_country 抢。
	// 空字符串配置表示禁用 CS 专属代理 → 回退到 ext proxy（不推荐，触发 OpenAI 风控）。
	csProxyURL := strings.TrimSpace(payload.CSProxyURL)
	if csProxyURL != "" {
		svc.LogInfo(ctx, task.ID, fmt.Sprintf("账号代理使用指定通道 %s", dispatcher.MaskProxy(csProxyURL)))
	} else {
		csCountry := cfg.PlusUpgradeCSProxyCountry(ctx) // 默认 JP
		if csCountry == "" || csCountry == extCountry {
			svc.LogWarn(ctx, task.ID, "未配置账号代理国家（或与支付代理同一国家），将复用支付代理；可能触发 OpenAI 异地风控")
			csProxyURL = extProxyURL
		} else {
			_ = svc.UpdateProgress(ctx, task.ID, "lease_cs_proxy", 30)
			csProxy, csErr := d.pickProxy(ctx, 0, csCountry)
			if csErr != nil || csProxy == nil {
				return fmt.Errorf("无可用 Phase A 代理（country=%s，请去 Plus 升级资源池 → 支付代理池 添加一条 %s 代理）: %v", csCountry, csCountry, csErr)
			}
			st.csProxy = csProxy
			csURL, csUErr := d.buildProxyURL(ctx, csProxy)
			if csUErr != nil {
				return fmt.Errorf("构建 Phase A 代理 URL 失败: %w", csUErr)
			}
			st.csProxyURL = csURL
			csProxyURL = csURL
			svc.LogInfo(ctx, task.ID, fmt.Sprintf("已挂上账号代理 #%d（%s, %s）", csProxy.ID, csCountry, dispatcher.MaskProxy(csURL)))
		}
	}

	// === 4c) Preflight：用 csProxy 探测账号 country / email / id ===
	// approve "blocked" 的最常见原因是账号 billing_country != ID（GoPay 仅
	// 支持印尼账号）。这里在 lease 资源后、跑 15 步前先调一次 /backend-api/me，
	// 把账号 region 暴露到任务日志；country != ID 时只 warn，让操作人看清楚。
	_ = svc.UpdateProgress(ctx, task.ID, "probe_account", 32)
	probeDeviceID := stableDeviceID(row.ID)
	probeInfo, probeErr := gopay.ProbeMe(ctx, gopay.ProbeConfig{
		ProxyURL:    csProxyURL,
		AccessToken: accessToken,
		Cookies:     buildCookies(accessToken, probeDeviceID),
		DeviceID:    probeDeviceID,
		Timeout:     30 * time.Second,
	})
	if probeErr != nil {
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("探测账号信息失败（继续跑流程）：%v", probeErr))
	} else {
		// probeInfo 仅做诊断输出，不暴露 email / id 等敏感字段；JP/US 账号也能
		// 跑通 GoPay，所以这里仅 warn 不 fail-fast。
		svc.LogInfo(ctx, task.ID, fmt.Sprintf("账号信息已识别（国家=%s）", probeInfo.Country))
		if probeInfo.Country == "" {
			svc.LogWarn(ctx, task.ID, "账号国家字段为空（可能是新号未激活）")
		}
	}

	// === 5) GeeLark：先 EnsureOnline + Snapshot 旧 OTP ===
	_ = svc.UpdateProgress(ctx, task.ID, "geelark_warmup", 35)
	glClient := geelark.New(geelark.Options{BaseURL: cfg.GeeLarkAPIBase(ctx)})
	if err := glClient.EnsureOnline(ctx, glToken, phone.ID, 30*time.Second); err != nil {
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("云手机预热失败（继续运行）：%v", err))
	}
	otpSnapshot, snapErr := glClient.SnapshotExistingOTPs(ctx, glToken, phone.ID)
	if snapErr != nil {
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("快照旧 OTP 失败（继续运行）：%v", snapErr))
		otpSnapshot = map[string]struct{}{}
	} else {
		svc.LogInfo(ctx, task.ID, fmt.Sprintf("快照旧 OTP %d 条（避免命中残留通知）", len(otpSnapshot)))
	}

	// === 6) 实例化 OTPProvider + Charger ===
	otpProvider := &gopay.GeeLarkOTPProvider{
		Client:       glClient,
		Token:        glToken,
		PhoneID:      phone.ID,
		Timeout:      cfg.PlusUpgradeOTPTimeout(ctx),
		EnsureOnline: false, // 上面已经 EnsureOnline 过
		Snapshot:     otpSnapshot,
		OnLog: func(format string, args ...any) {
			svc.LogInfo(ctx, task.ID, fmt.Sprintf("[Plus 升级/OTP] "+format, args...))
		},
	}

	// 给 charger.Run 用的 logFn：把每条日志直接转为 register_task_log。
	logFn := func(level, msg string) {
		switch level {
		case "warn":
			svc.LogWarn(ctx, task.ID, msg)
		case "error":
			svc.LogError(ctx, task.ID, msg)
		default:
			svc.LogInfo(ctx, task.ID, msg)
		}
	}

	// device_id：pool_gpt 没存，每次跑用一个稳定的（按账号 ID 派生）保持 sentinel 一致。
	deviceID := stableDeviceID(row.ID)

	cookies := buildCookies(accessToken, deviceID)

	chargerCfg := gopay.Config{
		CSProxy:  csProxyURL,
		ExtProxy: extProxyURL,
		Auth: gopay.Auth{
			AccessToken: accessToken,
			Cookies:     cookies,
			DeviceID:    deviceID,
			// UserAgent: 留空走 browser.Client 的随机 Chrome UA
		},
		Wallet: gopay.Wallet{
			// 手机号一律走 cloud_phone（一台云手机 = 一个 SIM = 一个 GoPay 钱包）。
			CountryCode: st.phone.CountryCode,
			PhoneNumber: st.phone.PhoneNumber,
			PIN:         pin,
		},
		OTPProvider:        otpProvider,
		Log:                logFn,
		RateLimitStrategy: "retry",
		// Midtrans 对同一出口 IP 的 linking 限频窗口实测 ~5-10 分钟，30s 等待经常
		// 还在限流期内立刻又被打回。提到 60s 配合 5 次重试（共 5 分钟）能跨过大
		// 部分窗口，又不会让单任务挂太久。可在 system_config 里再加 key 调。
		RateLimitRetrySeconds: 60,
		RefreshExtProxy: func() (string, error) {
			// 当前 Phase B 代理 banned，标 failed 并按同 country 重抢一个（**排除刚 banned 的那个**，
			// 否则池里只有 1 个 ID 代理时会死循环回锅）。
			var exclude []uint64
			if st.extProxy != nil {
				_ = d.PaymentProxy.MarkFailed(context.Background(), st.extProxy.ID, "rate_limited from gopay")
				st.extProxyConsumed = true
				exclude = append(exclude, st.extProxy.ID)
			}
			fresh, ferr := d.PaymentProxy.PickRandomExcluding(context.Background(), extCountry, exclude)
			if ferr != nil {
				return "", fmt.Errorf("查替补 Phase B 代理失败 (country=%s): %v", extCountry, ferr)
			}
			if fresh == nil {
				return "", fmt.Errorf("无可用替补 Phase B 代理（country=%s，池里所有 %s 代理都已 banned，请在 Plus 升级资源池 → 支付代理池 加一条 country=%s 的代理）", extCountry, extCountry, extCountry)
			}
			st.extProxy = fresh
			st.extProxyConsumed = false
			newURL, uerr := d.buildProxyURL(context.Background(), fresh)
			if uerr != nil {
				return "", uerr
			}
			st.extProxyURL = newURL
			svc.LogInfo(context.Background(), task.ID,
				fmt.Sprintf("已切换支付代理到 #%d（%s, %s）", fresh.ID, extCountry, dispatcher.MaskProxy(newURL)))
			return newURL, nil
		},
	}

	charger, cerr := gopay.New(ctx, chargerCfg)
	if cerr != nil {
		return fmt.Errorf("初始化 charger 失败: %w", cerr)
	}

	// === 7) 跑 15 步 ===
	_ = svc.UpdateProgress(ctx, task.ID, "gopay_run", 40)
	result, runErr := charger.Run(ctx)

	if runErr != nil {
		return d.handleRunError(ctx, svc, task, st, runErr)
	}

	// === 8) 成功收尾 ===
	return d.handleRunSuccess(ctx, svc, task, st, result, perWalletQuota)
}

// handleRunSuccess 统一处理：写 binding + 标 plan_type + 释放代理 + cleanup 标 wallet 已消耗。
func (d *Dispatcher) handleRunSuccess(
	ctx context.Context,
	svc *service.RegisterTaskService,
	task *model.RegisterTask,
	st *runState,
	result *gopay.Result,
	perWalletQuota int,
) error {
	_ = svc.UpdateProgress(ctx, task.ID, "binding_write", 92)

	expiresAt := result.ChargedAt.Add(30 * 24 * time.Hour)
	if result.ChargedAt.IsZero() {
		expiresAt = time.Now().Add(30 * 24 * time.Hour)
	}
	binding := &model.GopayWalletBinding{
		WalletID:     st.wallet.ID,
		GptAccountID: st.account.ID,
		AmountIDR:    result.AmountIDR,
		ChargedAt:    nonZeroTime(result.ChargedAt),
		ExpiresAt:    expiresAt,
		Status:       model.GopayBindingStatusActive,
	}
	if result.CSID != "" {
		v := result.CSID
		binding.CSID = &v
	}
	if result.ChargeRef != "" {
		v := result.ChargeRef
		binding.ChargeRef = &v
	}

	if err := d.Wallet.CreateBindingAndMarkSuccess(ctx, binding, perWalletQuota); err != nil {
		// 极少见：扣款已成功但写库失败。报警让人工兜底。
		svc.LogError(ctx, task.ID, fmt.Sprintf("⚠️ 扣款成功但写绑定记录失败（请人工核对）：%v", err))
		return fmt.Errorf("write binding: %w", err)
	}
	st.walletConsumed = true

	if err := d.PoolGpt.MarkPlusUpgraded(ctx, st.account.ID, fmt.Sprintf("plus charged_at=%s ref=%s", result.ChargedAt.Format(time.RFC3339), result.ChargeRef)); err != nil {
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("更新账号订阅类型失败（不影响绑定）：%v", err))
	}

	// Phase B 代理 MarkUsed（成功）；Phase A 代理只是被请求过，也顺便 MarkUsed 算一次。
	if st.extProxy != nil && !st.extProxyConsumed {
		_ = d.PaymentProxy.MarkUsed(ctx, st.extProxy.ID)
		st.extProxyConsumed = true
	}
	if st.csProxy != nil && !st.csProxyConsumed {
		_ = d.PaymentProxy.MarkUsed(ctx, st.csProxy.ID)
		st.csProxyConsumed = true
	}

	gopayUnlinkStatus := "skipped_config"
	autoGopayUnlink := true
	if d.SysCfg != nil {
		autoGopayUnlink = d.SysCfg.GetBool(ctx, service.SettingPlusUpgradeAutoGopayUnlink, true)
	}
	if autoGopayUnlink {
		if d.Phone == nil {
			gopayUnlinkStatus = "skipped_no_service"
		} else {
			_ = svc.UpdateProgress(ctx, task.ID, "gopay_unlink_openai", 94)
			unlinkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			base := ""
			if d.SysCfg != nil {
				base = d.SysCfg.GeeLarkAPIBase(ctx)
			}
			uerr := d.Phone.GopayUnlinkOpenAI(unlinkCtx, st.phone.ID, base, "")
			cancel()
			if uerr != nil {
				gopayUnlinkStatus = "failed"
				svc.LogWarn(ctx, task.ID, fmt.Sprintf("Plus 已开通，但云手机内自动移除 GoPay 已连接 OpenAI 未完全成功（可在后台云手机面板手动「解绑 OpenAI」）：%v", uerr))
			} else {
				gopayUnlinkStatus = "success"
				svc.LogInfo(ctx, task.ID, "已在云手机 GoPay 内自动移除 OpenAI（已连接应用）")
			}
		}
	}

	resultMap := map[string]any{
		"state":         result.State,
		"cs_id":         result.CSID,
		"charge_ref":    result.ChargeRef,
		"amount_idr":    result.AmountIDR,
		"verify_ok":     result.VerifyOK,
		"wallet_id":     st.wallet.ID,
		"phone_id":      st.phone.ID,
		"ext_proxy_id":  func() uint64 { if st.extProxy != nil { return st.extProxy.ID }; return 0 }(),
		"cs_proxy_id":   func() uint64 { if st.csProxy != nil { return st.csProxy.ID }; return 0 }(),
		"gopay_unlink_openai": gopayUnlinkStatus,
	}
	if !result.ChargedAt.IsZero() {
		resultMap["charged_at"] = result.ChargedAt.UTC().Format(time.RFC3339)
	}
	if !expiresAt.IsZero() {
		resultMap["expires_at"] = expiresAt.UTC().Format(time.RFC3339)
	}

	if err := svc.FinishSuccess(ctx, task.ID, st.account.ID, resultMap); err != nil {
		return err
	}
	if !result.VerifyOK {
		svc.LogWarn(ctx, task.ID, "GoPay 已扣款但 OpenAI 订阅状态确认超时；账号大概率已升级，可稍后手工探测刷新")
	}
	return nil
}

// handleRunError 按 GoPay error code 分流资源标记。
func (d *Dispatcher) handleRunError(
	ctx context.Context,
	svc *service.RegisterTaskService,
	task *model.RegisterTask,
	st *runState,
	runErr error,
) error {
	cooldownMin := 60
	if d.SysCfg != nil {
		cooldownMin = d.SysCfg.PlusUpgradeWalletCooldownMin(ctx)
	}

	switch {
	case gopay.IsCode(runErr, gopay.ErrCodePINRejected):
		// 钱包 PIN 错（甚至可能 PIN 被锁），永久 banned。
		_ = d.Wallet.MarkBanned(ctx, st.wallet.ID, "PIN rejected by GoPay")
		st.walletConsumed = true // ban 后 cleanup 不再 Release
		svc.LogError(ctx, task.ID, fmt.Sprintf("钱包 #%d 已封禁（PIN 错误）", st.wallet.ID))

	case gopay.IsCode(runErr, gopay.ErrCodeRateLimited):
		// Phase B 代理 banned，钱包/手机不计失败（本次没消耗钱包额度）。
		if st.extProxy != nil && !st.extProxyConsumed {
			_ = d.PaymentProxy.MarkFailed(ctx, st.extProxy.ID, "rate_limited at gopay")
			st.extProxyConsumed = true
		}
		svc.LogError(ctx, task.ID, "支付代理被限流，已标记失败；钱包/手机原样放回")

	case gopay.IsCode(runErr, gopay.ErrCodeOTPCancelled), gopay.IsCode(runErr, gopay.ErrCodeOTPTimeout):
		// 钱包 OTP 走流，下次还能用，但需要冷却避免 GoPay 风控。
		_ = d.Wallet.MarkFailed(ctx, st.wallet.ID, "OTP failed/timeout", cooldownMin)
		st.walletConsumed = true // mark failed 内部已转 cooldown，cleanup 别再 Release 覆盖
		svc.LogError(ctx, task.ID, fmt.Sprintf("OTP 未拿到，钱包 #%d 进入冷却 %d 分钟", st.wallet.ID, cooldownMin))

	case gopay.IsCode(runErr, gopay.ErrCodeChatGPTApprove),
		gopay.IsCode(runErr, gopay.ErrCodeStripeConfirm),
		gopay.IsCode(runErr, gopay.ErrCodeUnrecoverable):
		// 账号/会话侧问题（cookie 失效 / 风控 / stripe total!=0 没 promo 资格 /
		// checkout create 失败 等）。钱包 / 代理 / 云手机都无责，原样放回 ——
		// 不能因为账号没 0 元 promo 资格就惩罚钱包，否则资源池会被这种本来
		// 可控的失败快速烧光。
		svc.LogError(ctx, task.ID, fmt.Sprintf("账号/会话失败（钱包/代理无责，原样放回）：%v", runErr))

	case gopay.IsCode(runErr, gopay.ErrCodeNetwork):
		// 网络抖动可以细分两类：
		//   1) 真随机抖动（TLS EOF / connection reset / DNS 偶失败）：所有资源原样放回。
		//   2) 代理本身坏（proxy CONNECT 失败 / 502 / 407 / 403）：钱包/手机无责，
		//      但代理要标 failed，否则下个任务又会抽到同一条死代理。
		errStr := runErr.Error()
		proxyDead := strings.Contains(errStr, "proxy CONNECT") ||
			strings.Contains(errStr, "Bad Gateway") ||
			strings.Contains(errStr, "HTTP 502") ||
			strings.Contains(errStr, "HTTP 503") ||
			strings.Contains(errStr, "HTTP 407") ||
			strings.Contains(errStr, "Proxy Authentication Required")
		if proxyDead && st.extProxy != nil && !st.extProxyConsumed {
			reason := "proxy CONNECT/auth failed: " + errStr
			if len(reason) > 200 {
				reason = reason[:200]
			}
			_ = d.PaymentProxy.MarkFailed(ctx, st.extProxy.ID, reason)
			st.extProxyConsumed = true
			svc.LogError(ctx, task.ID, fmt.Sprintf("代理 #%d 通道不可用（连接/认证失败），已标记失败；钱包/手机原样放回：%v", st.extProxy.ID, runErr))
		} else {
			svc.LogError(ctx, task.ID, fmt.Sprintf("网络抖动（钱包/代理无责，原样放回）：%v", runErr))
		}

	case gopay.IsCode(runErr, gopay.ErrCodeProxyBanned):
		// 代理本身被 banned —— 标 proxy failed，钱包/手机原样放回。
		if st.extProxy != nil && !st.extProxyConsumed {
			_ = d.PaymentProxy.MarkFailed(ctx, st.extProxy.ID, "proxy banned")
			st.extProxyConsumed = true
		}
		svc.LogError(ctx, task.ID, "代理被封禁，已标记失败；钱包/手机原样放回")

	case gopay.IsCode(runErr, gopay.ErrCodeChargeRejected):
		// Midtrans /charge 返回 HTTP 200 但 body.status_code != 200/201
		// （典型："Transaksi Anda ditolak"）。常见原因：钱包余额不足 / 钱包风控 /
		// 支付代理出口 IP 被打标。**单钱包池场景下不冷却钱包**，否则会一锅端；
		// 把疑点引到代理（IP 风控是最常见、也最容易自愈的一条），下次任务自然
		// 抽到另一个出口 IP。手机/钱包原样放回。
		if st.extProxy != nil && !st.extProxyConsumed {
			_ = d.PaymentProxy.MarkFailed(ctx, st.extProxy.ID, "midtrans charge rejected via this proxy")
			st.extProxyConsumed = true
			svc.LogError(ctx, task.ID, fmt.Sprintf(
				"GoPay 拒绝扣款，已把支付代理 #%d 标记失败（出口 IP 大概率被风控）；钱包/手机原样放回。若多个代理都连续被拒，请去 GoPay app 检查钱包余额或风控状态：%v",
				st.extProxy.ID, runErr))
		} else {
			svc.LogError(ctx, task.ID, fmt.Sprintf(
				"GoPay 拒绝扣款（钱包/代理/手机原样放回；若再次复现请去 GoPay app 检查余额/风控）：%v", runErr))
		}

	case gopay.IsCode(runErr, gopay.ErrCodeMidtransLink):
		// Midtrans linking / charge 在 GoPay 校验前失败 —— 这一步还没动钱包余额，
		// 失败原因通常是 ext 代理出口被 Midtrans 风控（"linking exhausted retries: rate_limited"）
		// 或者 snap token 失效。**钱包无责，原样放回**；如果错误信息明确指向代理，
		// 顺手把代理也标 failed。
		errStr := runErr.Error()
		if st.extProxy != nil && !st.extProxyConsumed &&
			(strings.Contains(errStr, "rate_limited") || strings.Contains(errStr, "429")) {
			_ = d.PaymentProxy.MarkFailed(ctx, st.extProxy.ID, "midtrans rate_limited via this proxy")
			st.extProxyConsumed = true
			svc.LogError(ctx, task.ID, fmt.Sprintf("GoPay 绑定被限流，代理 #%d 已标记失败；钱包/手机原样放回：%v", st.extProxy.ID, runErr))
		} else {
			svc.LogError(ctx, task.ID, fmt.Sprintf("GoPay 绑定失败（钱包/代理无责，原样放回）：%v", runErr))
		}

	default:
		// 真正未知的失败：钱包/代理/手机一律原样放回，由人去 admin 后台看日志再处置。
		// 历史上 default 分支会无脑冷冻钱包，导致大量任务因为暂态失败连锁烧光池子；
		// 现在所有已知错误码都有单独分支处理（network / chatgpt_approve / stripe_confirm /
		// unrecoverable / rate_limited / proxy_banned / midtrans_link / pin_rejected /
		// otp_timeout / otp_cancelled），default 仅留作未来扩展的兜底。
		svc.LogError(ctx, task.ID, fmt.Sprintf("未知失败（钱包/代理无责，原样放回）：%v", runErr))
	}

	return runErr // RegisterTaskService 会调 FinishFailed
}

// cleanup 把所有未消耗的资源放回池里。任何路径退出都跑一次。
func (d *Dispatcher) cleanup(ctx context.Context, svc *service.RegisterTaskService, task *model.RegisterTask, st *runState) {
	if st == nil {
		return
	}
	if st.wallet != nil && !st.walletConsumed {
		if err := d.Wallet.Release(ctx, st.wallet.ID); err != nil {
			if svc != nil && task != nil {
				svc.LogWarn(ctx, task.ID, fmt.Sprintf("释放钱包 #%d 失败: %v", st.wallet.ID, err))
			}
		}
	}
	// proxy / phone 没有显式 release 概念（它们都是共享的 PickRandom 池），
	// 失败的 proxy 已经在 handleRunError 里 MarkFailed；成功则 MarkUsed。
	// 这里只补一个保险：如果都没标记过，做一次 MarkUsed（避免代理统计漏算）。
	if st.extProxy != nil && !st.extProxyConsumed {
		_ = d.PaymentProxy.MarkUsed(ctx, st.extProxy.ID)
	}
	if st.csProxy != nil && !st.csProxyConsumed {
		_ = d.PaymentProxy.MarkUsed(ctx, st.csProxy.ID)
	}
}

// pickProxy 优先按指定 id 取，否则按 country 在池中 PickRandom。
//
// country 为空字符串时不做国家过滤（PickRandom 一条 active 即可）。
func (d *Dispatcher) pickProxy(ctx context.Context, id uint64, country string) (*model.PaymentProxyPool, error) {
	if id > 0 {
		p, err := d.PaymentProxy.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("payment_proxy #%d 不存在", id)
		}
		if p.Status != model.PaymentProxyStatusActive {
			return nil, fmt.Errorf("payment_proxy #%d 状态 %s 不可用", id, p.Status)
		}
		return p, nil
	}
	return d.PaymentProxy.PickRandom(ctx, country)
}

// buildProxyURL 把 PaymentProxyPool 转成可用的 proxy URL。支持静态 host:port 和动态 api_url。
func (d *Dispatcher) buildProxyURL(ctx context.Context, p *model.PaymentProxyPool) (string, error) {
	if p == nil {
		return "", errors.New("proxy is nil")
	}
	// 静态：直接用 BuildURL
	if p.Host != nil && *p.Host != "" {
		u, err := d.PaymentProxy.BuildURL(p)
		if err != nil {
			return "", err
		}
		return u.String(), nil
	}
	// 动态：先 fetch api_url，目前 PaymentProxyService 没暴露 fetch helper，
	// 这种情况下这里短期 fallback：直接报错引导用户改用静态 / 等 service 暴露 fetch helper。
	if p.APIURL != nil && *p.APIURL != "" {
		return "", errors.New("dynamic proxy (api_url) not yet wired in dispatcher; please add a static host:port proxy for now")
	}
	return "", errors.New("proxy missing host/api_url")
}

// parsePayload 解析 task.Payload。
func parsePayload(raw []byte) (*Payload, error) {
	if len(raw) == 0 {
		return nil, errors.New("payload 为空")
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("payload JSON 错误: %w", err)
	}
	return &p, nil
}

// stableDeviceID 用账号 ID 派生一个稳定的 oai-device-id。
//
// 注意：跟实际注册时记的 device_id 可能不同；OpenAI 偶尔会因此触发 sentinel
// 重新校验，但不会拒认 access_token。若发现 backend-api 风控可改为按账号
// 持久化存储 device_id（pool_gpt 加列）。
func stableDeviceID(accountID uint64) string {
	return fmt.Sprintf("oaidev-%s", uuid.NewSHA1(uuid.Nil, []byte(strconv.FormatUint(accountID, 10))).String()[:32])
}

// buildCookies 从 access_token + device_id 拼最小 cookie 串。
//
// chatgpt.com backend-api 在 Bearer auth 模式下不强制要 cookie，但带 oai-did
// 能避免一些情况下的 sentinel 抖动。__Secure-next-auth.session-token 我们
// 没有就不带。
func buildCookies(accessToken, deviceID string) string {
	parts := []string{}
	if deviceID != "" {
		parts = append(parts, "oai-did="+deviceID)
	}
	if accessToken != "" {
		// 不再塞 sessionToken（我们没存 cookie）。这里仅占位避免空 Cookie 头被
		// browser.Client 跳过；实际验证只靠 Authorization Bearer。
	}
	return strings.Join(parts, "; ")
}

// nonZeroTime 兜底：result.ChargedAt 为零值时回退到 now，避免写库出 0001-01-01。
func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

// maskEmail / maskPhone 简单脱敏，仅用于日志。
func maskEmail(e string) string {
	at := strings.Index(e, "@")
	if at <= 0 {
		return e
	}
	user := e[:at]
	if len(user) <= 2 {
		return e
	}
	return user[:2] + strings.Repeat("*", len(user)-2) + e[at:]
}

func maskPhone(p string) string {
	if len(p) < 7 {
		return p
	}
	return p[:3] + strings.Repeat("*", len(p)-6) + p[len(p)-3:]
}

// 防止 net/url 被裁剪（buildProxyURL 间接依赖）。
var _ = url.Parse
