// Package flowmusic 移植自独立服务 FlowMusic2API：把 FlowMusic（flowmusic.app）
// 的「会话 → 流式 → 轮询 → 取片」音乐生成 + Supabase/Google token 刷新链，
// 包装成本项目的 provider.Provider + 号池续期所需的客户端。
//
// 本文件是从上游 internal/service/flowmusic.go 移植的通用 JSON 解析 helper，
// 字段别名兼容非常宽容（上游响应结构多变），逐字保留其语义。
package flowmusic

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

func conversationJobID(payload map[string]any) string {
	return firstNonEmpty(
		findString(payload, "job_id"),
		findString(payload, "jobId"),
		findString(payload, "conversation_id"),
		getString(payload, "id"),
		getNestedString(payload, "data", "id"),
	)
}

func collectIDs(data string, result *ConversationResult) {
	var payload any
	if json.Unmarshal([]byte(data), &payload) == nil {
		for _, id := range findOperationIDs(payload) {
			result.OperationIDs = appendUnique(result.OperationIDs, id)
		}
		for _, id := range findClipIDs(payload) {
			result.ClipIDs = appendUnique(result.ClipIDs, id)
		}
	}
}

func parseConversationStreamEvent(eventName, data string) ConversationStreamEvent {
	event := ConversationStreamEvent{Event: eventName, Data: data}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return event
	}
	event.Status = getString(payload, "status")
	event.TextDelta = getString(payload, "delta")
	if part, ok := directValue(payload, "part"); ok {
		if partMap, ok := part.(map[string]any); ok {
			event.PartKind = getString(partMap, "part_kind")
			event.ToolName = getString(partMap, "tool_name")
			if event.PartKind == "" && event.ToolName != "" {
				if event.Status == "start" {
					event.PartKind = "tool-call"
				} else if event.Status == "end" {
					event.PartKind = "tool-return"
				}
			}
			if args, ok := directValueOrNil(partMap, "args").(map[string]any); ok {
				event.ToolTitle = firstNonEmpty(getString(args, "title"), findString(args, "title"))
				event.SoundPrompt = firstNonEmpty(getString(args, "sound_prompt"), findString(args, "sound_prompt"))
			}
			if content := directValueOrNil(partMap, "content"); content != nil {
				if cm, ok := content.(map[string]any); ok && event.ToolName == "audio__create_song" && event.PartKind == "tool-return" {
					if id := firstNonEmpty(getString(cm, "operation_id"), getString(cm, "op_id")); id != "" {
						event.OperationIDs = appendUnique(event.OperationIDs, id)
					}
					if op, ok := cm["operation"].(map[string]any); ok {
						if id := firstNonEmpty(getString(op, "id"), getString(op, "operation_id")); id != "" {
							event.OperationIDs = appendUnique(event.OperationIDs, id)
						}
					}
					for _, key := range []string{"clip_ids", "clip_id", "clipIds", "clipId", "clips"} {
						if val, ok := cm[key]; ok {
							switch v := val.(type) {
							case []any:
								for _, item := range v {
									if s, ok := item.(string); ok {
										event.ClipIDs = appendUnique(event.ClipIDs, s)
									}
								}
							case string:
								event.ClipIDs = appendUnique(event.ClipIDs, v)
							}
						}
					}
				}
				for _, id := range findOperationIDs(content) {
					event.OperationIDs = appendUnique(event.OperationIDs, id)
				}
				for _, id := range findClipIDs(content) {
					event.ClipIDs = appendUnique(event.ClipIDs, id)
				}
				event.TextContent = scalarString(content)
			}
		}
	}
	return event
}

func extractToolLyricsIDs(events []string) []string {
	var lyricsIDs []string
	for _, raw := range events {
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		part, ok := directValue(payload, "part")
		if !ok {
			continue
		}
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if getString(partMap, "tool_name") != "audio__create_song" {
			continue
		}
		args, ok := partMap["args"].(map[string]any)
		if !ok {
			continue
		}
		lyricsID := getString(args, "lyrics_id")
		if lyricsID != "" {
			lyricsIDs = appendUnique(lyricsIDs, lyricsID)
		}
	}
	return lyricsIDs
}

func directValueOrNil(m map[string]any, key string) any {
	value, ok := directValue(m, key)
	if !ok {
		return nil
	}
	return value
}

func clipsFromPayload(payload map[string]any) []ClipResult {
	raw := findValue(payload, "clips")
	if raw == nil {
		raw = findValue(payload, "data")
	}
	switch typed := raw.(type) {
	case map[string]any:
		out := make([]ClipResult, 0, len(typed))
		for id, item := range typed {
			if m, ok := item.(map[string]any); ok {
				out = append(out, clipFromMap(m, id))
			}
		}
		return out
	case []any:
		out := make([]ClipResult, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				out = append(out, clipFromMap(m, ""))
			}
		}
		return out
	default:
		return nil
	}
}

func orderClipsByIDs(clips []ClipResult, ids []string) []ClipResult {
	if len(clips) < 2 || len(ids) == 0 {
		return clips
	}
	byID := make(map[string][]ClipResult, len(clips))
	for _, clip := range clips {
		byID[clip.ID] = append(byID[clip.ID], clip)
	}
	out := make([]ClipResult, 0, len(clips))
	used := make(map[string]int, len(clips))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		items := byID[id]
		index := used[id]
		if index >= len(items) {
			continue
		}
		out = append(out, items[index])
		used[id] = index + 1
	}
	for _, clip := range clips {
		if used[clip.ID] > 0 {
			used[clip.ID]--
			continue
		}
		out = append(out, clip)
	}
	if len(out) == len(clips) {
		return out
	}
	return clips
}

func clipFromMap(item map[string]any, fallbackID string) ClipResult {
	lyrics, lyricsID := clipLyrics(item)
	return ClipResult{
		ID:              firstNonEmpty(getString(item, "id"), getString(item, "clip_id"), getString(item, "clipId"), fallbackID),
		Title:           findString(item, "title"),
		AudioURL:        mediaURL(item, "audio", "audio_url", "audioUrl", "mp3_url", "mp3Url", "m4a_url", "m4aUrl"),
		WavURL:          mediaURL(item, "wav", "wav_url", "wavUrl", "wave_url", "waveUrl"),
		ImageURL:        mediaURL(item, "image", "image_url", "imageUrl", "cover_url", "coverUrl"),
		VideoURL:        mediaURL(item, "video", "video_url", "videoUrl", "avi_url", "aviUrl", "mp4_url", "mp4Url"),
		Lyrics:          lyrics,
		LyricsID:        lyricsID,
		SoundPrompt:     firstNonEmpty(getNestedString(item, "operation", "sound_prompt"), getString(item, "sound_prompt"), findString(item, "sound_prompt")),
		OperationID:     firstNonEmpty(getString(item, "op_id"), getString(item, "operation_id"), getString(item, "operationId"), getNestedString(item, "operation", "id")),
		OperationType:   firstNonEmpty(getString(item, "op_type"), getString(item, "operation_type"), getNestedString(item, "operation", "op_type")),
		DurationSeconds: clipDurationSeconds(item),
		CreatedAt:       getString(item, "created_at"),
	}
}

func clipLyrics(item map[string]any) (string, string) {
	raw, ok := directValue(item, "lyrics")
	if !ok || raw == nil {
		return firstNonEmpty(getString(item, "lyrics_text"), getString(item, "lyricsText")), getString(item, "lyrics_id")
	}
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed), getString(item, "lyrics_id")
	case map[string]any:
		if value, ok := directValue(typed, "value"); ok {
			if valueMap, ok := value.(map[string]any); ok {
				return firstNonEmpty(getString(valueMap, "text"), getString(valueMap, "lyrics")), firstNonEmpty(getString(valueMap, "id"), getString(item, "lyrics_id"))
			}
			if text := scalarString(value); text != "" {
				return text, firstNonEmpty(getString(typed, "id"), getString(item, "lyrics_id"))
			}
		}
		return firstNonEmpty(getString(typed, "text"), getString(typed, "lyrics")), firstNonEmpty(getString(typed, "id"), getString(item, "lyrics_id"))
	default:
		return scalarString(typed), getString(item, "lyrics_id")
	}
}

func clipDurationSeconds(item map[string]any) float64 {
	raw, ok := directValue(item, "duration")
	if !ok || raw == nil {
		if n, ok := findNumeric(item, "duration_seconds"); ok {
			return n
		}
		return 0
	}
	if n, ok := numericValue(raw); ok {
		return n
	}
	if m, ok := raw.(map[string]any); ok {
		if value, ok := directValue(m, "value"); ok {
			if n, ok := numericValue(value); ok {
				return n
			}
		}
	}
	return 0
}

func mediaURL(item map[string]any, objectKey string, aliases ...string) string {
	for _, key := range aliases {
		if url := findString(item, key); url != "" {
			return url
		}
	}
	if raw, ok := directValue(item, objectKey); ok {
		if url := mediaValueURL(raw, aliases...); url != "" {
			return url
		}
	}
	if url := findString(item, objectKey); url != "" {
		return url
	}
	return ""
}

func mediaValueURL(value any, aliases ...string) string {
	if s := scalarString(value); s != "" {
		return s
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := append([]string{"url", "src", "href", "download_url", "downloadUrl"}, aliases...)
		for _, key := range keys {
			if s := getString(typed, key); s != "" {
				return s
			}
		}
		for _, key := range keys {
			if s := findString(typed, key); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range typed {
			if s := mediaValueURL(child, aliases...); s != "" {
				return s
			}
		}
	}
	return ""
}

func findClipIDs(value any) []string {
	var out []string
	var walk func(any, string, []string)
	walk = func(v any, key string, ancestors []string) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range sortedMapKeys(x) {
				child := x[k]
				normalized := normalizeFieldName(k)
				if isClipIDKey(normalized) {
					out = appendClipIDValues(out, child)
					continue
				}
				if normalized == "id" && hasClipAncestor(ancestors) {
					out = appendClipIDValues(out, child)
					continue
				}
				walk(child, normalized, append(ancestors, normalized))
			}
		case []any:
			for _, child := range x {
				walk(child, key, ancestors)
			}
		case string:
			if isClipIDKey(key) {
				out = appendUnique(out, x)
			}
		}
	}
	walk(value, "", nil)
	return out
}

func findOperationIDs(value any) []string {
	var out []string
	var walk func(any, string, []string)
	walk = func(v any, key string, ancestors []string) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range sortedMapKeys(x) {
				child := x[k]
				normalized := normalizeFieldName(k)
				if isOperationIDKey(normalized) {
					out = appendOperationIDValues(out, child)
					continue
				}
				if normalized == "id" && hasOperationAncestor(ancestors) {
					out = appendOperationIDValues(out, child)
					continue
				}
				walk(child, normalized, append(ancestors, normalized))
			}
		case []any:
			for _, child := range x {
				walk(child, key, ancestors)
			}
		case string:
			if isOperationIDKey(key) {
				out = appendUnique(out, x)
			}
		}
	}
	walk(value, "", nil)
	return out
}

func isOperationIDKey(key string) bool {
	key = normalizeFieldName(key)
	return key == "operationid" || key == "operationids" || key == "operationida" || key == "operationidb" || key == "opid" || key == "opids"
}

func appendOperationIDValues(values []string, value any) []string {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			values = appendOperationIDValues(values, child)
		}
	case map[string]any:
		values = appendOperationIDValues(values, firstNonEmpty(getString(typed, "operation_id"), getString(typed, "operationId"), getString(typed, "id")))
	default:
		values = appendUnique(values, scalarString(typed))
	}
	return values
}

func hasOperationAncestor(ancestors []string) bool {
	for _, ancestor := range ancestors {
		switch normalizeFieldName(ancestor) {
		case "operation", "operations", "audiooperation", "songoperation", "generationoperation":
			return true
		}
	}
	return false
}

func isClipIDKey(key string) bool {
	key = normalizeFieldName(key)
	return key == "clipid" || key == "clipids" || key == "clipida" || key == "clipidb" || key == "clip_id"
}

func appendClipIDValues(values []string, value any) []string {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			values = appendClipIDValues(values, child)
		}
	case map[string]any:
		values = appendClipIDValues(values, firstNonEmpty(getString(typed, "id"), getString(typed, "clip_id"), getString(typed, "clipId")))
	default:
		values = appendUnique(values, scalarString(typed))
	}
	return values
}

func hasClipAncestor(ancestors []string) bool {
	for _, ancestor := range ancestors {
		switch normalizeFieldName(ancestor) {
		case "clip", "clips", "cliplist", "clipdata", "generatedclips":
			return true
		}
	}
	return false
}

func normalizeFieldName(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "_", "")
	key = strings.ReplaceAll(key, "-", "")
	return key
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = appendUnique(out, value)
	}
	return out
}

func parseExpires(payload map[string]any) *time.Time {
	if seconds, ok := findNumeric(payload, "expires_in"); ok && seconds > 0 {
		t := time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		return &t
	}
	if unix, ok := findNumeric(payload, "expires_at"); ok && unix > 0 {
		t := time.Unix(int64(unix), 0).UTC()
		return &t
	}
	if raw := firstNonEmpty(findString(payload, "expires_at"), findString(payload, "expires")); raw != "" {
		return parseTime(raw)
	}
	return nil
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if value, ok := directValue(m, key); ok && value != nil {
		return scalarString(value)
	}
	return ""
}

func getNestedString(m map[string]any, keys ...string) string {
	var value any = m
	for _, key := range keys {
		current, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		next, ok := directValue(current, key)
		if !ok {
			return ""
		}
		value = next
	}
	return scalarString(value)
}

func directValue(m map[string]any, key string) (any, bool) {
	target := strings.TrimSpace(key)
	for k, value := range m {
		if strings.EqualFold(strings.TrimSpace(k), target) {
			return value, true
		}
	}
	normalizedTarget := normalizeFieldName(target)
	if normalizedTarget == "" {
		return nil, false
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if normalizeFieldName(k) == normalizedTarget {
			return m[k], true
		}
	}
	return nil, false
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(typed, 'f', -1, 64))
	case float32:
		return strings.TrimSpace(strconv.FormatFloat(float64(typed), 'f', -1, 32))
	default:
		return ""
	}
}

func numericValue(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case int32:
		return float64(value), true
	case json.Number:
		f, err := value.Float64()
		return f, err == nil
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(value, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func findString(value any, key string) string {
	key = normalizeFieldName(key)
	if key == "" {
		return ""
	}
	var walk func(any) string
	walk = func(v any) string {
		switch typed := v.(type) {
		case map[string]any:
			for k, child := range typed {
				if normalizeFieldName(k) == key {
					if s := scalarString(child); s != "" && s != "<nil>" {
						return s
					}
				}
			}
			for _, child := range typed {
				if s := walk(child); s != "" {
					return s
				}
			}
		case []any:
			for _, child := range typed {
				if s := walk(child); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(value)
}

func findNumeric(value any, key string) (float64, bool) {
	key = normalizeFieldName(key)
	if key == "" {
		return 0, false
	}
	var walk func(any) (float64, bool)
	walk = func(v any) (float64, bool) {
		switch typed := v.(type) {
		case map[string]any:
			for k, child := range typed {
				if normalizeFieldName(k) == key {
					if number, ok := numericValue(child); ok {
						return number, true
					}
				}
			}
			for _, child := range typed {
				if number, ok := walk(child); ok {
					return number, true
				}
			}
		case []any:
			for _, child := range typed {
				if number, ok := walk(child); ok {
					return number, true
				}
			}
		}
		return 0, false
	}
	return walk(value)
}

func findValue(value any, key string) any {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return nil
	}
	var walk func(any) any
	walk = func(v any) any {
		switch typed := v.(type) {
		case map[string]any:
			for k, child := range typed {
				if strings.ToLower(strings.TrimSpace(k)) == key {
					return child
				}
			}
			for _, child := range typed {
				if found := walk(child); found != nil {
					return found
				}
			}
		case []any:
			for _, child := range typed {
				if found := walk(child); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return walk(value)
}

func parseTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
