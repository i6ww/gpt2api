package dto

// PaymentProxyCreateReq 新增支付代理
type PaymentProxyCreateReq struct {
	Name     string `json:"name" binding:"omitempty,max=128"`
	Scheme   string `json:"scheme" binding:"required,oneof=http https socks5 socks5h"`
	Host     string `json:"host" binding:"omitempty,max=255"` // 静态：必填；动态：留空
	Port     int    `json:"port" binding:"omitempty,min=0,max=65535"`
	Username string `json:"username" binding:"omitempty,max=128"`
	Password string `json:"password" binding:"omitempty,max=255"`
	APIURL   string `json:"api_url" binding:"omitempty,max=512"`
	Country  string `json:"country" binding:"omitempty,max=8"`
	Remark   string `json:"remark" binding:"omitempty,max=255"`
}

// PaymentProxyUpdateReq 编辑代理
type PaymentProxyUpdateReq struct {
	Name     *string `json:"name" binding:"omitempty,max=128"`
	Scheme   *string `json:"scheme" binding:"omitempty,oneof=http https socks5 socks5h"`
	Host     *string `json:"host" binding:"omitempty,max=255"`
	Port     *int    `json:"port" binding:"omitempty,min=0,max=65535"`
	Username *string `json:"username" binding:"omitempty,max=128"`
	Password *string `json:"password" binding:"omitempty,max=255"`
	APIURL   *string `json:"api_url" binding:"omitempty,max=512"`
	Country  *string `json:"country" binding:"omitempty,max=8"`
	Status   *string `json:"status" binding:"omitempty,oneof=active disabled banned"`
	Remark   *string `json:"remark" binding:"omitempty,max=255"`
}

// PaymentProxyListReq 列表过滤
type PaymentProxyListReq struct {
	Status   string `form:"status" binding:"omitempty,oneof=active disabled banned"`
	Country  string `form:"country" binding:"omitempty,max=8"`
	Keyword  string `form:"keyword" binding:"omitempty,max=64"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// PaymentProxyBatchDeleteReq 批量删除
type PaymentProxyBatchDeleteReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// PaymentProxyResp 响应
type PaymentProxyResp struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	Scheme      string `json:"scheme"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port"`
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"has_password"`
	APIURL      string `json:"api_url,omitempty"`
	Country     string `json:"country"`
	Status      string `json:"status"`
	TotalUsed   int    `json:"total_used"`
	TotalFailed int    `json:"total_failed"`
	LastUsedAt  int64  `json:"last_used_at,omitempty"`
	LastCheckAt int64  `json:"last_check_at,omitempty"`
	LastCheckOK int8   `json:"last_check_ok"`
	LastCheckMs int    `json:"last_check_ms"`
	LastError   string `json:"last_error,omitempty"`
	Remark      string `json:"remark,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// PaymentProxyImportReq 批量导入
//
// 复用 proxy 表的批量导入语义：text 一行一条 URL，scheme://user:pass@host:port
// 或 host:port:user:pass。
type PaymentProxyImportReq struct {
	Text    string `json:"text" binding:"required"`
	Country string `json:"country" binding:"omitempty,max=8"`
	Remark  string `json:"remark" binding:"omitempty,max=255"`
}

// PaymentProxyImportResult 导入结果
type PaymentProxyImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

// PaymentProxyBulkOpResult 批量操作结果
type PaymentProxyBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// PaymentProxyTestResp 测试结果
type PaymentProxyTestResp struct {
	OK        bool   `json:"ok"`
	LatencyMs int    `json:"latency_ms"`
	IP        string `json:"ip,omitempty"`     // 出口 IP
	Country   string `json:"country,omitempty"` // 反查国家
	Error     string `json:"error,omitempty"`
}
