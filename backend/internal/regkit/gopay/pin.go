package gopay

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

// tokenizePIN POST customer.gopayapi.com/api/v1/users/pin/tokens/nb → JWT pin_token。
//
// pinClientIDOverride 用于 charge 阶段（GoPay 后端要求 charge 用 -GWC client id，
// linking 用 -MGUPA client id）。如果传空就用 challenge 自带的 client_id。
//
// 失败码语义：
//
//	400/401/403 → PIN 错 (ErrCodePINRejected)，dispatcher 应标钱包 banned
//	其他       → 通用 fail
func (c *Charger) tokenizePIN(ctx context.Context, challengeID, clientID, pinClientIDOverride string) (string, error) {
	const step = "tokenize_pin"
	body := map[string]any{
		"challenge_id": challengeID,
		"client_id":    pickFirstNonEmpty(pinClientIDOverride, clientID),
		"pin":          c.cfg.Wallet.PIN,
	}
	headers := http.Header{}
	headers.Set("x-appversion", "1.0.0")
	headers.Set("x-correlation-id", uuid.New().String())
	headers.Set("x-is-mobile", "false")
	headers.Set("x-platform", "Mac OS 12.2.1")
	headers.Set("x-request-id", uuid.New().String())
	headers.Set("x-user-locale", "id")
	headers.Set("Origin", "https://pin-web-client.gopayapi.com")
	headers.Set("Referer", "https://pin-web-client.gopayapi.com/")

	status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost,
		"https://customer.gopayapi.com/api/v1/users/pin/tokens/nb", body, headers, nil)
	if err != nil {
		return "", wrapErr(step, ErrCodeNetwork, 0, err, "POST tokenize")
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", newErr(step, ErrCodePINRejected, status, "PIN rejected body=%s", shortBody(raw, 300))
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", newErr(step, ErrCodeMidtransLink, status, "tokenize body=%s", shortBody(raw, 300))
	}
	var data genericJSON
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", wrapErr(step, ErrCodeMidtransLink, status, err, "decode tokenize")
	}
	tok := jsonString(data, "token")
	if tok == "" {
		// 嵌套 data.token / data.pin_token
		if d, ok := data["data"].(map[string]any); ok {
			tok = pickFirstNonEmpty(jsonString(d, "token"), jsonString(d, "pin_token"))
		}
	}
	if tok == "" {
		return "", newErr(step, ErrCodeMidtransLink, status, "no token in response body=%s", shortBody(raw, 400))
	}
	return tok, nil
}
