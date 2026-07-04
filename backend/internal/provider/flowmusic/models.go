package flowmusic

import "strings"

// DefaultModelID 默认模型。
const DefaultModelID = "lyria"

// GenerationModel 对外模型 → FlowMusic 上游参数映射。
//
// 对外多个模型全部映射到上游同一个 model_name=producer:standard + mode=standard，
// 区别只在 ghostwriter_version（standard vs pro）。
type GenerationModel struct {
	ID                 string
	Name               string
	FlowMusicModelName string
	FlowMusicMode      string
	GhostwriterVersion string
	Aliases            []string
}

var generationModels = []GenerationModel{
	{
		ID:                 "lyria",
		Name:               "Lyria",
		FlowMusicModelName: "producer:standard",
		FlowMusicMode:      "standard",
		GhostwriterVersion: "standard",
		Aliases:            []string{"lyria-standard", "flowmusic-producer-standard", "flowmusic-standard", "flowmusic", "music-1"},
	},
	{
		ID:                 "lyria-fast",
		Name:               "Lyria Fast",
		FlowMusicModelName: "producer:standard",
		FlowMusicMode:      "standard",
		GhostwriterVersion: "standard",
	},
	{
		ID:                 "lyria-pro",
		Name:               "Lyria Pro",
		FlowMusicModelName: "producer:standard",
		FlowMusicMode:      "standard",
		GhostwriterVersion: "pro",
		Aliases:            []string{"music-pro"},
	},
	{
		ID:                 "lyria-pro-fast",
		Name:               "Lyria Pro Fast",
		FlowMusicModelName: "producer:standard",
		FlowMusicMode:      "standard",
		GhostwriterVersion: "pro",
	},
}

func normalizeModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}

func resolveModel(modelID string) GenerationModel {
	normalized := normalizeModelID(modelID)
	if normalized == "" {
		normalized = DefaultModelID
	}
	for _, m := range generationModels {
		if m.ID == normalized {
			return m
		}
		for _, alias := range m.Aliases {
			if normalizeModelID(alias) == normalized {
				return m
			}
		}
	}
	spec := generationModels[0]
	return spec
}

func buildConversationRequest(prompt, model string) ConversationRequest {
	spec := resolveModel(model)
	return ConversationRequest{
		Parts: []ConversationPart{
			{Content: buildMusicGenerationPrompt(prompt), PartKind: "user-prompt"},
		},
		ClientContext: ConversationClientContext{
			SongQueue:          []any{},
			SelectedModel:      nil,
			LyricsIDMap:        map[string]any{},
			GhostwriterVersion: spec.GhostwriterVersion,
		},
		ModelName: spec.FlowMusicModelName,
		Mode:      spec.FlowMusicMode,
	}
}

func buildMusicGenerationPrompt(prompt string) string {
	prompt = compactMusicPrompt(prompt)
	if prompt == "" {
		prompt = "适合直接播放的纯音乐"
	}
	runes := []rune(prompt)
	limit := 220
	if promptContainsExplicitLyrics(prompt) {
		limit = 3000
	}
	if len(runes) > limit {
		prompt = string(runes[:limit])
	}
	if promptHasMusicIntent(prompt) {
		return "直接生成" + prompt
	}
	return "直接生成" + prompt + "音乐"
}

func compactMusicPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	preserveLayout := promptContainsExplicitLyrics(prompt)
	if preserveLayout {
		prompt = strings.ReplaceAll(prompt, "\r\n", "\n")
		prompt = strings.ReplaceAll(prompt, "\r", "\n")
	} else {
		prompt = strings.Join(strings.Fields(prompt), " ")
	}
	replacements := []struct {
		old string
		new string
	}{
		{"catchy chorus", "catchy"},
		{"必须直接生成音乐", ""},
		{"必须生成音乐", ""},
		{"直接生成音乐", ""},
		{"直接生成歌曲", ""},
		{"生成音乐", ""},
		{"生成歌曲", ""},
		{"创作音乐", ""},
		{"创作歌曲", ""},
		{"一首", ""},
		{"歌曲", "音乐"},
		{"歌名", ""},
		{"作词", ""},
		{"不要只回复文字", ""},
		{"不要回复文字", ""},
		{"不要只返回文字", ""},
		{"不要返回文字", ""},
		{"不要只给文字", ""},
		{"不要给建议", ""},
		{"不要解释", ""},
		{"只输出结果", ""},
		{"工具调用", ""},
		{"调用工具", ""},
		{"audio__create_song", ""},
		{"dalle.text2im", ""},
		{"DALL-E", ""},
		{"dalle", ""},
		{"text2im", ""},
		{"图片", ""},
		{"图像", ""},
		{"封面", ""},
		{"海报", ""},
		{"album cover", ""},
		{"cover art", ""},
		{"cover image", ""},
		{"image", ""},
		{"picture", ""},
		{"poster", ""},
		{"song", "music"},
		{"write a", ""},
		{"write an", ""},
		{"API", ""},
		{"api", ""},
	}
	for _, item := range replacements {
		prompt = strings.ReplaceAll(prompt, item.old, item.new)
	}
	if preserveLayout {
		lines := strings.Split(prompt, "\n")
		out := make([]string, 0, len(lines))
		blank := false
		for _, line := range lines {
			line = strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
			if line == "" {
				if !blank && len(out) > 0 {
					out = append(out, "")
				}
				blank = true
				continue
			}
			out = append(out, line)
			blank = false
		}
		prompt = strings.TrimSpace(strings.Join(out, "\n"))
	} else {
		prompt = strings.NewReplacer(
			"，", " ",
			",", " ",
			"；", " ",
			";", " ",
			"。", " ",
			".", " ",
			"：", " ",
			":", " ",
			"、", " ",
			"（", " ",
			"）", " ",
			"(", " ",
			")", " ",
		).Replace(prompt)
		prompt = strings.Trim(prompt, " \t\r\n,，;；。.!！:：-—")
		prompt = strings.Join(strings.Fields(prompt), " ")
	}
	prompt = strings.Trim(prompt, " \t\r\n,，;；。.!！:：-—")
	for strings.Contains(prompt, "音乐音乐") {
		prompt = strings.ReplaceAll(prompt, "音乐音乐", "音乐")
	}
	return prompt
}

func promptContainsExplicitLyrics(prompt string) bool {
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "歌词") || strings.Contains(lower, "lyrics") {
		return true
	}
	for _, marker := range []string{
		"[verse]", "[chorus]", "[bridge]", "[intro]", "[outro]", "[pre-chorus]",
		"[主歌]", "[副歌]", "[导歌]", "[桥段]", "[前奏]", "[尾奏]",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func promptHasMusicIntent(prompt string) bool {
	lower := strings.ToLower(prompt)
	for _, marker := range []string{
		"音乐", "歌曲", "歌", "曲", "旋律", "副歌", "歌词", "人声", "纯音乐", "器乐", "节拍", "电音", "流行", "摇滚", "爵士", "民谣", "说唱", "嘻哈", "lo-fi", "lofi", "bpm",
		"music", "song", "track", "melody", "chorus", "lyrics", "vocal", "instrumental", "beat", "pop", "rock", "jazz", "hip hop", "electro", "electropop",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
