package flowmusic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/provider"
)

const (
	genTimeout  = 10 * time.Minute
	pollTimeout = 8 * time.Minute
)

// Provider 实现 provider.Provider：把 FlowMusic 4 步生成包装成同步 Generate。
type Provider struct {
	client *Client
}

// New 构造 FlowMusic Provider。
func New(cfg Config) *Provider {
	return &Provider{client: NewClient(cfg)}
}

// Name provider 标识。
func (p *Provider) Name() string { return "flowmusic" }

// statusOf 从上游错误里抽 HTTP 状态码；非 HTTP 错误返回 0。
func statusOf(err error) int {
	if err == nil {
		return 0
	}
	var he *UpstreamHTTPError
	if errors.As(err, &he) {
		return he.StatusCode
	}
	var ae *AuthError
	if errors.As(err, &ae) {
		return ae.StatusCode
	}
	return 0
}

// errBody 抽上游错误响应体（截断），供 admin 上游日志「响应」栏展示。
func errBody(err error) string {
	if err == nil {
		return ""
	}
	var he *UpstreamHTTPError
	if errors.As(err, &he) {
		return truncateStr(strings.TrimSpace(he.Body), 1500)
	}
	return truncateStr(err.Error(), 1500)
}

// jsonExcerpt 把任意结构序列化成截断后的 JSON 摘要。
func jsonExcerpt(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return truncateStr(string(b), 1500)
}

func truncateStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func parseCredential(raw string) Credentials {
	return ParseCredentialBundle(raw)
}

// Generate 同步发起一次音乐生成（会话 → 流式 → 轮询 → 取片）。
func (p *Provider) Generate(ctx context.Context, req *provider.Request) (*provider.Result, error) {
	if req == nil {
		return nil, errors.New("flowmusic: nil request")
	}
	creds := parseCredential(req.Credential)
	if strings.TrimSpace(creds.AccessToken) == "" && strings.TrimSpace(creds.Cookies) == "" {
		return nil, errors.New("flowmusic: empty credential")
	}
	creds.ProxyURL = req.ProxyURL

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, errors.New("flowmusic: empty prompt")
	}
	model := strings.TrimSpace(req.ModelCode)

	genCtx, cancel := context.WithTimeout(ctx, genTimeout)
	defer cancel()

	emit := func(percent int) {
		if req.OnPollProgress != nil && percent >= 0 {
			req.OnPollProgress(genCtx, percent, 0)
		}
	}
	base := strings.TrimRight(p.client.cfg.BaseURL, "/")
	emitLog := func(e provider.UpstreamLogEntry) {
		if req.UpstreamLog == nil {
			return
		}
		e.Provider = "flowmusic"
		req.UpstreamLog(genCtx, e)
	}

	start := time.Now()

	// 1. 创建会话
	emit(5)
	convReq := jsonExcerpt(map[string]any{
		"prompt": truncateStr(prompt, 400),
		"model":  firstNonEmpty(model, DefaultModelID),
	})
	convStart := time.Now()
	conv, err := p.client.StartConversation(genCtx, creds, prompt, model)
	if err != nil {
		emitLog(provider.UpstreamLogEntry{
			Stage: "conversation", Method: "POST", URL: base + "/__api/conversation",
			StatusCode: statusOf(err), DurationMs: time.Since(convStart).Milliseconds(),
			RequestExcerpt: convReq, ResponseExcerpt: errBody(err), Error: err.Error(),
		})
		return nil, err
	}
	emitLog(provider.UpstreamLogEntry{
		Stage: "conversation", Method: "POST", URL: base + "/__api/conversation",
		StatusCode: 200, DurationMs: time.Since(convStart).Milliseconds(),
		RequestExcerpt: convReq,
		ResponseExcerpt: jsonExcerpt(map[string]any{
			"job_id": conv.JobID, "operation_ids": conv.OperationIDs, "clip_ids": conv.ClipIDs,
		}),
		Meta: map[string]any{"job_id": conv.JobID, "operation_id_count": len(conv.OperationIDs), "clip_id_count": len(conv.ClipIDs)},
	})
	emit(20)

	operationIDs := append([]string{}, conv.OperationIDs...)
	clipIDs := append([]string{}, conv.ClipIDs...)

	// 2. 流式读取
	streamURL := base + "/__api/messages/" + conv.JobID + "/stream?last_id=0"
	streamStart := time.Now()
	var streamEventCount int
	stream, err := p.client.StreamMessagesWithEvents(genCtx, creds, conv.JobID, func(event ConversationStreamEvent) {
		streamEventCount++
		// 流式阶段进度大致 20~50%
		emit(35)
	})
	if err != nil {
		emitLog(provider.UpstreamLogEntry{
			Stage: "stream", Method: "GET", URL: streamURL,
			StatusCode: statusOf(err), DurationMs: time.Since(streamStart).Milliseconds(),
			ResponseExcerpt: errBody(err), Error: err.Error(),
			Meta: map[string]any{"events": streamEventCount},
		})
		return nil, err
	}
	operationIDs = uniqueStrings(append(operationIDs, stream.OperationIDs...))
	clipIDs = uniqueStrings(append(clipIDs, stream.ClipIDs...))
	emitLog(provider.UpstreamLogEntry{
		Stage: "stream", Method: "GET", URL: streamURL,
		StatusCode: 200, DurationMs: time.Since(streamStart).Milliseconds(),
		ResponseExcerpt: jsonExcerpt(map[string]any{
			"events": streamEventCount, "operation_ids": operationIDs, "clip_ids": clipIDs,
		}),
		Meta: map[string]any{"events": streamEventCount, "operation_id_count": len(operationIDs), "clip_id_count": len(clipIDs)},
	})
	emit(50)

	// 3. 轮询
	if len(clipIDs) == 0 {
		pollIDs := operationIDs
		if len(pollIDs) == 0 {
			hasToolCall, _ := streamAudioDiagnostics(stream)
			if hasToolCall {
				if lyricsIDs := extractToolLyricsIDs(stream.RawEvents); len(lyricsIDs) > 0 {
					pollIDs = lyricsIDs
				}
			}
		}
		pollURL := base + "/__api/audio-create-song-status/{id}"
		if len(pollIDs) == 0 {
			err := noAudioToolCallError(stream)
			emitLog(provider.UpstreamLogEntry{
				Stage: "poll", Method: "GET", URL: pollURL, StatusCode: 0,
				ResponseExcerpt: errBody(err), Error: err.Error(),
				Meta:            map[string]any{"poll_ids": pollIDs},
			})
			return nil, err
		}
		pollStart := time.Now()
		var lastStatus string
		var pollPolls int
		deadline := time.Now().Add(pollTimeout)
		ids, pollErr := p.client.PollClipsWithProgress(genCtx, creds, pollIDs, deadline, func(status ClipPollStatus) {
			pollPolls++
			if status.Status != "" {
				lastStatus = status.Status
			}
			emit(65)
		})
		if pollErr != nil && len(ids) == 0 {
			emitLog(provider.UpstreamLogEntry{
				Stage: "poll", Method: "GET", URL: pollURL,
				StatusCode: statusOf(pollErr), DurationMs: time.Since(pollStart).Milliseconds(),
				RequestExcerpt:  jsonExcerpt(map[string]any{"poll_ids": pollIDs}),
				ResponseExcerpt: errBody(pollErr), Error: pollErr.Error(),
				Meta:            map[string]any{"poll_ids": pollIDs, "polls": pollPolls, "last_status": lastStatus},
			})
			return nil, pollErr
		}
		clipIDs = uniqueStrings(append(clipIDs, ids...))
		emitLog(provider.UpstreamLogEntry{
			Stage: "poll", Method: "GET", URL: pollURL,
			StatusCode: 200, DurationMs: time.Since(pollStart).Milliseconds(),
			RequestExcerpt:  jsonExcerpt(map[string]any{"poll_ids": pollIDs}),
			ResponseExcerpt: jsonExcerpt(map[string]any{"clip_ids": clipIDs, "last_status": lastStatus}),
			Meta:            map[string]any{"poll_ids": pollIDs, "polls": pollPolls, "clip_id_count": len(clipIDs), "last_status": lastStatus},
		})
	} else {
		emitLog(provider.UpstreamLogEntry{
			Stage: "poll", StatusCode: 200,
			ResponseExcerpt: jsonExcerpt(map[string]any{"clip_ids": clipIDs}),
			Meta:            map[string]any{"clip_id_count": len(clipIDs), "skipped": true},
		})
	}
	if len(clipIDs) == 0 {
		return nil, fmt.Errorf("flowmusic: no clip ids produced")
	}
	emit(75)

	// 4. 取片详情
	clipsStart := time.Now()
	clips, err := p.client.GetClips(genCtx, creds, clipIDs)
	if err != nil {
		emitLog(provider.UpstreamLogEntry{
			Stage: "clips", Method: "POST", URL: base + "/__api/clips",
			StatusCode: statusOf(err), DurationMs: time.Since(clipsStart).Milliseconds(),
			RequestExcerpt:  jsonExcerpt(map[string]any{"clip_ids": clipIDs}),
			ResponseExcerpt: errBody(err), Error: err.Error(),
		})
		return nil, err
	}
	clipTitles := make([]string, 0, len(clips))
	for _, c := range clips {
		clipTitles = append(clipTitles, c.Title)
	}
	emitLog(provider.UpstreamLogEntry{
		Stage: "clips", Method: "POST", URL: base + "/__api/clips",
		StatusCode: 200, DurationMs: time.Since(clipsStart).Milliseconds(),
		RequestExcerpt:  jsonExcerpt(map[string]any{"clip_ids": clipIDs}),
		ResponseExcerpt: jsonExcerpt(map[string]any{"clip_count": len(clips), "titles": clipTitles}),
		Meta:            map[string]any{"clip_count": len(clips)},
	})
	emit(90)

	assets := make([]provider.Asset, 0, len(clips))
	for _, clip := range clips {
		if clip.AudioURL == "" && clip.WavURL == "" {
			continue
		}
		meta := map[string]any{
			"model": firstNonEmpty(model, DefaultModelID),
			"kind":  "music",
		}
		if clip.Title != "" {
			meta["title"] = clip.Title
		}
		if clip.Lyrics != "" {
			meta["lyrics"] = clip.Lyrics
		}
		if clip.LyricsID != "" {
			meta["lyrics_id"] = clip.LyricsID
		}
		if clip.WavURL != "" {
			meta["wav_url"] = clip.WavURL
		}
		if clip.VideoURL != "" {
			meta["video_url"] = clip.VideoURL
		}
		if clip.SoundPrompt != "" {
			meta["sound_prompt"] = clip.SoundPrompt
		}
		if clip.OperationID != "" {
			meta["operation_id"] = clip.OperationID
		}
		if clip.ID != "" {
			meta["clip_id"] = clip.ID
		}
		audioURL := firstNonEmpty(clip.AudioURL, clip.WavURL)
		assets = append(assets, provider.Asset{
			URL:        audioURL,
			ThumbURL:   clip.ImageURL,
			DurationMs: int(clip.DurationSeconds * 1000),
			Mime:       "audio/mpeg",
			Meta:       meta,
		})
	}
	if len(assets) == 0 {
		return nil, fmt.Errorf("flowmusic: clips returned without audio")
	}
	emit(100)

	return &provider.Result{
		TaskID:  req.TaskID,
		Assets:  assets,
		Latency: time.Since(start),
	}, nil
}

func streamAudioDiagnostics(stream ConversationResult) (bool, string) {
	var hasToolCall bool
	var texts []string
	for _, raw := range stream.RawEvents {
		event := parseConversationStreamEvent("", raw)
		if event.ToolName == "audio__create_song" && (event.PartKind == "tool-call" || event.PartKind == "tool-return" || event.PartKind == "retry-prompt") {
			hasToolCall = true
		}
		switch event.PartKind {
		case "text":
			if text := firstNonEmpty(event.TextDelta, event.TextContent); text != "" {
				texts = append(texts, text)
			}
		case "retry-prompt":
			if event.TextContent != "" {
				texts = append(texts, event.TextContent)
			}
		}
	}
	return hasToolCall, strings.TrimSpace(strings.Join(texts, ""))
}

func noAudioToolCallError(stream ConversationResult) error {
	hasToolCall, text := streamAudioDiagnostics(stream)
	switch {
	case hasToolCall:
		return fmt.Errorf("音乐生成失败：上游已调用生成工具但未返回任务 ID，请稍后重试")
	case text != "":
		return fmt.Errorf("音乐生成失败：上游未调用音乐生成工具，请检查提示词是否包含音乐相关描述")
	default:
		return fmt.Errorf("音乐生成失败：上游未返回音乐生成结果，请检查账号状态后重试")
	}
}
