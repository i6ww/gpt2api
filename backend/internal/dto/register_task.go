package dto

// RegisterTaskListReq ????
type RegisterTaskListReq struct {
	Provider string `form:"provider" binding:"omitempty,oneof=adobe grok gpt upgrade_plus"`
	Status   string `form:"status" binding:"omitempty,oneof=pending running success failed cancelled"`
	Keyword  string `form:"keyword" binding:"omitempty,max=128"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// RegisterTaskCreateReq ????
//
// payload ???????? provider ????
//   adobe / grok / gpt ???first_name / last_name / password / proxy_id / count / notes
//   grok ???trial????????
//   gpt ???country
type RegisterTaskCreateReq struct {
	Provider string `json:"provider" binding:"required,oneof=adobe grok gpt upgrade_plus"`
	// MailID ?????? ID?????????? worker ?? acquire
	MailID *uint64 `json:"mail_id" binding:"omitempty,min=1"`
	// Count ???????????????? acquire ????????
	//
	// ?? 5000?
	//   - ??????????? register_task ??? + worker ????
	//   - ???????? system_config.register.worker_concurrency??? 5??
	//     ????"??????? pending ??"?????????????
	//   - ???? 1 ??????? 1000-2000 ???????? 20-32????
	//     ?? + ?????????????? docs?
	Count int `json:"count" binding:"omitempty,min=1,max=5000"`
	// Payload ???? JSON
	Payload map[string]any `json:"payload" binding:"omitempty"`
}

// RegisterTaskCreateResp ????
type RegisterTaskCreateResp struct {
	Created int      `json:"created"`
	IDs     []uint64 `json:"ids"`
}

// RegisterTaskBatchIDsReq ???? ID
type RegisterTaskBatchIDsReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// RegisterTaskBulkOpResult ??????
type RegisterTaskBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// RegisterTaskResp ????
type RegisterTaskResp struct {
	ID              uint64         `json:"id"`
	Provider        string         `json:"provider"`
	Status          string         `json:"status"`
	Step            string         `json:"step,omitempty"`
	Progress        uint8          `json:"progress"`
	MailID          uint64         `json:"mail_id,omitempty"`
	Email           string         `json:"email,omitempty"`
	Payload         map[string]any `json:"payload,omitempty"`
	Result          map[string]any `json:"result,omitempty"`
	Error           string         `json:"error,omitempty"`
	PoolAccountID   uint64         `json:"pool_account_id,omitempty"`
	CancelRequested bool           `json:"cancel_requested"`
	CreatedAt       int64          `json:"created_at"`
	StartedAt       int64          `json:"started_at,omitempty"`
	FinishedAt      int64          `json:"finished_at,omitempty"`
	UpdatedAt       int64          `json:"updated_at"`
}

// RegisterTaskStatsResp ????
type RegisterTaskStatsResp struct {
	Total     int64 `json:"total"`
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Success   int64 `json:"success"`
	Failed    int64 `json:"failed"`
	Cancelled int64 `json:"cancelled"`
}

// RegisterTaskLogListReq ???????
type RegisterTaskLogListReq struct {
	TaskID   uint64 `form:"task_id"`
	Provider string `form:"provider" binding:"omitempty,oneof=adobe grok gpt upgrade_plus"`
	Level    string `form:"level" binding:"omitempty,oneof=info warn error"`
	Limit    int    `form:"limit" binding:"omitempty,min=1,max=10000"`
}

// RegisterTaskLogResp ?????
type RegisterTaskLogResp struct {
	ID        uint64 `json:"id"`
	TaskID    uint64 `json:"task_id"`
	Provider  string `json:"provider,omitempty"`
	Level     string `json:"level"`
	Step      string `json:"step,omitempty"`
	Progress  uint8  `json:"progress,omitempty"`
	Message   string `json:"message,omitempty"`
	CreatedAt int64  `json:"created_at"`
}
