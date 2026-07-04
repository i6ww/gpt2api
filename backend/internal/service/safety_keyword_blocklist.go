package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kleinai/backend/pkg/errcode"
)

const UnsafeKeywordMessage = "Your keyword is unsafe. Please update or change your keyword."

func (s *SystemConfigService) ValidateKeywordSafe(ctx context.Context, values ...string) error {
	if s == nil || !s.GetBool(ctx, SettingSafetyKeywordBlocklistEnabled, false) {
		return nil
	}
	words := s.keywordBlocklistWords(ctx)
	if len(words) == 0 {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(s.GetString(ctx, SettingSafetyKeywordBlocklistMode, "contains")))
	if mode == "" {
		mode = "contains"
	}
	for _, value := range values {
		if keywordBlocklistHit(value, words, mode) {
			return errcode.InvalidParam.WithMsg(UnsafeKeywordMessage)
		}
	}
	return nil
}

func (s *SystemConfigService) keywordBlocklistWords(ctx context.Context) []string {
	raw, ok := s.getRaw(ctx, SettingSafetyKeywordBlocklistWords)
	if !ok {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		return cleanKeywordBlocklistWords(arr)
	}
	var text string
	if err := json.Unmarshal([]byte(raw), &text); err != nil {
		text = strings.Trim(raw, "\"")
	}
	return parseKeywordBlocklistText(text)
}

func keywordBlocklistHit(value string, words []string, mode string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	for _, word := range words {
		w := strings.ToLower(strings.TrimSpace(word))
		if w == "" {
			continue
		}
		switch mode {
		case "exact":
			if normalized == w {
				return true
			}
		default:
			if strings.Contains(normalized, w) {
				return true
			}
		}
	}
	return false
}

func parseKeywordBlocklistText(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == '，'
	})
	return cleanKeywordBlocklistWords(fields)
}

func cleanKeywordBlocklistWords(words []string) []string {
	out := make([]string, 0, len(words))
	seen := map[string]struct{}{}
	for _, word := range words {
		w := strings.TrimSpace(word)
		if w == "" {
			continue
		}
		key := strings.ToLower(w)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, w)
	}
	return out
}
