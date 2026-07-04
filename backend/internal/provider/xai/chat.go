package xai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIUsage OpenAI 风格 token 用量。
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResult chat 调用结果（与 provider/grok.ChatResult 对齐，便于 chat_service 复用）。
type ChatResult struct {
	Raw    []byte
	Status int
	Usage  *OpenAIUsage
}

// ChatComplete 调用官方 xAI /responses（非流式聚合），把 OpenAI chat-completions
// 风格的 body 翻译成 Responses 请求，解析 response.completed，回 OpenAI 格式 JSON。
//
//   - token    : 解密后的 access_token（Bearer）
//   - modelCode : 路由模型名（可带 "xai/" 前缀）
//   - body     : OpenAI chat-completions 请求体（含 messages）
//   - baseURL  : 账号 base_url，空走 DefaultBaseURL
func (c *Client) ChatComplete(ctx context.Context, token, modelCode string, body map[string]any, baseURL string) (*ChatResult, error) {
	upstreamModel := UpstreamModel(modelCode)
	reqBody := buildResponsesRequest(upstreamModel, body)
	payload, _ := json.Marshal(reqBody)

	base := strings.TrimRight(firstNonEmpty(baseURL, c.baseURL, DefaultBaseURL), "/")
	url := base + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("xai chat http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return &ChatResult{Raw: raw, Status: resp.StatusCode}, nil
	}

	text, usage, err := parseResponsesSSE(resp.Body)
	if err != nil {
		return nil, err
	}
	out := buildOpenAIChatResponse(upstreamModel, text, usage)
	raw, _ := json.Marshal(out)
	return &ChatResult{Raw: raw, Status: http.StatusOK, Usage: usage}, nil
}

// buildResponsesRequest 把 OpenAI chat-completions body 翻译成 xAI Responses 请求。
//
//   - system / developer 消息 → instructions（拼接）
//   - user / assistant 消息   → input[]（message item，input_text / output_text）
func buildResponsesRequest(model string, body map[string]any) map[string]any {
	var instructions []string
	input := make([]map[string]any, 0, 8)

	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		content := contentToText(m["content"])
		switch role {
		case "system", "developer":
			if content != "" {
				instructions = append(instructions, content)
			}
		case "assistant":
			input = append(input, map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": content}},
			})
		default: // user / tool / 其他都按 user 文本喂入
			input = append(input, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []map[string]any{{"type": "input_text", "text": content}},
			})
		}
	}

	req := map[string]any{
		"model":  model,
		"stream": true,
		"input":  input,
	}
	if len(instructions) > 0 {
		req["instructions"] = strings.Join(instructions, "\n\n")
	}
	if v, ok := body["temperature"]; ok {
		req["temperature"] = v
	}
	if v, ok := body["max_tokens"]; ok {
		req["max_output_tokens"] = v
	}
	if v, ok := body["max_output_tokens"]; ok {
		req["max_output_tokens"] = v
	}
	return req
}

// contentToText 把 OpenAI content（string 或 multimodal 数组）压成纯文本。
func contentToText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := pm["type"].(string); t == "text" || t == "input_text" || t == "output_text" {
				if s, _ := pm["text"].(string); s != "" {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(s)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

// parseResponsesSSE 读取 /responses 的 SSE 流，聚合最终文本 + usage。
//
// xAI 在 response.completed 事件里带完整 response.output；这里同时兜底累积
// response.output_text.delta 增量，避免某些模型不回 completed.output 的情况。
func parseResponsesSSE(r io.Reader) (string, *OpenAIUsage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var deltaBuf strings.Builder
	var finalText string
	var usage *OpenAIUsage

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Output []struct {
					Type    string `json:"type"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
				Usage *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
					TotalTokens  int `json:"total_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			deltaBuf.WriteString(ev.Delta)
		case "response.completed":
			var b strings.Builder
			for _, item := range ev.Response.Output {
				if item.Type != "message" {
					continue
				}
				for _, ct := range item.Content {
					if ct.Type == "output_text" {
						b.WriteString(ct.Text)
					}
				}
			}
			if b.Len() > 0 {
				finalText = b.String()
			}
			if u := ev.Response.Usage; u != nil {
				usage = &OpenAIUsage{
					PromptTokens:     u.InputTokens,
					CompletionTokens: u.OutputTokens,
					TotalTokens:      u.TotalTokens,
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("xai chat sse: %w", err)
	}
	if finalText == "" {
		finalText = deltaBuf.String()
	}
	if usage != nil && usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return finalText, usage, nil
}

// buildOpenAIChatResponse 组装 OpenAI chat-completions 风格响应。
func buildOpenAIChatResponse(model, text string, usage *OpenAIUsage) map[string]any {
	resp := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-xai-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": text},
				"finish_reason": "stop",
			},
		},
	}
	if usage != nil {
		resp["usage"] = map[string]any{
			"prompt_tokens":     usage.PromptTokens,
			"completion_tokens": usage.CompletionTokens,
			"total_tokens":      usage.TotalTokens,
		}
	}
	return resp
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
