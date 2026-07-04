package captcha

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ChainSolver 按顺序串联多个 Solver。任一 solver 返回非零错误时立刻切到下一家，
// 直到某家成功返回 token；全部失败时聚合错误返回。
//
// 设计动机（2026-05 Adobe Banana 注册线上观察）：
//   - 单家 solver（如 anti-captcha）队列堵塞时，60s 等待超时直接把邮箱 / 代理 /
//     captcha blob 全部浪费掉；
//   - blob 在 solver 端是"无副作用"的：worker 只要拿不出 token，blob 仍然是有效
//     challenge，可以让下一家继续解；
//   - 把多家供应商串成链，单家超时立刻 fail-over 到备胎，可在不浪费邮箱前提下
//     把整体解题率从 ~70%（单家）拉到 ~91%（两家）/ ~97%（三家）。
//
// 注意：
//   - 失败 fail-over 的前提是 token "从未提交给上游"。也就是说 ChainSolver 应当
//     在拿到 token 之后立刻交给 Adobe / GPT / Grok 提交，一旦上游拒绝 token
//     （捕获 captcha_required 之类），blob 已被销毁，重新链跑只会浪费打码钱，
//     dispatcher 应当走"放弃 + fast-fail 跳号"的常规路径。
//   - ErrNotConfigured 视为该 solver 跳过（不计入聚合错误），用于支持
//     "fallback 列表里塞了空 api_key 的占位"这种半残配置。
//   - context 取消会立刻终止整条链，把 ctx.Err 直接返回。
type ChainSolver struct {
	solvers []Solver
	// PerAttempt 单家允许的最长解题时间。0 表示沿用各 solver 内置默认
	// （ArkoseMaxWait=60s / TurnstileMaxWait=180s）。
	//
	// 用法：当链上有 2 家以上 solver 时，dispatcher 应当把 PerAttempt 调小
	// （比如 2 家 → 45s/家、3 家 → 35s/家），保证总时间预算可控。
	PerAttempt time.Duration
	// OnAttempt 在每家 solver 开始 SolveXxx 调用前回调。可空。
	//   idx       从 1 起的链上顺位
	//   solverNm  当前 solver 名称（capsolver / anti-captcha / ...）
	//   total     链长度
	// 用于 dispatcher 把链路进度落到 register_task_log，方便排查
	// "anti-captcha 超时 → nopecha 接手"这种切换路径。
	OnAttempt func(idx int, solverName string, total int)
	// OnFailover 单家失败后回调。可空。idx 同 OnAttempt 语义。
	OnFailover func(idx int, solverName string, err error)
}

// NewChain 构造一条 fallback 链。solvers 应当至少包含 1 家；空链返回 nil。
//
// 如果只有 1 家，仍然返回 ChainSolver（语义和直接用单家完全一致，只是多走了
// 一层包装），方便上层无脑构造而不用区分"单家 / 多家"两条路径。
func NewChain(solvers ...Solver) *ChainSolver {
	cleaned := make([]Solver, 0, len(solvers))
	for _, s := range solvers {
		if s == nil {
			continue
		}
		cleaned = append(cleaned, s)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return &ChainSolver{solvers: cleaned}
}

// Solvers 返回链上所有 solver（供上层观测 / 测试用，不要直接修改返回切片）。
func (c *ChainSolver) Solvers() []Solver { return c.solvers }

// Name 返回 "chain(name1+name2+...)"，方便日志识别。
func (c *ChainSolver) Name() string {
	if c == nil || len(c.solvers) == 0 {
		return "chain(empty)"
	}
	if len(c.solvers) == 1 {
		return c.solvers[0].Name()
	}
	names := make([]string, 0, len(c.solvers))
	for _, s := range c.solvers {
		names = append(names, s.Name())
	}
	return "chain(" + strings.Join(names, "+") + ")"
}

// SolveArkose 按顺序尝试每个 solver；任意一家成功返回 token 即返回。
func (c *ChainSolver) SolveArkose(ctx context.Context, t *ArkoseTask) (string, error) {
	if c == nil || len(c.solvers) == 0 {
		return "", ErrNotConfigured
	}
	total := len(c.solvers)
	var errs []string
	skipped := 0
	for idx, s := range c.solvers {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if c.OnAttempt != nil {
			c.OnAttempt(idx+1, s.Name(), total)
		}
		token, err := c.solveArkoseOne(ctx, s, t)
		if err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
		// ErrNotConfigured 视为静默跳过，不计入聚合错误（"占位 solver"）。
		if errors.Is(err, ErrNotConfigured) {
			skipped++
			continue
		}
		if err == nil {
			err = errors.New("返回空 token")
		}
		if c.OnFailover != nil {
			c.OnFailover(idx+1, s.Name(), err)
		}
		// 收集错误，标注 solver 顺位，方便排查"链路里哪家挂了"。
		errs = append(errs, fmt.Sprintf("[%d/%s] %v", idx+1, s.Name(), err))
	}
	if skipped == total {
		return "", ErrNotConfigured
	}
	return "", fmt.Errorf("captcha chain 所有 solver 均失败：%s", strings.Join(errs, " | "))
}

// SolveTurnstile 同 SolveArkose 的语义，但目标任务是 Cloudflare Turnstile。
func (c *ChainSolver) SolveTurnstile(ctx context.Context, t *TurnstileTask) (string, error) {
	if c == nil || len(c.solvers) == 0 {
		return "", ErrNotConfigured
	}
	total := len(c.solvers)
	var errs []string
	skipped := 0
	for idx, s := range c.solvers {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if c.OnAttempt != nil {
			c.OnAttempt(idx+1, s.Name(), total)
		}
		token, err := c.solveTurnstileOne(ctx, s, t)
		if err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
		if errors.Is(err, ErrNotConfigured) {
			skipped++
			continue
		}
		if err == nil {
			err = errors.New("返回空 token")
		}
		if c.OnFailover != nil {
			c.OnFailover(idx+1, s.Name(), err)
		}
		errs = append(errs, fmt.Sprintf("[%d/%s] %v", idx+1, s.Name(), err))
	}
	if skipped == total {
		return "", ErrNotConfigured
	}
	return "", fmt.Errorf("captcha chain 所有 solver 均失败：%s", strings.Join(errs, " | "))
}

// solveArkoseOne 套一个 per-attempt 超时再调单个 solver。
//
// PerAttempt > 0 时给当前调用裹一层带超时的 ctx；solver 自身的 MaxWait 仍然生效，
// 取两者的较小值。换句话说：dispatcher 调一句 PerAttempt=45s，单家解到第 45s
// 就会被 ctx 干掉切到下一家，即便该 solver 内置 MaxWait 是 60s。
func (c *ChainSolver) solveArkoseOne(parent context.Context, s Solver, t *ArkoseTask) (string, error) {
	if c.PerAttempt <= 0 {
		return s.SolveArkose(parent, t)
	}
	ctx, cancel := context.WithTimeout(parent, c.PerAttempt)
	defer cancel()
	return s.SolveArkose(ctx, t)
}

func (c *ChainSolver) solveTurnstileOne(parent context.Context, s Solver, t *TurnstileTask) (string, error) {
	if c.PerAttempt <= 0 {
		return s.SolveTurnstile(parent, t)
	}
	ctx, cancel := context.WithTimeout(parent, c.PerAttempt)
	defer cancel()
	return s.SolveTurnstile(ctx, t)
}
