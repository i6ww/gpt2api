package dto

// ProxyCreateReq 创建代理
type ProxyCreateReq struct {
	Name     string `json:"name" binding:"required,min=1,max=128"`
	Protocol string `json:"protocol" binding:"required,oneof=http https socks5 socks5h"`
	Host     string `json:"host" binding:"required,min=1,max=255"`
	Port     uint16 `json:"port" binding:"required,min=1,max=65535"`
	Username string `json:"username" binding:"omitempty,max=255"`
	Password string `json:"password" binding:"omitempty,max=255"`
	Remark   string `json:"remark" binding:"omitempty,max=255"`
}

// ProxyUpdateReq 更新代理
type ProxyUpdateReq struct {
	Name     *string `json:"name" binding:"omitempty,min=1,max=128"`
	Protocol *string `json:"protocol" binding:"omitempty,oneof=http https socks5 socks5h"`
	Host     *string `json:"host" binding:"omitempty,min=1,max=255"`
	Port     *uint16 `json:"port" binding:"omitempty,min=1,max=65535"`
	Username *string `json:"username" binding:"omitempty,max=255"`
	Password *string `json:"password" binding:"omitempty,max=255"`
	Status   *int8   `json:"status" binding:"omitempty,oneof=0 1"`
	Remark   *string `json:"remark" binding:"omitempty,max=255"`
}

// ProxyListReq 代理列表过滤
type ProxyListReq struct {
	Status   *int8  `form:"status"`
	Keyword  string `form:"keyword" binding:"omitempty,max=64"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// ProxyBatchDeleteReq 批量删除代理
type ProxyBatchDeleteReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// ProxyBatchTestReq 批量测试代理
type ProxyBatchTestReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// ProxyImportReq 批量导入代理，支持多�?URL
type ProxyImportReq struct {
	Text   string `json:"text" binding:"required"`
	Remark string `json:"remark" binding:"omitempty,max=255"`
}

// ProxyImportResult 批量导入结果
type ProxyImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

// ProxyResp 代理响应
type ProxyResp struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	Host        string `json:"host"`
	Port        uint16 `json:"port"`
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"has_password"`
	Status      int8   `json:"status"`
	LastCheckAt int64  `json:"last_check_at,omitempty"`
	LastCheckOK int8   `json:"last_check_ok"`
	LastCheckMs int    `json:"last_check_ms"`
	LastError   string `json:"last_error,omitempty"`
	Remark      string `json:"remark,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ProxyTestResp 单个代理测试结果
type ProxyTestResp struct {
	OK        bool   `json:"ok"`
	LatencyMs int    `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// ProxyBulkOpResult 批量删除结果
type ProxyBulkOpResult struct {
	Deleted int64 `json:"deleted"`
}

// ProxyBatchTestResp 批量测试结果
type ProxyBatchTestResp struct {
	Tested    int      `json:"tested"`
	Success   int      `json:"success"`
	Failed    int      `json:"failed"`
	FailedIDs []uint64 `json:"failed_ids"`
}
