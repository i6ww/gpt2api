package dto

type AdminGenerationLogListReq struct {
	Keyword  string `form:"keyword" binding:"omitempty,max=128"`
	Kind     string `form:"kind" binding:"omitempty,oneof=image video chat text"`
	Status   *int   `form:"status" binding:"omitempty,oneof=0 1 2 3 4"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

type AdminGenerationLogResp struct {
	TaskID      string `json:"task_id"`
	CreatedAt   int64  `json:"created_at"`
	UserID      uint64 `json:"user_id"`
	UserLabel   string `json:"user_label"`
	APIKeyID    uint64 `json:"api_key_id,omitempty"`
	KeyLabel    string `json:"key_label,omitempty"`
	Kind        string `json:"kind"`
	ModelCode   string `json:"model_code"`
	Prompt      string `json:"prompt"`
	Status      int8   `json:"status"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	CostPoints  int64  `json:"cost_points"`
	Resolution  string `json:"resolution,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	// PreviewURL 是用来在列表小缩略图位置展示的链接：
	//   - 图片任务：就是图片本体（数据库 url，通常 png/jpg）
	//   - 视频任务：是首帧的 _thumb.jpg
	// 前端 <img src={preview_url}> 一加载就行，不用切换 tag。
	PreviewURL string `json:"preview_url,omitempty"`
	// AssetURL 是真正的"主资源"链接，用来点开/下载/播放：
	//   - 图片任务：和 preview_url 通常是同一个 png/jpg
	//   - 视频任务：mp4 主文件
	// 前端「查看」按钮应该用这个，而不是 preview_url。
	AssetURL string `json:"asset_url,omitempty"`
	Error    string `json:"error,omitempty"`
}

type AdminGenerationLogPurgeReq struct {
	// Days 删除多少天前的记录。允许 0 = 删除所有（不限时间），但 0 时强制要求 Status 非空，
	// 防止运营误点「删全部」把成功记录也清掉。
	Days int `json:"days" binding:"min=0,max=3650"`
	// Status 可选；指定后只删该 status 的记录（典型用法：3=失败、4=已退款）。
	// 不传 = 全部状态都删（仅在 days >= 1 时允许）。
	Status *int `json:"status" binding:"omitempty,oneof=0 1 2 3 4"`
}

type AdminGenerationLogPurgeResp struct {
	Deleted int64 `json:"deleted"`
}

type AdminGenerationStuckCleanupReq struct {
	// MinAgeMinutes 只清理超过该分钟数仍处于生成中的任务，避免误杀正常长任务。
	MinAgeMinutes int `json:"min_age_minutes" binding:"omitempty,min=1,max=1440"`
}

type AdminGenerationStuckCleanupResp struct {
	Cleaned int `json:"cleaned"`
}

type AdminGenerationUpstreamLogResp struct {
	ID              uint64  `json:"id"`
	TaskID          string  `json:"task_id"`
	Provider        string  `json:"provider"`
	AccountID       *uint64 `json:"account_id,omitempty"`
	Stage           string  `json:"stage"`
	Method          string  `json:"method,omitempty"`
	URL             string  `json:"url,omitempty"`
	StatusCode      int     `json:"status_code"`
	DurationMs      int64   `json:"duration_ms"`
	RequestExcerpt  string  `json:"request_excerpt,omitempty"`
	ResponseExcerpt string  `json:"response_excerpt,omitempty"`
	Error           string  `json:"error,omitempty"`
	Meta            string  `json:"meta,omitempty"`
	CreatedAt       int64   `json:"created_at"`
}
