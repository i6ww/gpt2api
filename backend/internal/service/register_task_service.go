package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/logger"
)

// RegisterDispatcher 实际执行注册流程。每个 provider 一个实现。
//
// 实现方需要保证：
//   - 自行处理超时与取消（task.cancel_requested）
//   - 完成时通过 RegisterTaskService.Finish*** 方法回写状态
type RegisterDispatcher interface {
	// Run 执行一次注册（同步，调用方负责放进 goroutine）。
	// 实现内可读取 task.Payload、task.MailID/Email，并把结果通过 svc 回写。
	Run(ctx context.Context, svc *RegisterTaskService, task *model.RegisterTask) error
}

// taskSubmitter 把 taskID 投到 worker pool 的最小接口（避免循环依赖）。
type taskSubmitter interface {
	Submit(taskID uint64)
}

// RegisterTaskService 号池注册任务服务。
type RegisterTaskService struct {
	repo         *repo.RegisterTaskRepo
	logRepo      *repo.RegisterTaskLogRepo
	mailPoolRepo *repo.MailPoolRepo
	dispatchers  map[string]RegisterDispatcher
	submitter    taskSubmitter
	// providerCache: taskID -> provider，避免每条日志都 SELECT 一次。
	providerCache sync.Map
}

// NewRegisterTaskService 构造。
func NewRegisterTaskService(
	r *repo.RegisterTaskRepo,
	logRepo *repo.RegisterTaskLogRepo,
	mailPoolRepo *repo.MailPoolRepo,
) *RegisterTaskService {
	return &RegisterTaskService{
		repo:         r,
		logRepo:      logRepo,
		mailPoolRepo: mailPoolRepo,
		dispatchers:  make(map[string]RegisterDispatcher),
	}
}

// SetSubmitter 注入 worker pool。注入前 Create 创建的任务会原地起 goroutine（兼容旧路径）。
func (s *RegisterTaskService) SetSubmitter(sub taskSubmitter) { s.submitter = sub }

// RecoverPending 启动时把上次崩溃留下的 running 任务清理掉，
// 把仍在 pending 的任务重新投到 worker pool。
//
// 调用方在 worker pool 启动后调用一次。
func (s *RegisterTaskService) RecoverPending(ctx context.Context) (running int64, requeued int64, err error) {
	now := time.Now().UTC()
	tx := s.repo
	// 1) 上次进程退出时还在 running 的任务：标 failed
	if r, e := s.markRunningAsFailed(ctx, "进程重启时仍在 running，已自动标记失败"); e != nil {
		return 0, 0, e
	} else {
		running = r
	}
	_ = tx
	_ = now
	// 2) pending 任务：重新 Submit
	if s.submitter == nil {
		return running, 0, nil
	}
	pendings, _, e := s.repo.List(ctx, repo.RegisterTaskFilter{
		Status:   model.RegisterTaskPending,
		Page:     1,
		PageSize: 200,
	})
	if e != nil {
		return running, 0, e
	}
	for _, p := range pendings {
		s.submitter.Submit(p.ID)
		requeued++
	}
	return running, requeued, nil
}

// markRunningAsFailed 把所有 running 任务批量标 failed，返回行数。
func (s *RegisterTaskService) markRunningAsFailed(ctx context.Context, reason string) (int64, error) {
	now := time.Now().UTC()
	rows, _, err := s.repo.List(ctx, repo.RegisterTaskFilter{
		Status:   model.RegisterTaskRunning,
		Page:     1,
		PageSize: 1000,
	})
	if err != nil {
		return 0, err
	}
	var n int64
	for _, r := range rows {
		_ = s.repo.Update(ctx, r.ID, map[string]any{
			"status":      model.RegisterTaskFailed,
			"error":       reason,
			"finished_at": now,
		})
		n++
	}
	return n, nil
}

// RegisterDispatcher 注册一个 provider 的执行器。
// 调用方在 router 装配阶段把 adobe / grok / gpt 三个 dispatcher 注册进来即可。
func (s *RegisterTaskService) RegisterDispatcher(provider string, d RegisterDispatcher) {
	s.dispatchers[provider] = d
}

// List 列表。
func (s *RegisterTaskService) List(ctx context.Context, req *dto.RegisterTaskListReq) ([]*dto.RegisterTaskResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.RegisterTaskFilter{
		Provider: req.Provider,
		Status:   req.Status,
		Keyword:  strings.TrimSpace(req.Keyword),
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.RegisterTaskResp, 0, len(items))
	for _, it := range items {
		out = append(out, registerTaskToResp(it))
	}
	return out, total, nil
}

// Get 详情。
func (s *RegisterTaskService) Get(ctx context.Context, id uint64) (*dto.RegisterTaskResp, error) {
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errcode.ResourceMissing
		}
		return nil, errcode.DBError.Wrap(err)
	}
	return registerTaskToResp(m), nil
}

// Stats 状态分布。
func (s *RegisterTaskService) Stats(ctx context.Context, provider string) (*dto.RegisterTaskStatsResp, error) {
	m, err := s.repo.Stats(ctx, provider)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return &dto.RegisterTaskStatsResp{
		Total:     m["total"],
		Pending:   m["pending"],
		Running:   m["running"],
		Success:   m["success"],
		Failed:    m["failed"],
		Cancelled: m["cancelled"],
	}, nil
}

// Create 创建一个或多个注册任务。任务创建后立即异步执行。
func (s *RegisterTaskService) Create(ctx context.Context, req *dto.RegisterTaskCreateReq, createdBy uint64) (*dto.RegisterTaskCreateResp, error) {
	count := req.Count
	if count <= 0 {
		count = 1
	}
	if count > 5000 {
		count = 5000
	}
	if req.MailID != nil && count != 1 {
		return nil, errcode.InvalidParam.Wrap(errors.New("指定 mail_id 时只能创建 1 个任务"))
	}
	payloadBytes, err := json.Marshal(req.Payload)
	if err != nil {
		return nil, errcode.InvalidParam.Wrap(err)
	}
	resp := &dto.RegisterTaskCreateResp{IDs: make([]uint64, 0, count)}
	for i := 0; i < count; i++ {
		t := &model.RegisterTask{
			Provider: req.Provider,
			Status:   model.RegisterTaskPending,
			Payload:  payloadBytes,
		}
		if createdBy > 0 {
			t.CreatedBy = &createdBy
		}
		if req.MailID != nil {
			t.MailID = req.MailID
		}
		if err := s.repo.Create(ctx, t); err != nil {
			return nil, errcode.DBError.Wrap(err)
		}
		resp.IDs = append(resp.IDs, t.ID)
		if s.submitter != nil {
			s.submitter.Submit(t.ID)
		} else {
			go s.RunTask(context.Background(), t.ID)
		}
	}
	resp.Created = len(resp.IDs)
	return resp, nil
}

// Cancel 请求取消任务。worker 看到 cancel_requested 后会自行退出。
// 对于 pending 状态会直接置 cancelled。
func (s *RegisterTaskService) Cancel(ctx context.Context, id uint64) error {
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}
	switch m.Status {
	case model.RegisterTaskPending:
		now := time.Now().UTC()
		if err := s.repo.Update(ctx, id, map[string]any{
			"status":      model.RegisterTaskCancelled,
			"finished_at": now,
		}); err != nil {
			return errcode.DBError.Wrap(err)
		}
		return nil
	case model.RegisterTaskRunning:
		if err := s.repo.MarkCancelRequested(ctx, id); err != nil {
			return errcode.DBError.Wrap(err)
		}
		return nil
	default:
		return errcode.InvalidParam.Wrap(errors.New("任务已结束，无需取消"))
	}
}

// Delete 软删（已结束的任务可清理）。
func (s *RegisterTaskService) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Purge 批量软删任务。仅清理已结束态（success/failed/cancelled），运行中/排队中任务保留。
// provider 为空表示全部 provider。
func (s *RegisterTaskService) Purge(ctx context.Context, provider string) (int64, error) {
	n, err := s.repo.Purge(ctx, repo.PurgeFilter{Provider: provider})
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// === worker 接口（供 dispatcher 回写状态） ===

// MarkRunning worker 取到任务后调用。
func (s *RegisterTaskService) MarkRunning(ctx context.Context, id uint64, step string) error {
	now := time.Now().UTC()
	if err := s.repo.Update(ctx, id, map[string]any{
		"status":     model.RegisterTaskRunning,
		"step":       step,
		"started_at": now,
	}); err != nil {
		return err
	}
	s.appendLog(ctx, id, model.RegisterLogInfo, step, 0, "任务开始执行")
	return nil
}

// UpdateProgress 推进进度（同步追加一条 info 日志到 register_task_log）。
func (s *RegisterTaskService) UpdateProgress(ctx context.Context, id uint64, step string, progress uint8) error {
	if err := s.repo.Update(ctx, id, map[string]any{
		"step":     step,
		"progress": progress,
	}); err != nil {
		return err
	}
	s.appendLog(ctx, id, model.RegisterLogInfo, step, progress, "")
	return nil
}

// LogInfo / LogWarn / LogError 给 dispatcher 在自由文本时使用。
func (s *RegisterTaskService) LogInfo(ctx context.Context, id uint64, message string) {
	s.appendLog(ctx, id, model.RegisterLogInfo, "", 0, message)
}
func (s *RegisterTaskService) LogWarn(ctx context.Context, id uint64, message string) {
	s.appendLog(ctx, id, model.RegisterLogWarn, "", 0, message)
}
func (s *RegisterTaskService) LogError(ctx context.Context, id uint64, message string) {
	s.appendLog(ctx, id, model.RegisterLogError, "", 0, message)
}

// appendLog 内部：写一条日志，吞掉错误（log 写不进去不能让主流程挂）。
//
// progress=0 不写入 log.progress 字段，避免误以为任务回到 0%。
func (s *RegisterTaskService) appendLog(ctx context.Context, taskID uint64, level, step string, progress uint8, message string) {
	if s.logRepo == nil {
		return
	}
	provider := s.lookupProvider(ctx, taskID)
	row := &model.RegisterTaskLog{
		TaskID:   taskID,
		Provider: provider,
		Level:    level,
	}
	if step != "" {
		s := step
		row.Step = &s
	}
	if progress > 0 {
		p := progress
		row.Progress = &p
	}
	if message != "" {
		// 截断防止超长占用 InnoDB row buffer / 影响日志检索。
		// message 列已升级到 TEXT(64KB)，留余量到 8KB 即可保留绝大部分上下文；
		// 历史 980 截断会把 `Get "<long URL>": <real net error>` 这种错误的
		// "real net error" 部分吃掉，定位 codex chase 链路问题困难。
		if len(message) > 8000 {
			message = message[:8000] + "…"
		}
		row.Message = &message
	}
	_ = s.logRepo.Insert(ctx, row)
}

// lookupProvider 从缓存或 DB 拿 provider。
func (s *RegisterTaskService) lookupProvider(ctx context.Context, taskID uint64) string {
	if v, ok := s.providerCache.Load(taskID); ok {
		if str, ok := v.(string); ok {
			return str
		}
	}
	t, err := s.repo.GetByID(ctx, taskID)
	if err != nil || t == nil {
		return ""
	}
	s.providerCache.Store(taskID, t.Provider)
	return t.Provider
}

// AttachMail 把领取到的邮箱绑定到任务（步骤完成后即写入 email 冗余字段）。
func (s *RegisterTaskService) AttachMail(ctx context.Context, id, mailID uint64, email string) error {
	return s.repo.Update(ctx, id, map[string]any{
		"mail_id": mailID,
		"email":   email,
	})
}

// FinishSuccess 标记成功。
func (s *RegisterTaskService) FinishSuccess(ctx context.Context, id, poolAccountID uint64, result map[string]any) error {
	now := time.Now().UTC()
	resultBytes, _ := json.Marshal(result)
	fields := map[string]any{
		"status":          model.RegisterTaskSuccess,
		"step":            "done",
		"progress":        100,
		"result":          resultBytes,
		"finished_at":     now,
		"pool_account_id": poolAccountID,
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return err
	}
	msg := fmt.Sprintf("注册成功，写入号池行 ID=%d", poolAccountID)
	s.appendLog(ctx, id, model.RegisterLogInfo, "done", 100, msg)
	s.providerCache.Delete(id)
	return nil
}

// FinishFailed 标记失败。
func (s *RegisterTaskService) FinishFailed(ctx context.Context, id uint64, errMsg string) error {
	now := time.Now().UTC()
	short := errMsg
	if len(short) > 480 {
		short = short[:480]
	}
	if err := s.repo.Update(ctx, id, map[string]any{
		"status":      model.RegisterTaskFailed,
		"error":       short,
		"finished_at": now,
	}); err != nil {
		return err
	}
	s.appendLog(ctx, id, model.RegisterLogError, "failed", 0, errMsg)
	s.providerCache.Delete(id)
	return nil
}

// FinishCancelled 标记取消。
func (s *RegisterTaskService) FinishCancelled(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	if err := s.repo.Update(ctx, id, map[string]any{
		"status":      model.RegisterTaskCancelled,
		"finished_at": now,
	}); err != nil {
		return err
	}
	s.appendLog(ctx, id, model.RegisterLogWarn, "cancelled", 0, "任务被取消")
	s.providerCache.Delete(id)
	return nil
}

// LogsList 拉取最近若干条日志，支持按 task_id / provider / level 过滤。
func (s *RegisterTaskService) LogsList(ctx context.Context, taskID uint64, provider, level string, limit int) ([]*model.RegisterTaskLog, error) {
	if s.logRepo == nil {
		return nil, nil
	}
	rows, err := s.logRepo.List(ctx, repo.RegisterTaskLogFilter{
		TaskID:   taskID,
		Provider: provider,
		Level:    level,
		Limit:    limit,
	})
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return rows, nil
}

// LogsPurge 按过滤条件批量清理日志。零值过滤即清空。
func (s *RegisterTaskService) LogsPurge(ctx context.Context, taskID uint64, provider, level string) (int64, error) {
	if s.logRepo == nil {
		return 0, nil
	}
	n, err := s.logRepo.Purge(ctx, repo.RegisterTaskLogFilter{
		TaskID:   taskID,
		Provider: provider,
		Level:    level,
	})
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// MailPoolRepo 暴露给 dispatcher 用于 acquire/mark。
func (s *RegisterTaskService) MailPoolRepo() *repo.MailPoolRepo { return s.mailPoolRepo }

// === 内部 ===

// RunTask 执行单个注册任务。worker pool 直接以此为消费回调。
func (s *RegisterTaskService) RunTask(ctx context.Context, id uint64) {
	log := logger.L().Sugar()
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("register task panic id=%d: %v", id, r)
			_ = s.FinishFailed(context.Background(), id, fmt.Sprintf("panic: %v", r))
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		log.Warnf("register task get failed id=%d err=%v", id, err)
		return
	}
	if m.Status != model.RegisterTaskPending {
		return
	}
	// 在 dispatcher 跑之前预热 provider 缓存，避免 appendLog 多查一次。
	s.providerCache.Store(id, m.Provider)
	d, ok := s.dispatchers[m.Provider]
	if !ok || d == nil {
		_ = s.FinishFailed(ctx, id, "未实现该 provider 的注册 dispatcher")
		return
	}
	if err := s.MarkRunning(ctx, id, "start"); err != nil {
		log.Warnf("register task mark running failed id=%d err=%v", id, err)
	}
	if err := d.Run(ctx, s, m); err != nil {
		_ = s.FinishFailed(ctx, id, err.Error())
	}
}

func registerTaskToResp(m *model.RegisterTask) *dto.RegisterTaskResp {
	r := &dto.RegisterTaskResp{
		ID:              m.ID,
		Provider:        m.Provider,
		Status:          m.Status,
		Progress:        m.Progress,
		CancelRequested: m.CancelRequested,
		CreatedAt:       m.CreatedAt.UnixMilli(),
		UpdatedAt:       m.UpdatedAt.UnixMilli(),
	}
	if m.Step != nil {
		r.Step = *m.Step
	}
	if m.MailID != nil {
		r.MailID = *m.MailID
	}
	if m.Email != nil {
		r.Email = *m.Email
	}
	if m.Error != nil {
		r.Error = *m.Error
	}
	if m.PoolAccountID != nil {
		r.PoolAccountID = *m.PoolAccountID
	}
	if m.StartedAt != nil {
		r.StartedAt = m.StartedAt.UnixMilli()
	}
	if m.FinishedAt != nil {
		r.FinishedAt = m.FinishedAt.UnixMilli()
	}
	if len(m.Payload) > 0 {
		var p map[string]any
		_ = json.Unmarshal(m.Payload, &p)
		r.Payload = p
	}
	if len(m.Result) > 0 {
		var p map[string]any
		_ = json.Unmarshal(m.Result, &p)
		r.Result = p
	}
	return r
}
