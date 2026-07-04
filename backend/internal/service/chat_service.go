package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
	gptprovider "github.com/kleinai/backend/internal/provider/gpt"
	grokweb "github.com/kleinai/backend/internal/provider/grok"
	xaiprovider "github.com/kleinai/backend/internal/provider/xai"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/logger"
	"github.com/kleinai/backend/pkg/outbound"
)

type ChatService struct {
	db       *gorm.DB
	repo     *repo.GenerationRepo
	pool     *AccountPool
	billing  *BillingService
	priceFn  func(modelCode string) ChatPrice
	cfg      *SystemConfigService
	aes      *crypto.AESGCM
	proxySvc *ProxyService
	client   *http.Client
	grok     *grokweb.WebClient
	codex    *gptprovider.CodexChatClient
	mock     bool
	grokMock bool
	xaiMock  bool
	cost     *CostRecorder // Phase B 落账；nil 表示不写 task_cost_log
}

// SetCostRecorder 注入上游成本记录器。
func (s *ChatService) SetCostRecorder(c *CostRecorder) {
	if s == nil {
		return
	}
	s.cost = c
}

type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatCallRequest struct {
	UserID   uint64
	APIKeyID *uint64
	ClientIP string
	IdemKey  string
	Body     map[string]any
	RawBody  []byte
}

func NewChatService(db *gorm.DB, r *repo.GenerationRepo, pool *AccountPool, billing *BillingService, cfg *SystemConfigService, aes *crypto.AESGCM, proxySvc *ProxyService) *ChatService {
	return &ChatService{
		db:       db,
		repo:     r,
		pool:     pool,
		billing:  billing,
		priceFn:  ConfigChatPriceFn(cfg),
		cfg:      cfg,
		aes:      aes,
		proxySvc: proxySvc,
		client:   &http.Client{Timeout: 10 * time.Minute},
		grok:     grokweb.NewWebClient(os.Getenv("KLEIN_GROK_BASE_URL")),
		codex:    gptprovider.NewCodexChatClient(),
		mock:     !isLiveProvider(os.Getenv("KLEIN_PROVIDER_GPT")),
		grokMock: !isLiveProvider(os.Getenv("KLEIN_PROVIDER_GROK")),
		xaiMock:  !isLiveProvider(os.Getenv("KLEIN_PROVIDER_XAI")),
	}
}

func (s *ChatService) Complete(ctx context.Context, req ChatCallRequest) ([]byte, int, error) {
	modelCode := strAny(req.Body["model"], "gpt-4o-mini")
	prompt := sanitizeDBText(chatPrompt(req.Body))
	if s.cfg != nil {
		if err := s.cfg.ValidateKeywordSafe(ctx, prompt); err != nil {
			return nil, http.StatusBadRequest, err
		}
	}
	extProvider := s.externalChatProvider(modelCode)
	if extProvider == "" {
		if xaiprovider.IsChatModel(modelCode) {
			return s.completeXAI(ctx, req, modelCode)
		}
		if grokweb.IsChatModel(modelCode) {
			return s.completeGrok(ctx, req, modelCode)
		}
		if gptprovider.IsCodexChatModel(modelCode) {
			return s.completeCodex(ctx, req, modelCode)
		}
	}
	providerName := model.ProviderGPT
	if extProvider != "" {
		providerName = extProvider
	}
	req.Body["model"] = s.upstreamModel(modelCode)
	req.Body["stream"] = false
	estimate := s.estimateCost(modelCode, req.Body)
	// 外部转发型 provider（newapi 等真实上游）始终走真实调用，不受 GPT mock 开关影响。
	if s.mock && extProvider == "" {
		return s.completeMock(ctx, req, modelCode, prompt, estimate)
	}
	t, acc, err := s.prepare(ctx, req, modelCode, prompt, estimate, providerName)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	raw, status, usage, err := s.callJSON(ctx, acc, req.Body)
	if err != nil {
		s.fail(ctx, t, err.Error())
		return nil, status, err
	}
	if status >= 400 {
		s.fail(ctx, t, fmt.Sprintf("upstream http %d: %s", status, snippet(raw, 240)))
		return raw, status, nil
	}
	actual := estimate
	if usage != nil {
		actual = ChatCost(s.priceFn(modelCode), usage.PromptTokens, usage.CompletionTokens)
	}
	_ = s.repo.UpdateCost(ctx, t.TaskID, actual)
	if t.CostPoints > 0 || actual > 0 {
		if err := s.billing.FinalizeUsage(ctx, t.TaskID, actual, &acc.ID); err != nil {
			s.fail(ctx, t, err.Error())
			return nil, http.StatusBadRequest, err
		}
	}
	_ = s.repo.SetSucceeded(ctx, t.TaskID, nil)
	s.recordChatCost(ctx, t, acc, modelCode, usage, actual)
	return raw, status, nil
}

func (s *ChatService) Stream(ctx context.Context, req ChatCallRequest, w http.ResponseWriter) error {
	modelCode := strAny(req.Body["model"], "gpt-4o-mini")
	prompt := sanitizeDBText(chatPrompt(req.Body))
	if s.cfg != nil {
		if err := s.cfg.ValidateKeywordSafe(ctx, prompt); err != nil {
			return err
		}
	}
	extProvider := s.externalChatProvider(modelCode)
	if extProvider == "" {
		if xaiprovider.IsChatModel(modelCode) {
			return s.streamXAI(ctx, req, modelCode, w)
		}
		if grokweb.IsChatModel(modelCode) {
			return s.streamGrok(ctx, req, modelCode, w)
		}
		if gptprovider.IsCodexChatModel(modelCode) {
			return s.streamCodex(ctx, req, modelCode, w)
		}
	}
	providerName := model.ProviderGPT
	if extProvider != "" {
		providerName = extProvider
	}
	req.Body["model"] = s.upstreamModel(modelCode)
	req.Body["stream"] = true
	req.Body["stream_options"] = map[string]any{"include_usage": true}
	estimate := s.estimateCost(modelCode, req.Body)
	if s.mock && extProvider == "" {
		return s.streamMock(ctx, req, modelCode, prompt, estimate, w)
	}
	t, acc, err := s.prepare(ctx, req, modelCode, prompt, estimate, providerName)
	if err != nil {
		return err
	}

	resp, err := s.openUpstream(ctx, acc, req.Body)
	if err != nil {
		s.fail(ctx, t, err.Error())
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		s.fail(ctx, t, fmt.Sprintf("upstream http %d: %s", resp.StatusCode, snippet(raw, 240)))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	var usage *ChatUsage
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload != "" && payload != "[DONE]" {
				if u := parseStreamUsage([]byte(payload)); u != nil {
					usage = u
				}
			}
		}
		_, _ = io.WriteString(w, line+"\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := sc.Err(); err != nil {
		s.fail(ctx, t, err.Error())
		return err
	}
	actual := estimate
	if usage != nil {
		actual = ChatCost(s.priceFn(modelCode), usage.PromptTokens, usage.CompletionTokens)
	}
	_ = s.repo.UpdateCost(context.Background(), t.TaskID, actual)
	if t.CostPoints > 0 || actual > 0 {
		if err := s.billing.FinalizeUsage(context.Background(), t.TaskID, actual, &acc.ID); err != nil {
			s.fail(context.Background(), t, err.Error())
			return err
		}
	}
	_ = s.repo.SetSucceeded(context.Background(), t.TaskID, nil)
	s.recordChatCost(context.Background(), t, acc, modelCode, usage, actual)
	return nil
}

func (s *ChatService) completeGrok(ctx context.Context, req ChatCallRequest, modelCode string) ([]byte, int, error) {
	req.Body["model"] = modelCode
	req.Body["stream"] = false
	prompt := chatPrompt(req.Body)
	estimate := s.estimateCost(modelCode, req.Body)
	if s.grokMock {
		return s.completeMockWithProvider(ctx, req, modelCode, prompt, estimate, model.ProviderGROK)
	}
	maxAttempts := 10
	retryDelay := 800 * time.Millisecond
	if s.cfg != nil {
		maxAttempts = s.cfg.RetryMaxAttempts(ctx)
		retryDelay = s.cfg.RetryBaseDelay(ctx)
	}
	var lastRaw []byte
	var lastStatus int
	var lastErr error
	allowProxyPoolFallback := false
	triedProxyIDs := map[uint64]struct{}{}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptReq := req
		if attempt > 1 && req.IdemKey != "" {
			attemptReq.IdemKey = fmt.Sprintf("%s-retry-%d", req.IdemKey, attempt)
		}
		t, acc, err := s.prepare(ctx, attemptReq, modelCode, prompt, estimate, model.ProviderGROK)
		if err != nil {
			if lastErr != nil {
				return nil, http.StatusBadGateway, lastErr
			}
			return nil, http.StatusBadRequest, err
		}

		cred, err := s.credential(acc)
		if err != nil {
			lastErr = err
			s.fail(ctx, t, err.Error())
			s.markChatFailure(ctx, acc, err)
			sleepBeforeRetry(ctx, retryDelay, attempt)
			continue
		}
		grok := s.grok
		proxyURL, proxyID, perr := s.resolveProxyURL(ctx, acc, triedProxyIDs, allowProxyPoolFallback)
		if perr != nil {
			logger.FromCtx(ctx).Warn("chat.grok.resolve_proxy", zap.Error(perr))
		}
		if proxyURL != "" || (acc.BaseURL != nil && *acc.BaseURL != "") {
			base := os.Getenv("KLEIN_GROK_BASE_URL")
			if acc.BaseURL != nil && *acc.BaseURL != "" {
				base = *acc.BaseURL
			}
			grok = grokweb.NewWebClientWithProxy(base, proxyURL)
		}
		res, err := grok.ChatComplete(ctx, cred, modelCode, req.Body)
		if err != nil {
			lastErr = err
			s.fail(ctx, t, err.Error())
			rotateProxy := shouldRotateProxyOnRetry(model.ProviderGROK, err)
			if rotateProxy && proxyID != 0 {
				triedProxyIDs[proxyID] = struct{}{}
			}
			if rotateProxy || isRetryableProxyTransportError(err) {
				allowProxyPoolFallback = true
			}
			s.markChatFailure(ctx, acc, err)
			if attempt < maxAttempts && retryableProviderError(err) {
				logger.FromCtx(ctx).Warn("chat.grok.retrying_with_next_account", zap.Int("attempt", attempt), zap.Uint64("account_id", acc.ID), zap.Uint64("proxy_id", proxyID), zap.Bool("rotate_proxy", rotateProxy), zap.Error(err))
				sleepBeforeRetry(ctx, retryDelay, attempt)
				continue
			}
			return nil, http.StatusBadGateway, err
		}
		if res.Status >= 400 {
			lastRaw = res.Raw
			lastStatus = res.Status
			lastErr = fmt.Errorf("grok chat http %d: %s", res.Status, snippet(res.Raw, 240))
			s.fail(ctx, t, lastErr.Error())
			rotateProxy := shouldRotateProxyOnRetry(model.ProviderGROK, lastErr)
			if rotateProxy && proxyID != 0 {
				triedProxyIDs[proxyID] = struct{}{}
			}
			if rotateProxy || isRetryableProxyTransportError(lastErr) {
				allowProxyPoolFallback = true
			}
			s.markChatFailure(ctx, acc, lastErr)
			if attempt < maxAttempts && retryableProviderError(lastErr) {
				logger.FromCtx(ctx).Warn("chat.grok.retrying_with_next_account", zap.Int("attempt", attempt), zap.Uint64("account_id", acc.ID), zap.Uint64("proxy_id", proxyID), zap.Bool("rotate_proxy", rotateProxy), zap.Error(lastErr))
				sleepBeforeRetry(ctx, retryDelay, attempt)
				continue
			}
			return res.Raw, res.Status, nil
		}
		actual := estimate
		if res.Usage != nil {
			actual = ChatCost(s.priceFn(modelCode), res.Usage.PromptTokens, res.Usage.CompletionTokens)
		}
		_ = s.repo.UpdateCost(ctx, t.TaskID, actual)
		if t.CostPoints > 0 || actual > 0 {
			if err := s.billing.FinalizeUsage(ctx, t.TaskID, actual, &acc.ID); err != nil {
				s.fail(ctx, t, err.Error())
				return nil, http.StatusBadRequest, err
			}
		}
		s.pool.MarkUsed(ctx, acc.ID)
		_ = s.repo.SetSucceeded(ctx, t.TaskID, nil)
		var chatUsage *ChatUsage
		if res.Usage != nil {
			chatUsage = &ChatUsage{PromptTokens: res.Usage.PromptTokens, CompletionTokens: res.Usage.CompletionTokens, TotalTokens: res.Usage.TotalTokens}
		}
		s.recordChatCost(ctx, t, acc, modelCode, chatUsage, actual)
		return res.Raw, res.Status, nil
	}
	if lastRaw != nil && lastStatus > 0 {
		return lastRaw, lastStatus, nil
	}
	return nil, http.StatusBadGateway, lastErr
}

func (s *ChatService) streamGrok(ctx context.Context, req ChatCallRequest, modelCode string, w http.ResponseWriter) error {
	req.Body["model"] = modelCode
	req.Body["stream"] = true
	prompt := chatPrompt(req.Body)
	estimate := s.estimateCost(modelCode, req.Body)
	if s.grokMock {
		return s.streamMockWithProvider(ctx, req, modelCode, prompt, estimate, model.ProviderGROK, w)
	}
	t, acc, err := s.prepare(ctx, req, modelCode, prompt, estimate, model.ProviderGROK)
	if err != nil {
		return err
	}
	cred, err := s.credential(acc)
	if err != nil {
		s.fail(ctx, t, err.Error())
		s.markChatFailure(ctx, acc, err)
		return err
	}
	grok := s.grok
	proxyURL, _, perr := s.resolveProxyURL(ctx, acc, nil, false)
	if perr != nil {
		logger.FromCtx(ctx).Warn("chat.grok.resolve_proxy", zap.Error(perr))
	}
	if proxyURL != "" || (acc.BaseURL != nil && *acc.BaseURL != "") {
		base := os.Getenv("KLEIN_GROK_BASE_URL")
		if acc.BaseURL != nil && *acc.BaseURL != "" {
			base = *acc.BaseURL
		}
		grok = grokweb.NewWebClientWithProxy(base, proxyURL)
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	usage, err := grok.ChatStream(ctx, cred, modelCode, req.Body, w, flusher)
	if err != nil {
		s.fail(ctx, t, err.Error())
		s.markChatFailure(ctx, acc, err)
		return err
	}
	actual := estimate
	if usage != nil {
		actual = ChatCost(s.priceFn(modelCode), usage.PromptTokens, usage.CompletionTokens)
	}
	_ = s.repo.UpdateCost(context.Background(), t.TaskID, actual)
	if t.CostPoints > 0 || actual > 0 {
		if err := s.billing.FinalizeUsage(context.Background(), t.TaskID, actual, &acc.ID); err != nil {
			s.fail(context.Background(), t, err.Error())
			return err
		}
	}
	s.pool.MarkUsed(context.Background(), acc.ID)
	_ = s.repo.SetSucceeded(context.Background(), t.TaskID, nil)
	var chatUsage *ChatUsage
	if usage != nil {
		chatUsage = &ChatUsage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens}
	}
	s.recordChatCost(context.Background(), t, acc, modelCode, chatUsage, actual)
	return nil
}

// completeXAI 走官方 xAI /responses 通道（非流式聚合）。
func (s *ChatService) completeXAI(ctx context.Context, req ChatCallRequest, modelCode string) ([]byte, int, error) {
	req.Body["model"] = modelCode
	req.Body["stream"] = false
	prompt := chatPrompt(req.Body)
	estimate := s.estimateCost(modelCode, req.Body)
	if s.xaiMock {
		return s.completeMockWithProvider(ctx, req, modelCode, prompt, estimate, model.ProviderXAI)
	}
	maxAttempts := 10
	retryDelay := 800 * time.Millisecond
	if s.cfg != nil {
		maxAttempts = s.cfg.RetryMaxAttempts(ctx)
		retryDelay = s.cfg.RetryBaseDelay(ctx)
	}
	var lastRaw []byte
	var lastStatus int
	var lastErr error
	allowProxyPoolFallback := false
	triedProxyIDs := map[uint64]struct{}{}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptReq := req
		if attempt > 1 && req.IdemKey != "" {
			attemptReq.IdemKey = fmt.Sprintf("%s-retry-%d", req.IdemKey, attempt)
		}
		t, acc, err := s.prepare(ctx, attemptReq, modelCode, prompt, estimate, model.ProviderXAI)
		if err != nil {
			if lastErr != nil {
				return nil, http.StatusBadGateway, lastErr
			}
			return nil, http.StatusBadRequest, err
		}
		cred, err := s.credential(acc)
		if err != nil {
			lastErr = err
			s.fail(ctx, t, err.Error())
			s.markChatFailure(ctx, acc, err)
			sleepBeforeRetry(ctx, retryDelay, attempt)
			continue
		}
		proxyURL, proxyID, perr := s.resolveProxyURL(ctx, acc, triedProxyIDs, allowProxyPoolFallback)
		if perr != nil {
			logger.FromCtx(ctx).Warn("chat.xai.resolve_proxy", zap.Error(perr))
		}
		base := xaiChatBaseURL(acc)
		cli := xaiprovider.NewClient(base, proxyURL)
		res, err := cli.ChatComplete(ctx, cred, modelCode, req.Body, base)
		if err != nil {
			lastErr = err
			s.fail(ctx, t, err.Error())
			rotateProxy := shouldRotateProxyOnRetry(model.ProviderXAI, err)
			if rotateProxy && proxyID != 0 {
				triedProxyIDs[proxyID] = struct{}{}
			}
			if rotateProxy || isRetryableProxyTransportError(err) {
				allowProxyPoolFallback = true
			}
			s.markChatFailure(ctx, acc, err)
			if attempt < maxAttempts && retryableProviderError(err) {
				sleepBeforeRetry(ctx, retryDelay, attempt)
				continue
			}
			return nil, http.StatusBadGateway, err
		}
		if res.Status >= 400 {
			lastRaw = res.Raw
			lastStatus = res.Status
			lastErr = fmt.Errorf("xai chat http %d: %s", res.Status, snippet(res.Raw, 240))
			s.fail(ctx, t, lastErr.Error())
			rotateProxy := shouldRotateProxyOnRetry(model.ProviderXAI, lastErr)
			if rotateProxy && proxyID != 0 {
				triedProxyIDs[proxyID] = struct{}{}
			}
			if rotateProxy || isRetryableProxyTransportError(lastErr) {
				allowProxyPoolFallback = true
			}
			s.markChatFailure(ctx, acc, lastErr)
			if attempt < maxAttempts && retryableProviderError(lastErr) {
				sleepBeforeRetry(ctx, retryDelay, attempt)
				continue
			}
			return res.Raw, res.Status, nil
		}
		actual := estimate
		if res.Usage != nil {
			actual = ChatCost(s.priceFn(modelCode), res.Usage.PromptTokens, res.Usage.CompletionTokens)
		}
		_ = s.repo.UpdateCost(ctx, t.TaskID, actual)
		if t.CostPoints > 0 || actual > 0 {
			if err := s.billing.FinalizeUsage(ctx, t.TaskID, actual, &acc.ID); err != nil {
				s.fail(ctx, t, err.Error())
				return nil, http.StatusBadRequest, err
			}
		}
		s.pool.MarkUsed(ctx, acc.ID)
		_ = s.repo.SetSucceeded(ctx, t.TaskID, nil)
		var chatUsage *ChatUsage
		if res.Usage != nil {
			chatUsage = &ChatUsage{PromptTokens: res.Usage.PromptTokens, CompletionTokens: res.Usage.CompletionTokens, TotalTokens: res.Usage.TotalTokens}
		}
		s.recordChatCost(ctx, t, acc, modelCode, chatUsage, actual)
		return res.Raw, res.Status, nil
	}
	if lastRaw != nil && lastStatus > 0 {
		return lastRaw, lastStatus, nil
	}
	return nil, http.StatusBadGateway, lastErr
}

// streamXAI 官方 xAI chat 流式：聚合后用 OpenAI streaming SSE 一次性下发。
func (s *ChatService) streamXAI(ctx context.Context, req ChatCallRequest, modelCode string, w http.ResponseWriter) error {
	if s.xaiMock {
		prompt := chatPrompt(req.Body)
		estimate := s.estimateCost(modelCode, req.Body)
		return s.streamMockWithProvider(ctx, req, modelCode, prompt, estimate, model.ProviderXAI, w)
	}
	raw, status, err := s.completeXAI(ctx, req, modelCode)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	content := ""
	if status < 400 {
		content = extractOpenAIContent(raw)
	} else {
		content = snippet(raw, 480)
	}
	created := time.Now().Unix()
	id := fmt.Sprintf("chatcmpl-xai-%d", time.Now().UnixNano())
	deltaChunk := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": modelCode,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}, "finish_reason": nil}},
	}
	stopChunk := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": modelCode,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	}
	for _, ch := range []map[string]any{deltaChunk, stopChunk} {
		b, _ := json.Marshal(ch)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// xaiChatBaseURL 解析账号的 xAI API base：account.base_url > 环境变量 > 默认。
func xaiChatBaseURL(acc *model.Account) string {
	if acc != nil && acc.BaseURL != nil && strings.TrimSpace(*acc.BaseURL) != "" {
		return strings.TrimSpace(*acc.BaseURL)
	}
	if v := strings.TrimSpace(os.Getenv("KLEIN_XAI_BASE_URL")); v != "" {
		return v
	}
	return xaiprovider.DefaultBaseURL
}

// extractOpenAIContent 从 OpenAI chat.completion JSON 抽出 assistant content。
func extractOpenAIContent(raw []byte) string {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	if len(parsed.Choices) > 0 {
		return parsed.Choices[0].Message.Content
	}
	return ""
}

func (s *ChatService) completeCodex(ctx context.Context, req ChatCallRequest, modelCode string) ([]byte, int, error) {
	req.Body["model"] = modelCode
	req.Body["stream"] = false
	prompt := chatPrompt(req.Body)
	estimate := s.estimateCost(modelCode, req.Body)
	if s.mock {
		return s.completeMockWithProvider(ctx, req, modelCode, prompt, estimate, model.ProviderGPT)
	}
	maxAttempts := 10
	retryDelay := 800 * time.Millisecond
	if s.cfg != nil {
		maxAttempts = s.cfg.RetryMaxAttempts(ctx)
		retryDelay = s.cfg.RetryBaseDelay(ctx)
	}
	var lastRaw []byte
	var lastStatus int
	var lastErr error
	allowProxyPoolFallback := false
	triedProxyIDs := map[uint64]struct{}{}
	codex := s.codex
	if codex == nil {
		codex = gptprovider.NewCodexChatClient()
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptReq := req
		if attempt > 1 && req.IdemKey != "" {
			attemptReq.IdemKey = fmt.Sprintf("%s-retry-%d", req.IdemKey, attempt)
		}
		t, acc, err := s.prepareCodex(ctx, attemptReq, modelCode, prompt, estimate)
		if err != nil {
			if lastErr != nil {
				return nil, http.StatusBadGateway, lastErr
			}
			return nil, http.StatusBadRequest, err
		}
		proxyURL, proxyID, perr := s.resolveProxyURL(ctx, acc, triedProxyIDs, allowProxyPoolFallback)
		if perr != nil {
			logger.FromCtx(ctx).Warn("chat.codex.resolve_proxy", zap.Error(perr))
		}
		cred, err := s.gptChatCredential(ctx, acc, proxyURL)
		if err != nil {
			lastErr = err
			s.fail(ctx, t, err.Error())
			s.markChatFailure(ctx, acc, err)
			sleepBeforeRetry(ctx, retryDelay, attempt)
			continue
		}
		res, err := codex.ChatComplete(ctx, cred, proxyURL, modelCode, req.Body)
		if err != nil {
			lastErr = err
			s.fail(ctx, t, err.Error())
			rotateProxy := shouldRotateProxyOnRetry(model.ProviderGPT, err)
			if rotateProxy && proxyID != 0 {
				triedProxyIDs[proxyID] = struct{}{}
			}
			if rotateProxy || isRetryableProxyTransportError(err) {
				allowProxyPoolFallback = true
			}
			s.markChatFailure(ctx, acc, err)
			if attempt < maxAttempts && retryableProviderError(err) {
				sleepBeforeRetry(ctx, retryDelay, attempt)
				continue
			}
			return nil, http.StatusBadGateway, err
		}
		if res.Status >= 400 {
			lastRaw = res.Raw
			lastStatus = res.Status
			lastErr = fmt.Errorf("codex chat http %d: %s", res.Status, snippet(res.Raw, 240))
			s.fail(ctx, t, lastErr.Error())
			rotateProxy := shouldRotateProxyOnRetry(model.ProviderGPT, lastErr)
			if rotateProxy && proxyID != 0 {
				triedProxyIDs[proxyID] = struct{}{}
			}
			if rotateProxy || isRetryableProxyTransportError(lastErr) {
				allowProxyPoolFallback = true
			}
			s.markChatFailure(ctx, acc, lastErr)
			if attempt < maxAttempts && retryableProviderError(lastErr) {
				sleepBeforeRetry(ctx, retryDelay, attempt)
				continue
			}
			return res.Raw, res.Status, nil
		}
		actual := estimate
		if res.Usage != nil {
			actual = ChatCost(s.priceFn(modelCode), res.Usage.PromptTokens, res.Usage.CompletionTokens)
		}
		_ = s.repo.UpdateCost(ctx, t.TaskID, actual)
		if t.CostPoints > 0 || actual > 0 {
			if err := s.billing.FinalizeUsage(ctx, t.TaskID, actual, &acc.ID); err != nil {
				s.fail(ctx, t, err.Error())
				return nil, http.StatusBadRequest, err
			}
		}
		s.pool.MarkUsed(ctx, acc.ID)
		_ = s.repo.SetSucceeded(ctx, t.TaskID, nil)
		var chatUsage *ChatUsage
		if res.Usage != nil {
			chatUsage = &ChatUsage{PromptTokens: res.Usage.PromptTokens, CompletionTokens: res.Usage.CompletionTokens, TotalTokens: res.Usage.TotalTokens}
		}
		s.recordChatCost(ctx, t, acc, modelCode, chatUsage, actual)
		return res.Raw, res.Status, nil
	}
	if lastRaw != nil && lastStatus > 0 {
		return lastRaw, lastStatus, nil
	}
	return nil, http.StatusBadGateway, lastErr
}

func (s *ChatService) streamCodex(ctx context.Context, req ChatCallRequest, modelCode string, w http.ResponseWriter) error {
	req.Body["model"] = modelCode
	req.Body["stream"] = true
	prompt := chatPrompt(req.Body)
	estimate := s.estimateCost(modelCode, req.Body)
	if s.mock {
		return s.streamMockWithProvider(ctx, req, modelCode, prompt, estimate, model.ProviderGPT, w)
	}
	t, acc, err := s.prepareCodex(ctx, req, modelCode, prompt, estimate)
	if err != nil {
		return err
	}
	proxyURL, _, perr := s.resolveProxyURL(ctx, acc, nil, false)
	if perr != nil {
		logger.FromCtx(ctx).Warn("chat.codex.resolve_proxy", zap.Error(perr))
	}
	cred, err := s.gptChatCredential(ctx, acc, proxyURL)
	if err != nil {
		s.fail(ctx, t, err.Error())
		s.markChatFailure(ctx, acc, err)
		return err
	}
	codex := s.codex
	if codex == nil {
		codex = gptprovider.NewCodexChatClient()
	}
	usage, err := codex.ChatStream(ctx, cred, proxyURL, modelCode, req.Body, w)
	if err != nil {
		s.fail(ctx, t, err.Error())
		s.markChatFailure(ctx, acc, err)
		return err
	}
	actual := estimate
	if usage != nil {
		actual = ChatCost(s.priceFn(modelCode), usage.PromptTokens, usage.CompletionTokens)
	}
	_ = s.repo.UpdateCost(context.Background(), t.TaskID, actual)
	if t.CostPoints > 0 || actual > 0 {
		if err := s.billing.FinalizeUsage(context.Background(), t.TaskID, actual, &acc.ID); err != nil {
			s.fail(context.Background(), t, err.Error())
			return err
		}
	}
	s.pool.MarkUsed(context.Background(), acc.ID)
	_ = s.repo.SetSucceeded(context.Background(), t.TaskID, nil)
	var chatUsage *ChatUsage
	if usage != nil {
		chatUsage = &ChatUsage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens}
	}
	s.recordChatCost(context.Background(), t, acc, modelCode, chatUsage, actual)
	return nil
}

func (s *ChatService) prepareCodex(ctx context.Context, req ChatCallRequest, modelCode, prompt string, estimate int64) (*model.GenerationTask, *model.Account, error) {
	return s.prepareWithFilter(ctx, req, modelCode, prompt, estimate, model.ProviderGPT, isCodexOAuthGPTAccount)
}

func (s *ChatService) gptChatCredential(ctx context.Context, acc *model.Account, proxyURL string) (string, error) {
	if acc == nil {
		return "", fmt.Errorf("missing account")
	}
	if acc.AuthType == model.AuthTypeOAuth && acc.Provider == model.ProviderGPT {
		return resolveGPToAuthAccessToken(ctx, gptOAuthTokenDeps{aes: s.aes, cfg: s.cfg, pool: s.pool}, acc, proxyURL)
	}
	return s.credential(acc)
}

func (s *ChatService) completeMock(ctx context.Context, req ChatCallRequest, modelCode, prompt string, estimate int64) ([]byte, int, error) {
	return s.completeMockWithProvider(ctx, req, modelCode, prompt, estimate, model.ProviderGPT)
}

func (s *ChatService) completeMockWithProvider(ctx context.Context, req ChatCallRequest, modelCode, prompt string, estimate int64, providerName string) ([]byte, int, error) {
	t, err := s.prepareMock(ctx, req, modelCode, prompt, estimate, providerName)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	usage := &ChatUsage{PromptTokens: estimatePromptTokens(req.Body), CompletionTokens: 32}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	actual := ChatCost(s.priceFn(modelCode), usage.PromptTokens, usage.CompletionTokens)
	_ = s.repo.UpdateCost(ctx, t.TaskID, actual)
	if t.CostPoints > 0 || actual > 0 {
		if err := s.billing.FinalizeUsage(ctx, t.TaskID, actual, nil); err != nil {
			s.fail(ctx, t, err.Error())
			return nil, http.StatusBadRequest, err
		}
	}
	_ = s.repo.SetSucceeded(ctx, t.TaskID, nil)
	raw, _ := json.Marshal(map[string]any{
		"id":      "chatcmpl_" + t.TaskID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelCode,
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "这是 mock 文字回复，用于本地和测试环境验证计费链路。"}, "finish_reason": "stop"}},
		"usage":   usage,
	})
	return raw, http.StatusOK, nil
}

func (s *ChatService) streamMock(ctx context.Context, req ChatCallRequest, modelCode, prompt string, estimate int64, w http.ResponseWriter) error {
	return s.streamMockWithProvider(ctx, req, modelCode, prompt, estimate, model.ProviderGPT, w)
}

func (s *ChatService) streamMockWithProvider(ctx context.Context, req ChatCallRequest, modelCode, prompt string, estimate int64, providerName string, w http.ResponseWriter) error {
	t, err := s.prepareMock(ctx, req, modelCode, prompt, estimate, providerName)
	if err != nil {
		return err
	}
	usage := &ChatUsage{PromptTokens: estimatePromptTokens(req.Body), CompletionTokens: 32}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	chunks := []string{"这是 ", "mock ", "流式回复。"}
	for _, ch := range chunks {
		payload, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": ch}, "index": 0}}})
		_, _ = io.WriteString(w, "data: "+string(payload)+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
	payload, _ := json.Marshal(map[string]any{"choices": []map[string]any{}, "usage": usage})
	_, _ = io.WriteString(w, "data: "+string(payload)+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	actual := ChatCost(s.priceFn(modelCode), usage.PromptTokens, usage.CompletionTokens)
	_ = s.repo.UpdateCost(context.Background(), t.TaskID, actual)
	if t.CostPoints > 0 || actual > 0 {
		if err := s.billing.FinalizeUsage(context.Background(), t.TaskID, actual, nil); err != nil {
			s.fail(context.Background(), t, err.Error())
			return err
		}
	}
	_ = s.repo.SetSucceeded(context.Background(), t.TaskID, nil)
	return nil
}

func (s *ChatService) prepareMock(ctx context.Context, req ChatCallRequest, modelCode, prompt string, estimate int64, providerName string) (*model.GenerationTask, error) {
	if req.IdemKey == "" {
		req.IdemKey = uuid.NewString()
	}
	prompt = sanitizeDBText(prompt)
	taskID := chatTaskID()
	params, _ := json.Marshal(map[string]any{"estimate_points": estimate, "mock": true})
	t := &model.GenerationTask{
		TaskID:       taskID,
		UserID:       req.UserID,
		Kind:         string(provider.KindChat),
		Mode:         "chat",
		ModelCode:    modelCode,
		Prompt:       prompt,
		Params:       string(params),
		Count:        1,
		CostPoints:   estimate,
		IdemKey:      req.IdemKey,
		Provider:     providerName,
		Status:       model.GenStatusRunning,
		Progress:     5,
		FromAPIKeyID: req.APIKeyID,
	}
	if req.ClientIP != "" {
		ip := req.ClientIP
		t.ClientIP = &ip
	}
	if err := s.repo.Create(ctx, t); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	if estimate > 0 {
		if err := s.billing.PreDeduct(ctx, PreDeductReq{UserID: req.UserID, TaskID: taskID, Kind: string(provider.KindChat), ModelCode: modelCode, Count: 1, UnitPoints: estimate}); err != nil {
			_ = s.repo.SetFailed(ctx, taskID, err.Error())
			return nil, err
		}
	}
	return t, nil
}

func (s *ChatService) prepare(ctx context.Context, req ChatCallRequest, modelCode, prompt string, estimate int64, providerName string) (*model.GenerationTask, *model.Account, error) {
	var filter func(*model.Account) bool
	if providerName == model.ProviderGROK {
		requiredPlan := requiredGrokPlanForTask(modelCode, provider.KindChat)
		filter = func(candidate *model.Account) bool {
			return accountSupportsGrokPlan(candidate, requiredPlan)
		}
	}
	return s.prepareWithFilter(ctx, req, modelCode, prompt, estimate, providerName, filter)
}

func (s *ChatService) prepareWithFilter(ctx context.Context, req ChatCallRequest, modelCode, prompt string, estimate int64, providerName string, filter func(*model.Account) bool) (*model.GenerationTask, *model.Account, error) {
	if req.IdemKey == "" {
		req.IdemKey = uuid.NewString()
	}
	prompt = sanitizeDBText(prompt)
	if existing, err := s.repo.GetByIdem(ctx, req.UserID, req.IdemKey); err == nil && existing != nil {
		return nil, nil, errcode.InvalidParam.WithMsg("idempotent chat replay is not supported for response body")
	}
	var acc *model.Account
	var err error
	if filter != nil {
		acc, err = s.pool.PickWhere(ctx, providerName, "round_robin", filter)
	} else {
		acc, err = s.pool.Pick(ctx, providerName, "round_robin")
	}
	if err != nil {
		return nil, nil, errcode.ResourceMissing.WithMsg("no available chat account: " + err.Error())
	}
	taskID := chatTaskID()
	params, _ := json.Marshal(map[string]any{"estimate_points": estimate})
	t := &model.GenerationTask{
		TaskID:       taskID,
		UserID:       req.UserID,
		Kind:         string(provider.KindChat),
		Mode:         "chat",
		ModelCode:    modelCode,
		Prompt:       prompt,
		Params:       string(params),
		Count:        1,
		CostPoints:   estimate,
		IdemKey:      req.IdemKey,
		Provider:     providerName,
		Status:       model.GenStatusPending,
		FromAPIKeyID: req.APIKeyID,
	}
	if req.ClientIP != "" {
		ip := req.ClientIP
		t.ClientIP = &ip
	}
	if err := s.repo.Create(ctx, t); err != nil {
		return nil, nil, errcode.DBError.Wrap(err)
	}
	if estimate > 0 {
		if err := s.billing.PreDeduct(ctx, PreDeductReq{UserID: req.UserID, TaskID: taskID, Kind: string(provider.KindChat), ModelCode: modelCode, Count: 1, UnitPoints: estimate}); err != nil {
			_ = s.repo.SetFailed(ctx, taskID, err.Error())
			return nil, nil, err
		}
	}
	if _, err := s.repo.SetRunning(ctx, taskID, acc.ID); err != nil {
		logger.FromCtx(ctx).Warn("chat.set_running", zap.Error(err))
	}
	return t, acc, nil
}

func (s *ChatService) callJSON(ctx context.Context, acc *model.Account, body map[string]any) ([]byte, int, *ChatUsage, error) {
	resp, err := s.openUpstream(ctx, acc, body)
	if err != nil {
		return nil, http.StatusBadGateway, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return raw, resp.StatusCode, nil, nil
	}
	return raw, resp.StatusCode, parseUsage(raw), nil
}

func (s *ChatService) openUpstream(ctx context.Context, acc *model.Account, body map[string]any) (*http.Response, error) {
	cred, err := s.credential(acc)
	if err != nil {
		return nil, err
	}
	base := "https://api.openai.com"
	if acc.BaseURL != nil && *acc.BaseURL != "" {
		base = strings.TrimRight(*acc.BaseURL, "/")
	}
	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+cred)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("User-Agent", "kleinai/1.0")
	proxyURL, _, perr := s.resolveProxyURL(ctx, acc, nil, false)
	if perr != nil {
		logger.FromCtx(ctx).Warn("chat.openai.resolve_proxy", zap.Error(perr))
	}
	if proxyURL == "" {
		return s.client.Do(httpReq)
	}
	client, err := outbound.NewClient(outbound.Options{Timeout: 10 * time.Minute, ProxyURL: proxyURL, Mode: outbound.ModeUTLS, Profile: outbound.ProfileChrome})
	if err != nil {
		return nil, err
	}
	return client.Do(httpReq)
}

func (s *ChatService) resolveProxyURL(ctx context.Context, acc *model.Account, exclude map[uint64]struct{}, allowPoolFallback bool) (string, uint64, error) {
	return resolveAccountProxyURL(ctx, s.proxySvc, s.cfg, acc, exclude, allowPoolFallback)
}

func (s *ChatService) credential(acc *model.Account) (string, error) {
	if s.aes == nil || len(acc.CredentialEnc) == 0 {
		return "", fmt.Errorf("chat account missing credential")
	}
	plain, err := s.aes.Decrypt(acc.CredentialEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt credential failed")
	}
	return string(plain), nil
}

func (s *ChatService) estimateCost(modelCode string, body map[string]any) int64 {
	price := s.priceFn(modelCode)
	promptTokens := estimatePromptTokens(body)
	maxTokens := intAny(body["max_tokens"], 1000)
	if maxTokens <= 0 {
		maxTokens = 1000
	}
	return ChatCost(price, promptTokens, maxTokens)
}

// externalChatProvider 判断某文字模型是否应走「通用 OpenAI 兼容上游」转发
// （凭证存在 account 表，auth_type=api_key + base_url，如 newapi 这类聚合网关）。
//
// 约定：billing.model_prices 中 kind=text 且 provider 既不是 "gpt" 也不是 "grok"
// 的启用条目，即视为外部转发型，返回其 provider 作为号池 key。这样可以让
// gpt-5.4 这类与内置 Codex/Grok 路由同名的模型，按配置改走外部上游而不冲突。
// 返回 "" 表示沿用内置路由（Grok Web / Codex OAuth / 默认 GPT 池）。
func (s *ChatService) externalChatProvider(modelCode string) string {
	if s.cfg == nil {
		return ""
	}
	raw := s.cfg.GetString(context.Background(), "billing.model_prices", "")
	if raw == "" {
		return ""
	}
	var rows []struct {
		ModelCode string `json:"model_code"`
		Kind      string `json:"kind"`
		Provider  string `json:"provider"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return ""
	}
	for _, row := range rows {
		if row.ModelCode != modelCode {
			continue
		}
		if row.Enabled != nil && !*row.Enabled {
			return ""
		}
		if row.Kind != "" && row.Kind != "text" {
			return ""
		}
		switch p := strings.TrimSpace(row.Provider); p {
		case "", model.ProviderGPT, model.ProviderGROK:
			return ""
		default:
			return p
		}
	}
	return ""
}

func (s *ChatService) upstreamModel(modelCode string) string {
	if s.cfg == nil {
		return modelCode
	}
	raw := s.cfg.GetString(context.Background(), "billing.model_prices", "")
	if raw == "" {
		return modelCode
	}
	var rows []struct {
		ModelCode     string `json:"model_code"`
		UpstreamModel string `json:"upstream_model"`
		Enabled       *bool  `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return modelCode
	}
	for _, row := range rows {
		if row.ModelCode == modelCode && row.UpstreamModel != "" {
			if row.Enabled != nil && !*row.Enabled {
				return modelCode
			}
			return row.UpstreamModel
		}
	}
	return modelCode
}

func (s *ChatService) fail(ctx context.Context, t *model.GenerationTask, reason string) {
	_ = s.repo.SetFailed(ctx, t.TaskID, reason)
	_ = s.billing.FailRefund(ctx, t.TaskID, reason)
}

// recordChatCost 在 chat 成功路径写一条 task_cost_log。
// usage 为 nil（mock 路径无 usage 时）也会落账，UnitQty 默认 1。
func (s *ChatService) recordChatCost(ctx context.Context, t *model.GenerationTask, acc *model.Account, modelCode string, usage *ChatUsage, actualPoints int64) {
	if s == nil || s.cost == nil || t == nil {
		return
	}
	req := CostRecordReq{
		RefType:    model.CostRefChat,
		RefID:      t.TaskID,
		UserID:     t.UserID,
		ModelCode:  modelCode,
		Provider:   t.Provider,
		Kind:       "chat",
		UnitQty:    1,
		SalePoints: actualPoints,
	}
	if acc != nil {
		req.AccountID = acc.ID
	}
	if usage != nil {
		req.Tokens = TokenUsage{InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens}
	}
	s.cost.Record(ctx, req)
}

func parseUsage(raw []byte) *ChatUsage {
	var obj struct {
		Usage *ChatUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	return obj.Usage
}

func parseStreamUsage(raw []byte) *ChatUsage {
	var obj struct {
		Usage *ChatUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Usage == nil || obj.Usage.TotalTokens == 0 {
		return nil
	}
	return obj.Usage
}

func chatPrompt(body map[string]any) string {
	if v, ok := body["messages"]; ok {
		b, _ := json.Marshal(v)
		s := string(b)
		if len(s) > 4000 {
			return s[:4000]
		}
		return s
	}
	return ""
}

func estimatePromptTokens(body map[string]any) int {
	p := chatPrompt(body)
	if p == "" {
		return 1
	}
	return len([]rune(p))/4 + 1
}

func strAny(v any, def string) string {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return def
}

func intAny(v any, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return def
	}
}

func isLiveProvider(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "real", "live", "prod":
		return true
	default:
		return false
	}
}

func chatTaskID() string {
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	if len(id) > 26 {
		return id[:26]
	}
	return id
}

func snippet(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	r := []rune(string(b))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "...(truncated)"
}
