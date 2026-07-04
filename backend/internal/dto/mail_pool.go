package dto

// MailPoolListReq ????
type MailPoolListReq struct {
	Status   string `form:"status" binding:"omitempty,oneof=available in_use registered failed disabled"`
	Mode     string `form:"mode" binding:"omitempty,oneof=outlook_imap outlook_graph tempmail cf"`
	Keyword  string `form:"keyword" binding:"omitempty,max=64"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// MailPoolImportReq ???????4 ????email----password----client_id----refresh_token?
type MailPoolImportReq struct {
	Text      string `json:"text" binding:"required"`
	Mode      string `json:"mode" binding:"omitempty,oneof=outlook_imap outlook_graph tempmail cf"`
	Separator string `json:"separator" binding:"omitempty,max=8"`
}

// MailPoolImportResult ??????
type MailPoolImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// MailPoolUpdateReq ????
type MailPoolUpdateReq struct {
	Mode     *string `json:"mode" binding:"omitempty,oneof=outlook_imap outlook_graph tempmail cf"`
	Status   *string `json:"status" binding:"omitempty,oneof=available in_use registered failed disabled"`
	Password *string `json:"password" binding:"omitempty,max=255"`
	ClientID *string `json:"client_id" binding:"omitempty,max=128"`
	Refresh  *string `json:"refresh_token" binding:"omitempty,max=4000"`
}

// MailPoolBatchIDsReq ???? ID ??
type MailPoolBatchIDsReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// MailPoolDeleteByStatusReq ????????? failed / in_use / registered?
type MailPoolDeleteByStatusReq struct {
	Status string `json:"status" binding:"required,oneof=failed in_use registered"`
}

// MailPoolTruncateReq 按当前筛选条件清空（filter 全空 = 清空整张表）。
// 服务端要求 Confirm == "DELETE" 才会真正执行，避免误调用。
type MailPoolTruncateReq struct {
	Confirm string `json:"confirm" binding:"required,eq=DELETE"`
	Status  string `json:"status" binding:"omitempty,oneof=available in_use registered failed disabled"`
	Mode    string `json:"mode" binding:"omitempty,oneof=outlook_imap outlook_graph tempmail cf"`
	Keyword string `json:"keyword" binding:"omitempty,max=64"`
}

// MailPoolBulkOpResult ??????
type MailPoolBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// MailPoolResp ????
type MailPoolResp struct {
	ID              uint64 `json:"id"`
	Email           string `json:"email"`
	ClientID        string `json:"client_id"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	FailureCount    int    `json:"failure_count"`
	LastError       string `json:"last_error,omitempty"`
	UsedByProvider  string `json:"used_by_provider,omitempty"`
	UsedByAccountID uint64 `json:"used_by_account_id,omitempty"`
	ImportedAt      int64  `json:"imported_at"`
	UsedAt          int64  `json:"used_at,omitempty"`
	RegisteredAt    int64  `json:"registered_at,omitempty"`
}

// MailPoolStatsResp ????
type MailPoolStatsResp struct {
	Total      int64 `json:"total"`
	Available  int64 `json:"available"`
	InUse      int64 `json:"in_use"`
	Registered int64 `json:"registered"`
	Failed     int64 `json:"failed"`
	Disabled   int64 `json:"disabled"`
}

// MailPoolCFGenerateReq ?? CF Worker /admin/new_address ??????????????
type MailPoolCFGenerateReq struct {
	Count int `json:"count" binding:"required,min=1,max=200"`
	// EnablePrefix true = worker ??????? tmp ????? true?
	EnablePrefix *bool `json:"enable_prefix"`
	// Domain ????????????? mail.cf.email_domain
	Domain string `json:"domain" binding:"omitempty,max=128"`
	// NameLen ??????4-32???? 12
	NameLen int `json:"name_len" binding:"omitempty,min=4,max=32"`
}

// MailPoolCFGenerateResp ??????
type MailPoolCFGenerateResp struct {
	Generated int      `json:"generated"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
	Samples   []string `json:"samples,omitempty"`
}
