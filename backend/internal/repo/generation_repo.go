// Package repo 生成任务仓储。
package repo

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// skipLockedClause 返回 MySQL 8.0 的 `FOR UPDATE SKIP LOCKED` 子句；多 agent 同时
// 抢任务时让 InnoDB 自动跳过被别人锁住的行，避免阻塞。
func skipLockedClause() clause.Locking {
	return clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}
}

// ClaimAttemptHardCap 单条 task 被 ClaimBatch 重复抢的硬上限。
//
// 设计要点：
//   - 正常路径：runTask 跑完会显式 SetSucceeded/SetFailed，task 进入终态，
//     attempt 不会无限增长。典型值 1-3。
//   - 异常路径（runTask 静默 return / worker 进程 panic / lease 过期未收尾）：
//     ClaimBatch 会反复抢同一行，attempt 持续 +1，最终把用户卡在「待处理」越久。
//   - 这里设 6：一个任务被反复 reclaim 6 次（每次 lease ~5min）仍未出终态，
//     基本可判定为上游卡死（如 Adobe job 永久 in-progress）或调度异常，
//     再抢也是浪费号位，命中后 ClaimBatch 不再 pickup，由后台 ReapStaleTasks
//     周期扫并 SetFailed 退款。降低此值是为了让卡死任务尽快释放号池/worker 槽位，
//     保障高并发下整体吞吐稳定（避免僵尸任务空转蚕食容量）。
//   - 注意 model.GenerationTask.Attempt 是 int8（DB tinyint unsigned），最大 127。
const ClaimAttemptHardCap = 6

// GenerationRepo 生成任务仓储。
type GenerationRepo struct{ db *gorm.DB }

type AdminGenerationLogFilter struct {
	Keyword  string
	Kind     string
	Status   *int
	Page     int
	PageSize int
}

type AdminGenerationLogRow struct {
	TaskID     string    `gorm:"column:task_id"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UserID     uint64    `gorm:"column:user_id"`
	UserLabel  string    `gorm:"column:user_label"`
	APIKeyID   *uint64   `gorm:"column:api_key_id"`
	KeyLabel   *string   `gorm:"column:key_label"`
	Kind       string    `gorm:"column:kind"`
	ModelCode  string    `gorm:"column:model_code"`
	Prompt     string    `gorm:"column:prompt"`
	Params     string    `gorm:"column:params"`
	Status     int8      `gorm:"column:status"`
	DurationMs *int64    `gorm:"column:duration_ms"`
	CostPoints int64     `gorm:"column:cost_points"`
	PreviewURL *string   `gorm:"column:preview_url"` // 列表用的小图：图片 = 主图本身，视频 = 首帧 _thumb.jpg
	AssetURL   *string   `gorm:"column:asset_url"`   // 主资源：视频任务 = mp4 本体（preview 是首帧图，不是 mp4）
	ResultMeta *string   `gorm:"column:result_meta"`
	Width      *int      `gorm:"column:width"`
	Height     *int      `gorm:"column:height"`
	Error      *string   `gorm:"column:error"`
}

type AdminGenerationUpstreamLogRow struct {
	ID              uint64
	TaskID          string
	Provider        string
	AccountID       *uint64
	Stage           string
	Method          *string
	URL             *string
	StatusCode      int
	DurationMs      int64
	RequestExcerpt  *string
	ResponseExcerpt *string
	Error           *string
	Meta            *string
	CreatedAt       time.Time
}

// NewGenerationRepo 构造。
func NewGenerationRepo(db *gorm.DB) *GenerationRepo { return &GenerationRepo{db: db} }

func (r *GenerationRepo) CreateUpstreamLog(ctx context.Context, log *model.GenerationUpstreamLog) error {
	if log == nil || log.TaskID == "" {
		return nil
	}
	return r.db.WithContext(ctx).Create(log).Error
}

// CountActiveByAPIKey 返回某个 API Key 当前积压/执行中的任务数量。
// 用于 OpenAI 兼容入口的 per-key 队列保护：防止单 key 堆积大量 pending
// 或 running 任务，拖垮 ClaimBatch / DB / 上游账号池。
func (r *GenerationRepo) CountActiveByAPIKey(ctx context.Context, keyID uint64) (pending int64, running int64, err error) {
	if keyID == 0 {
		return 0, 0, nil
	}
	type row struct {
		Status int8
		Count  int64
	}
	var rows []row
	err = r.db.WithContext(ctx).
		Model(&model.GenerationTask{}).
		Select("status, COUNT(*) AS count").
		Where("deleted_at IS NULL").
		Where("from_api_key_id = ?", keyID).
		Where("status IN ?", []int8{model.GenStatusPending, model.GenStatusRunning}).
		Group("status").
		Find(&rows).Error
	if err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		switch r.Status {
		case model.GenStatusPending:
			pending = r.Count
		case model.GenStatusRunning:
			running = r.Count
		}
	}
	return pending, running, nil
}

// CountGlobalActive 返回全站 pending + running 任务总数（用于全局并发准入）。
func (r *GenerationRepo) CountGlobalActive(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&model.GenerationTask{}).
		Where("deleted_at IS NULL").
		Where("status IN ?", []int8{model.GenStatusPending, model.GenStatusRunning}).
		Count(&n).Error
	return n, err
}

func (r *GenerationRepo) ListUpstreamLogs(ctx context.Context, taskID string) ([]*AdminGenerationUpstreamLogRow, error) {
	var rows []*AdminGenerationUpstreamLogRow
	err := r.db.WithContext(ctx).Table("generation_upstream_log").
		Where("task_id = ?", taskID).
		Order("id ASC").
		Find(&rows).Error
	return rows, err
}

func (r *GenerationRepo) ListAdminLogs(ctx context.Context, f AdminGenerationLogFilter) ([]*AdminGenerationLogRow, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}

	where := []string{"t.deleted_at IS NULL"}
	args := []any{}
	if f.Kind != "" {
		where = append(where, "t.kind = ?")
		args = append(args, f.Kind)
	}
	if f.Status != nil {
		where = append(where, "t.status = ?")
		args = append(args, *f.Status)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		where = append(where, `(t.task_id = ? OR CAST(t.user_id AS CHAR) = ? OR t.model_code LIKE ? OR u.email LIKE ? OR u.phone LIKE ? OR u.username LIKE ? OR k.name LIKE ? OR k.last4 = ?)`)
		args = append(args, kw, kw, like, like, like, like, like, kw)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	countSQL := `SELECT COUNT(1)
FROM generation_task t
LEFT JOIN ` + "`user`" + ` u ON u.id = t.user_id
LEFT JOIN api_key k ON k.id = t.from_api_key_id
WHERE ` + whereSQL
	if err := r.db.WithContext(ctx).Raw(countSQL, args...).Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, (f.Page-1)*f.PageSize, f.PageSize)
	querySQL := `SELECT
  t.task_id,
  t.created_at,
  t.user_id,
  COALESCE(NULLIF(u.username, ''), NULLIF(u.email, ''), NULLIF(u.phone, ''), CONCAT('用户 #', t.user_id)) AS user_label,
  t.from_api_key_id AS api_key_id,
  CASE WHEN k.id IS NULL THEN NULL ELSE CONCAT(k.name, ' · ', k.prefix, '…', k.last4) END AS key_label,
  t.kind,
  t.model_code,
  t.prompt,
  t.params,
  t.status,
  CASE
    WHEN t.started_at IS NULL THEN NULL
    ELSE TIMESTAMPDIFF(MICROSECOND, t.started_at, COALESCE(t.finished_at, t.updated_at)) DIV 1000
  END AS duration_ms,
  t.cost_points,
  (SELECT COALESCE(r.thumb_url, r.url) FROM generation_result r WHERE r.task_id = t.task_id AND r.deleted_at IS NULL ORDER BY r.seq ASC, r.id ASC LIMIT 1) AS preview_url,
  (SELECT r.url FROM generation_result r WHERE r.task_id = t.task_id AND r.deleted_at IS NULL ORDER BY r.seq ASC, r.id ASC LIMIT 1) AS asset_url,
  (SELECT r.meta FROM generation_result r WHERE r.task_id = t.task_id AND r.deleted_at IS NULL ORDER BY r.seq ASC, r.id ASC LIMIT 1) AS result_meta,
  (SELECT r.width FROM generation_result r WHERE r.task_id = t.task_id AND r.deleted_at IS NULL ORDER BY r.seq ASC, r.id ASC LIMIT 1) AS width,
  (SELECT r.height FROM generation_result r WHERE r.task_id = t.task_id AND r.deleted_at IS NULL ORDER BY r.seq ASC, r.id ASC LIMIT 1) AS height,
  t.error
FROM generation_task t
LEFT JOIN ` + "`user`" + ` u ON u.id = t.user_id
LEFT JOIN api_key k ON k.id = t.from_api_key_id
WHERE ` + whereSQL + `
ORDER BY t.id DESC
LIMIT ?, ?`
	var rows []*AdminGenerationLogRow
	if err := r.db.WithContext(ctx).Raw(querySQL, queryArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// SoftDeleteAdminLogsBefore marks generation logs and their result rows as deleted.
//
// status 可选：
//   - nil：不限制状态（清理全部 N 天前的记录，旧行为）
//   - non-nil：只删该 status 的记录（典型 status=3 仅删失败）
func (r *GenerationRepo) SoftDeleteAdminLogsBefore(ctx context.Context, before time.Time, status *int8) (int64, error) {
	now := time.Now().UTC()
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&model.GenerationTask{}).
			Where("deleted_at IS NULL AND created_at < ?", before)
		if status != nil {
			q = q.Where("status = ?", *status)
		}
		var taskIDs []string
		if err := q.Pluck("task_id", &taskIDs).Error; err != nil {
			return err
		}
		if len(taskIDs) == 0 {
			return nil
		}
		taskRes := tx.Model(&model.GenerationTask{}).
			Where("deleted_at IS NULL AND task_id IN ?", taskIDs).
			Update("deleted_at", now)
		if taskRes.Error != nil {
			return taskRes.Error
		}
		deleted = taskRes.RowsAffected
		if err := tx.Table("generation_result").
			Where("deleted_at IS NULL AND task_id IN ?", taskIDs).
			Update("deleted_at", now).Error; err != nil {
			return err
		}
		return nil
	})
	return deleted, err
}

// Create 创建任务。
func (r *GenerationRepo) Create(ctx context.Context, t *model.GenerationTask) error {
	return r.db.WithContext(ctx).Create(t).Error
}

// GetByTaskID 通过 task_id 查询。
func (r *GenerationRepo) GetByTaskID(ctx context.Context, taskID string) (*model.GenerationTask, error) {
	var t model.GenerationTask
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND deleted_at IS NULL", taskID).First(&t).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

// GetByIdem 幂等查询：(user_id, idem_key)。
func (r *GenerationRepo) GetByIdem(ctx context.Context, userID uint64, idem string) (*model.GenerationTask, error) {
	var t model.GenerationTask
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND idem_key = ? AND deleted_at IS NULL", userID, idem).First(&t).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

// ── Cluster lease 调度 ─────────────────────────────────────
// 对应 docs/CLUSTER_OVERVIEW.md §4 数据流 lease/result 部分。

// LeasedTask 调度返回结构。
type LeasedTask struct {
	Task       *model.GenerationTask
	Account    *model.Account // 解密后凭证由 service 层注入；repo 层只把 account 行返回
	LeaseUntil time.Time
}

// ClaimBatch 由 agent (远端 / embedded) 调用：原子地从待跑队列里抢 N 条任务。
//
//	nodeID:    抢锁的节点 id，写入 claim_node_id
//	providers: 该节点能跑的 provider 列表（gpt/grok/adobe/...）
//	leaseTTL:  抢到的任务 lease 时长
//	max:       一次最多抢几条
//
// 返回的任务状态被置为 GenStatusRunning，claim_lease_until=now+leaseTTL。
// 利用 MySQL 8.0 SELECT ... FOR UPDATE SKIP LOCKED 防止并发抢同一行。
func (r *GenerationRepo) ClaimBatch(ctx context.Context, nodeID string, providers []string, leaseTTL time.Duration, max int) ([]*model.GenerationTask, error) {
	if nodeID == "" {
		return nil, errors.New("empty node_id")
	}
	if max <= 0 {
		max = 1
	}
	// 单次认领硬上限。与 EmbeddedAgent.tick 传入的 free(最多 256)配合：
	// 旧值 32 会把高并发部署的认领速率压到 32/tick(64/s)，500ms tick 下
	// 填满 ~100 槽位要好几秒，且和 cluster_embedded 注释里"放大到 256"矛盾。
	// 提到 128：单 tick 即可认领上百任务，配合 max_concurrency=500 与
	// 每账号并发上限，能在 ~1 个 tick 内把可用账号容量打满。
	if max > 128 {
		max = 128
	}
	if leaseTTL <= 0 {
		leaseTTL = 5 * time.Minute
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(leaseTTL)

	var picked []*model.GenerationTask
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1) 抢锁 N 行；只挑：
		//    - 待跑（status=0 + 没人 claim），或
		//    - 已 running 但 lease 已过期（被前一个 worker 抛弃了）
		//
		// 历史 bug：之前还有第 3 分支 `(status=Running AND claim_node_id=nodeID)`
		// 用于"自己重入续 lease"，但 EmbeddedAgent tick 周期是 1.5s、lease TTL 5min，
		// 在 task 正在跑期间下一次 tick 命中第 3 分支 → attempt+=1 → 起新 goroutine
		// 又跑一遍 → 同一 task 写出 N 张图（gpt-image-2 任务在 80s 内写过 7 张）。
		// 续 lease 的正确入口是 ExtendLease（agent 主动调），不该走 ClaimBatch。
		//
		// 硬上限保护（attempt < ClaimAttemptHardCap）：一旦某 task 被 reclaim 超过
		// 这个次数，ClaimBatch 不再碰它，留给 ReapStaleTasks 收尾 SetFailed。
		// 这是兜底防呆 —— 正常 runTask 跑完会显式 SetSucceeded/SetFailed 终止任务，
		// 不会触发这条保护；只有 runTask 静默 return 的异常 case（如老版本 SetRunning
		// bug、worker panic 在 recover 之外）才会让 attempt 持续累加，从而被这里拦住。
		var rows []*model.GenerationTask
		q := tx.Model(&model.GenerationTask{}).
			Where("deleted_at IS NULL").
			Where("attempt < ?", ClaimAttemptHardCap).
			Where(
				"((status = ? AND claim_node_id IS NULL) OR (status = ? AND claim_lease_until IS NOT NULL AND claim_lease_until < ?))",
				model.GenStatusPending,
				model.GenStatusRunning, now,
			).
			Where("provider IN ?", providers).
			Order("id ASC").
			Limit(max).
			Clauses(skipLockedClause())
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(rows))
		taskIDs := make([]string, 0, len(rows))
		for _, t := range rows {
			ids = append(ids, t.ID)
			taskIDs = append(taskIDs, t.TaskID)
		}
		// 2) 写入 claim_*；status -> running；started_at 仅首次设置；
		//    attempt += 1 累计 lease 次数（embedded / 远端 agent 都计入）。
		//    cluster_dispatch.ApplyAgentResult 失败分支会读取 attempt 决定
		//    是否还要放回队列由下一轮 lease 换号重试。
		if err := tx.Model(&model.GenerationTask{}).
			Where("id IN ?", ids).
			Updates(map[string]any{
				"claim_node_id":     nodeID,
				"claim_lease_until": leaseUntil,
				"status":            model.GenStatusRunning,
				"progress":          5,
				"attempt":           gorm.Expr("attempt + 1"),
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.GenerationTask{}).
			Where("id IN ? AND started_at IS NULL", ids).
			Update("started_at", now).Error; err != nil {
			return err
		}
		// 3) 重新读出来确认 claim 写入
		if err := tx.Where("task_id IN ?", taskIDs).
			Order("id ASC").Find(&picked).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return picked, nil
}

// ExtendLease agent 心跳时延长某任务的 lease 防止被回收。
func (r *GenerationRepo) ExtendLease(ctx context.Context, taskID, nodeID string, leaseTTL time.Duration) error {
	if leaseTTL <= 0 {
		leaseTTL = 5 * time.Minute
	}
	leaseUntil := time.Now().UTC().Add(leaseTTL)
	return r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ? AND claim_node_id = ?", taskID, nodeID).
		Update("claim_lease_until", leaseUntil).Error
}

// ReleaseClaim agent 决定不跑该任务时回滚（status -> pending；清空 claim_*）。
func (r *GenerationRepo) ReleaseClaim(ctx context.Context, taskID, nodeID string) error {
	return r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ? AND claim_node_id = ? AND status = ?", taskID, nodeID, model.GenStatusRunning).
		Updates(map[string]any{
			"claim_node_id":     nil,
			"claim_lease_until": nil,
			"status":            model.GenStatusPending,
			"started_at":        nil,
			"progress":          0,
		}).Error
}

// ReclaimExpired 周期性回收：所有 status=running 但 claim_lease_until < now 的任务回到待跑。
// 返回回收行数；调度器周期性调用。
func (r *GenerationRepo) ReclaimExpired(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("status = ? AND claim_lease_until IS NOT NULL AND claim_lease_until < ?", model.GenStatusRunning, now).
		Updates(map[string]any{
			"claim_node_id":     nil,
			"claim_lease_until": nil,
			"status":            model.GenStatusPending,
			"progress":          0,
		})
	return res.RowsAffected, res.Error
}

// CountInflight 返回某节点当前持有的 inflight 任务数（用于心跳展示）。
func (r *GenerationRepo) CountInflight(ctx context.Context, nodeID string) (int64, error) {
	var c int64
	err := r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("claim_node_id = ? AND status = ?", nodeID, model.GenStatusRunning).
		Count(&c).Error
	return c, err
}

// SetRunning 标记任务进入运行态。兼容两条路径：
//
//  1. inline 首跑：任务还在 Pending 且没人 claim，把它推到 Running 并写 account_id。
//  2. claim-batch 路径：embedded / 远端 agent 通过 ClaimBatch 把状态从 Pending 改成
//     Running 并写了 claim_node_id；之后才进 runTask 调本函数。此时只更新 account_id
//     即可（status/started_at 已是 Running / 已写过）。
//
// 返回值：err 仅代表 DB 错误；rows=0 表示该任务既不是 Pending、也不是当前进程能继续
// 跑的 Running（典型：已被另一个节点 claim 走、或已经 Succeeded/Failed），调用方应让出。
//
// 历史 bug：之前 WHERE 只允许 `status=Pending AND claim_node_id IS NULL`，让
// ClaimBatch（attempt+=1, status→Running, claim_node_id=本机）路径下 runTask 调本函数
// 永远拿 rows=0，runTask 静默 return 不 SetFailed，任务卡 Pending 直到 attempt 跑到
// int8 上限或被 ReapStaleTasks 收尸 —— 用户看到的 "待处理 N min" 越拖越久就来自这里。
func (r *GenerationRepo) SetRunning(ctx context.Context, taskID string, accountID uint64) (rows int64, err error) {
	now := time.Now().UTC()
	// 路径 1：Pending → Running
	res := r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ? AND status = ? AND claim_node_id IS NULL", taskID, model.GenStatusPending).
		Updates(map[string]any{
			"status":     model.GenStatusRunning,
			"account_id": accountID,
			"started_at": now,
			"progress":   5,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected > 0 {
		return res.RowsAffected, nil
	}
	// 路径 2：已是 Running（ClaimBatch 已经把状态推过去了），只更新 account_id。
	// 不再硬卡 claim_node_id，因为 runTask 与 ClaimBatch 不共享 nodeID 上下文；
	// 上层 EmbeddedAgent 已经保证 runOne 只被自己 claim 的 task 调到，足够防双跑。
	res2 := r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ? AND status = ?", taskID, model.GenStatusRunning).
		Updates(map[string]any{
			"account_id": accountID,
			"progress":   5,
		})
	return res2.RowsAffected, res2.Error
}

// SetRunningClaim 等价 SetRunning 但不要求 status=Pending 前置（ClaimBatch 已 running）。
// 仅写 account_id（started_at 由 ClaimBatch 写过）。
func (r *GenerationRepo) SetRunningClaim(ctx context.Context, taskID string, accountID uint64) error {
	return r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ?", taskID).
		Update("account_id", accountID).Error
}

// UpdateProgress 更新进度（0-100）。
func (r *GenerationRepo) UpdateProgress(ctx context.Context, taskID string, progress int8) error {
	return r.UpdatePollState(ctx, taskID, int(progress), 0)
}

// UpdatePollState 更新任务轮询进度与上游 Retry-After（秒）。progress < 0 表示不更新进度。
func (r *GenerationRepo) UpdatePollState(ctx context.Context, taskID string, progress, retryAfterSec int) error {
	if taskID == "" {
		return errors.New("empty task_id")
	}
	updates := map[string]any{}
	if progress >= 0 {
		if progress > 100 {
			progress = 100
		}
		updates["progress"] = int8(progress)
	}
	if retryAfterSec > 0 {
		if retryAfterSec > 255 {
			retryAfterSec = 255
		}
		updates["poll_retry_after"] = int8(retryAfterSec)
	}
	if len(updates) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ?", taskID).Updates(updates).Error
}

// UpdateCostPoints 把 task.cost_points 改成新值（用于通道降级退款后同步账面）。
func (r *GenerationRepo) UpdateCostPoints(ctx context.Context, taskID string, points int64) error {
	if taskID == "" {
		return errors.New("empty task_id")
	}
	return r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ?", taskID).
		Update("cost_points", points).Error
}

// SetSucceeded 任务成功 + 写入结果。
//
// 幂等保护：状态推到 Succeeded 时 WHERE 卡 `status != Succeeded`，rows=0 表示
// 已经被另一个 goroutine（典型：ClaimBatch 误重入 + 双 runTask 同时跑成功）
// 写过一次了，本次直接 return，不再写 generation_result，避免同一 task 出现
// N 张重复图（曾出过 80s 内同一 task 写出 7 张 3840×2160 的事故）。
func (r *GenerationRepo) SetSucceeded(ctx context.Context, taskID string, results []*model.GenerationResult) error {
	if taskID == "" {
		return errors.New("empty task_id")
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.GenerationTask{}).
			Where("task_id = ? AND status <> ?", taskID, model.GenStatusSucceeded).
			Updates(map[string]any{
				"status":      model.GenStatusSucceeded,
				"progress":    100,
				"finished_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 已经是终态（双跑场景，另一个 goroutine 抢先写过）。直接放弃 results 避免重复落库。
			return nil
		}
		if len(results) > 0 {
			return tx.CreateInBatches(results, 100).Error
		}
		return nil
	})
}

// SetFailed 任务失败。
func (r *GenerationRepo) SetFailed(ctx context.Context, taskID, reason string) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ?", taskID).
		Updates(map[string]any{
			"status":      model.GenStatusFailed,
			"error":       truncateStr(reason, 240),
			"finished_at": now,
		}).Error
}

// UpdateCost updates final task cost after usage-based billing.
func (r *GenerationRepo) UpdateCost(ctx context.Context, taskID string, cost int64) error {
	if cost < 0 {
		cost = 0
	}
	return r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("task_id = ?", taskID).
		Update("cost_points", cost).Error
}

// ListByUser 用户任务列表。
func (r *GenerationRepo) ListByUser(ctx context.Context, userID uint64, kind string, page, pageSize int) ([]*model.GenerationTask, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.GenerationTask{}).
		Where("user_id = ? AND deleted_at IS NULL", userID)
	if kind == "media" {
		q = q.Where("kind IN ?", []string{"image", "video", "music"})
	} else if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var ids []uint64
	if err := q.Select("id").Order("id DESC").Offset((page-1)*pageSize).Limit(pageSize).Pluck("id", &ids).Error; err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return []*model.GenerationTask{}, total, nil
	}
	var items []*model.GenerationTask
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	order := make(map[uint64]int, len(ids))
	for i, id := range ids {
		order[id] = i
	}
	sort.SliceStable(items, func(i, j int) bool { return order[items[i].ID] < order[items[j].ID] })
	return items, total, nil
}

// ListResultsByTask 查询结果列表。
func (r *GenerationRepo) ListResultsByTask(ctx context.Context, taskID string) ([]*model.GenerationResult, error) {
	var items []*model.GenerationResult
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND deleted_at IS NULL", taskID).Order("seq ASC, id ASC").Find(&items).Error
	return items, err
}

// GetResultByTaskSeq returns one result row by task and sequence.
func (r *GenerationRepo) GetResultByTaskSeq(ctx context.Context, taskID string, seq int) (*model.GenerationResult, error) {
	var item model.GenerationResult
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND seq = ? AND deleted_at IS NULL", taskID, seq).
		First(&item).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &item, nil
}

func (r *GenerationRepo) UpdateResultMediaMeta(ctx context.Context, taskID string, seq int, width, height *int, sizeBytes *int64) error {
	updates := map[string]any{}
	if width != nil {
		updates["width"] = *width
	}
	if height != nil {
		updates["height"] = *height
	}
	if sizeBytes != nil {
		updates["size_bytes"] = *sizeBytes
	}
	if len(updates) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Table("generation_result").
		Where("task_id = ? AND seq = ? AND deleted_at IS NULL", taskID, seq).
		Updates(updates).Error
}

// SoftDeleteByUser marks a user's generation tasks and results as deleted.
func (r *GenerationRepo) SoftDeleteByUser(ctx context.Context, userID uint64, failedOnly bool) (int64, error) {
	now := time.Now().UTC()
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&model.GenerationTask{}).
			Where("user_id = ? AND deleted_at IS NULL", userID)
		if failedOnly {
			q = q.Where("status = ?", model.GenStatusFailed)
		}
		var taskIDs []string
		if err := q.Pluck("task_id", &taskIDs).Error; err != nil {
			return err
		}
		if len(taskIDs) == 0 {
			return nil
		}
		taskRes := tx.Model(&model.GenerationTask{}).
			Where("user_id = ? AND deleted_at IS NULL AND task_id IN ?", userID, taskIDs).
			Update("deleted_at", now)
		if taskRes.Error != nil {
			return taskRes.Error
		}
		deleted = taskRes.RowsAffected
		return tx.Table("generation_result").
			Where("deleted_at IS NULL AND task_id IN ?", taskIDs).
			Update("deleted_at", now).Error
	})
	return deleted, err
}

// SoftDeleteByUserBefore marks a user's generation tasks before the cutoff as deleted.
func (r *GenerationRepo) SoftDeleteByUserBefore(ctx context.Context, userID uint64, before time.Time) (int64, error) {
	now := time.Now().UTC()
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskIDs []string
		if err := tx.Model(&model.GenerationTask{}).
			Where("user_id = ? AND deleted_at IS NULL AND created_at < ?", userID, before).
			Pluck("task_id", &taskIDs).Error; err != nil {
			return err
		}
		if len(taskIDs) == 0 {
			return nil
		}
		taskRes := tx.Model(&model.GenerationTask{}).
			Where("user_id = ? AND deleted_at IS NULL AND task_id IN ?", userID, taskIDs).
			Update("deleted_at", now)
		if taskRes.Error != nil {
			return taskRes.Error
		}
		deleted = taskRes.RowsAffected
		return tx.Table("generation_result").
			Where("deleted_at IS NULL AND task_id IN ?", taskIDs).
			Update("deleted_at", now).Error
	})
	return deleted, err
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
