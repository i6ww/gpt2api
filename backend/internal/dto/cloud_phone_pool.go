package dto

// CloudPhoneCreateReq 新增 GeeLark 云手机
//
// country_code + phone_number 是该云手机绑定的 WhatsApp 号 = GoPay 钱包手机号；
// 一台云手机就锁死一个手机号，钱包侧只关联 cloud_phone_id 即可。
type CloudPhoneCreateReq struct {
	ID          string `json:"id" binding:"required,min=1,max=64"` // GeeLark phone_id
	Name        string `json:"name" binding:"omitempty,max=128"`
	GLToken     string `json:"gl_token" binding:"required,min=8,max=512"`
	ADBAddr     string `json:"adb_addr" binding:"omitempty,max=128"`
	PreferAPI   *int8  `json:"prefer_api" binding:"omitempty,oneof=0 1"`
	CountryCode string `json:"country_code" binding:"omitempty,max=8"`   // 默认 62
	PhoneNumber string `json:"phone_number" binding:"omitempty,max=32"` // GoPay/WhatsApp 号
	Remark      string `json:"remark" binding:"omitempty,max=255"`
}

// CloudPhoneUpdateReq 编辑云手机
type CloudPhoneUpdateReq struct {
	Name        *string `json:"name" binding:"omitempty,max=128"`
	GLToken     *string `json:"gl_token" binding:"omitempty,min=8,max=512"` // 留空=不动
	ADBAddr     *string `json:"adb_addr" binding:"omitempty,max=128"`
	PreferAPI   *int8   `json:"prefer_api" binding:"omitempty,oneof=0 1"`
	CountryCode *string `json:"country_code" binding:"omitempty,max=8"`
	PhoneNumber *string `json:"phone_number" binding:"omitempty,max=32"`
	Status      *string `json:"status" binding:"omitempty,oneof=online offline banned disabled"`
	Remark      *string `json:"remark" binding:"omitempty,max=255"`
}

// CloudPhoneListReq 列表过滤
type CloudPhoneListReq struct {
	Status   string `form:"status" binding:"omitempty,oneof=online offline banned disabled"`
	Keyword  string `form:"keyword" binding:"omitempty,max=64"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// CloudPhoneBatchDeleteReq 批量删除
type CloudPhoneBatchDeleteReq struct {
	IDs []string `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// CloudPhoneResp 云手机响应（不返回明文 token）
type CloudPhoneResp struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	HasGLToken  bool   `json:"has_gl_token"`
	ADBAddr     string `json:"adb_addr,omitempty"`
	PreferAPI   int8   `json:"prefer_api"`
	CountryCode string `json:"country_code"`
	PhoneNumber string `json:"phone_number,omitempty"`
	PhoneMasked string `json:"phone_masked,omitempty"` // 中间打码用于列表展示
	Status      string `json:"status"`
	LastCheckAt int64  `json:"last_check_at,omitempty"`
	LastCheckOK int8   `json:"last_check_ok"`
	LastError   string `json:"last_error,omitempty"`
	Remark      string `json:"remark,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// CloudPhoneTestResp 测试结果
type CloudPhoneTestResp struct {
	OK    bool   `json:"ok"`
	Echo  string `json:"echo,omitempty"` // shell echo ok 的回显，证明 API 通
	Error string `json:"error,omitempty"`
}

// CloudPhoneBulkOpResult 批量操作结果
type CloudPhoneBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// CloudPhoneGopayUnlinkReq 触发云手机内 GoPay 移除「已连接应用」中的 OpenAI（uiautomator + 模拟点击）。
type CloudPhoneGopayUnlinkReq struct {
	// AppPackage Android 包名；空=默认 com.gojek.app；"-"=不自动拉起（假定已在 GoPay 前台）。
	AppPackage string `json:"app_package" binding:"omitempty,max=128"`
}

// CloudPhoneImportReq 批量导入云手机
//
// 支持 JSON 数组 / 一行一个 phone_id|gl_token|adb_addr 的 text 格式。
// 服务端按行解析，pid 已存在则更新 token。
type CloudPhoneImportReq struct {
	Text   string `json:"text" binding:"omitempty"`
	Items  []CloudPhoneCreateReq `json:"items" binding:"omitempty,dive"`
}

// CloudPhoneImportResult 导入结果
type CloudPhoneImportResult struct {
	Imported int      `json:"imported"`
	Updated  int      `json:"updated"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}
