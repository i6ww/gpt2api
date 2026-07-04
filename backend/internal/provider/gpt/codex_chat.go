package gpt

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

	"github.com/google/uuid"

	"github.com/kleinai/backend/pkg/outbound"
)

const (
	codexCLIUserAgent = "codex_cli_rs/0.125.0"
	codexCLIVersion   = "0.125.0"
)

var codexChatModelIDs = []string{
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
	"gpt-5.3-codex-spark",
}

var codexChatModelMap = map[string]string{
	"gpt-5.4":                    "gpt-5.4",
	"gpt-5.4-mini":               "gpt-5.4-mini",
	"gpt-5.3-codex":              "gpt-5.3-codex",
	"gpt-5.3-codex-low":          "gpt-5.3-codex",
	"gpt-5.3-codex-medium":       "gpt-5.3-codex",
	"gpt-5.3-codex-high":         "gpt-5.3-codex",
	"gpt-5.3-codex-xhigh":        "gpt-5.3-codex",
	"gpt-5.3-codex-spark":        "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-low":    "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-medium": "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-high":   "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-xhigh":  "gpt-5.3-codex-spark",
}

// ChatModelIDs returns downstream GPT Codex chat model codes.
func ChatModelIDs() []string {
	out := make([]string, len(codexChatModelIDs))
	copy(out, codexChatModelIDs)
	return out
}

// IsCodexChatModel reports whether model should route through chatgpt.com/backend-api/codex/responses.
func IsCodexChatModel(modelCode string) bool {
	_, ok := codexChatModelMap[normalizeCodexChatModelCode(modelCode)]
	return ok
}

func normalizeCodexChatModelCode(modelCode string) string {
	modelCode = strings.TrimSpace(modelCode)
	if idx := strings.LastIndex(modelCode, "/"); idx >= 0 {
		modelCode = modelCode[idx+1:]
	}
	return strings.ToLower(modelCode)
}

func upstreamCodexChatModel(modelCode string) string {
	if v, ok := codexChatModelMap[normalizeCodexChatModelCode(modelCode)]; ok && v != "" {
		return v
	}
	return strings.TrimSpace(modelCode)
}

// CodexChatUsage token usage from upstream Responses API.
type CodexChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// CodexChatResult non-streaming chat completion.
type CodexChatResult struct {
	Raw    []byte
	Status int
	Usage  *CodexChatUsage
}

// CodexChatClient talks to ChatGPT Codex Responses for Plus OAuth accounts.
type CodexChatClient struct{}

func NewCodexChatClient() *CodexChatClient { return &CodexChatClient{} }

func (c *CodexChatClient) ChatComplete(ctx context.Context, token, proxyURL, modelCode string, body map[string]any) (*CodexChatResult, error) {
	upstreamModel := upstreamCodexChatModel(modelCode)
	reqBody, err := chatCompletionsToCodexResponses(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	url := responseEndpoint("https://chatgpt.com/backend-api/codex")
	client, err := codexChatHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	setCodexChatHeaders(httpReq, token, uuid.NewString())
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return &CodexChatResult{Raw: raw, Status: resp.StatusCode}, nil
	}
	text, usage, err := collectCodexChatFromSSE(resp.Body)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(buildChatCompletionJSON(modelCode, text, usage))
	return &CodexChatResult{Raw: raw, Status: http.StatusOK, Usage: usage}, nil
}

func (c *CodexChatClient) ChatStream(ctx context.Context, token, proxyURL, modelCode string, body map[string]any, w http.ResponseWriter) (*CodexChatUsage, error) {
	upstreamModel := upstreamCodexChatModel(modelCode)
	reqBody, err := chatCompletionsToCodexResponses(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	url := responseEndpoint("https://chatgpt.com/backend-api/codex")
	client, err := codexChatHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	setCodexChatHeaders(httpReq, token, uuid.NewString())
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return nil, fmt.Errorf("codex chat http %d", resp.StatusCode)
	}
	return streamCodexChatToClient(resp.Body, modelCode, w)
}

func messagesFromBody(body map[string]any) ([]any, error) {
	raw, ok := body["messages"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("messages is required")
	}
	switch v := raw.(type) {
	case []any:
		return v, nil
	case []map[string]any:
		out := make([]any, len(v))
		for i, m := range v {
			out[i] = m
		}
		return out, nil
	default:
		return nil, fmt.Errorf("messages is required")
	}
}

func chatCompletionsToCodexResponses(body map[string]any, upstreamModel string) (map[string]any, error) {
	msgs, err := messagesFromBody(body)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("messages is required")
	}
	var instructions strings.Builder
	input := make([]map[string]any, 0, len(msgs))
	for _, raw := range msgs {
		msg, _ := raw.(map[string]any)
		if msg == nil {
			continue
		}
		role := strings.TrimSpace(anyString(msg["role"]))
		if role == "" {
			continue
		}
		if role == "system" {
			if s := contentToPlainText(msg["content"]); s != "" {
				if instructions.Len() > 0 {
					instructions.WriteString("\n")
				}
				instructions.WriteString(s)
			}
			continue
		}
		if role == "tool" {
			role = "user"
		}
		parts := contentToInputParts(msg["content"])
		if len(parts) == 0 {
			continue
		}
		input = append(input, map[string]any{
			"type":    "message",
			"role":    role,
			"content": parts,
		})
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("no user/assistant messages")
	}
	out := map[string]any{
		"model":  upstreamModel,
		"input":  input,
		"stream": true,
		"store":  false,
	}
	if instructions.Len() > 0 {
		out["instructions"] = strings.TrimSpace(instructions.String())
	} else {
		out["instructions"] = "You are a helpful assistant."
	}
	if effort := strings.TrimSpace(anyString(body["reasoning_effort"])); effort != "" {
		out["reasoning"] = map[string]any{"effort": effort}
	}
	applyCodexChatOAuthTransform(out)
	return out, nil
}

func applyCodexChatOAuthTransform(body map[string]any) {
	body["store"] = false
	body["stream"] = true
	for _, key := range []string{
		"temperature", "top_p", "max_tokens", "max_completion_tokens", "max_output_tokens",
		"frequency_penalty", "presence_penalty", "user", "metadata", "stream_options",
		"prompt_cache_retention", "safety_identifier", "n", "logprobs",
	} {
		delete(body, key)
	}
}

func contentToPlainText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		var b strings.Builder
		for _, item := range t {
			part, _ := item.(map[string]any)
			if part == nil {
				continue
			}
			if txt := strings.TrimSpace(anyString(part["text"])); txt != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(txt)
			}
		}
		return b.String()
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func contentToInputParts(v any) []map[string]any {
	txt := contentToPlainText(v)
	if txt == "" {
		return nil
	}
	return []map[string]any{{"type": "input_text", "text": txt}}
}

func anyString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

func codexChatHTTPClient(proxyURL string) (*http.Client, error) {
	return outbound.NewClient(outbound.Options{
		ProxyURL: strings.TrimSpace(proxyURL),
		Timeout:  10 * time.Minute,
		Mode:     outbound.ModeUTLS,
		Profile:  outbound.ProfileChrome,
	})
}

func setCodexChatHeaders(req *http.Request, token, sessionID string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", userAgentForEndpoint(req.URL.String()))
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("version", codexCLIVersion)
	req.Header.Set("session_id", sessionID)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Connection", "Keep-Alive")
}

func collectCodexChatFromSSE(r io.Reader) (string, *CodexChatUsage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var dataLines []string
	var text strings.Builder
	var usage *CodexChatUsage
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if data == "" || data == "[DONE]" {
			return
		}
		if delta := parseCodexTextDelta(data); delta != "" {
			text.WriteString(delta)
		}
		if u := parseCodexUsage(data); u != nil {
			usage = u
		}
		if completed := parseCodexCompletedText(data); completed != "" && text.Len() == 0 {
			text.WriteString(completed)
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return "", usage, err
	}
	if text.Len() == 0 {
		return "", usage, fmt.Errorf("codex chat returned empty text")
	}
	return text.String(), usage, nil
}

func streamCodexChatToClient(r io.Reader, modelCode string, w http.ResponseWriter) (*CodexChatUsage, error) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var dataLines []string
	id := "chatcmpl_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	created := time.Now().Unix()
	sentRole := false
	var usage *CodexChatUsage
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if data == "" || data == "[DONE]" {
			return
		}
		if u := parseCodexUsage(data); u != nil {
			usage = u
		}
		delta := parseCodexTextDelta(data)
		if delta == "" {
			delta = parseCodexCompletedText(data)
		}
		if delta == "" {
			return
		}
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   modelCode,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{},
			}},
		}
		choice := chunk["choices"].([]map[string]any)[0]
		deltaObj := choice["delta"].(map[string]any)
		if !sentRole {
			deltaObj["role"] = "assistant"
			sentRole = true
		}
		deltaObj["content"] = delta
		raw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		if flusher != nil {
			flusher.Flush()
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return usage, err
	}
	if usage != nil {
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   modelCode,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   usage,
		}
		raw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	} else {
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   modelCode,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		}
		raw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return usage, nil
}

func parseCodexTextDelta(data string) string {
	var ev map[string]any
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return ""
	}
	typ := strings.TrimSpace(anyString(ev["type"]))
	switch typ {
	case "response.output_text.delta":
		if s := strings.TrimSpace(anyString(ev["delta"])); s != "" {
			return s
		}
	case "response.content_part.delta":
		if part, _ := ev["delta"].(map[string]any); part != nil {
			if s := strings.TrimSpace(anyString(part["text"])); s != "" {
				return s
			}
		}
	}
	return ""
}

func parseCodexCompletedText(data string) string {
	var ev responseCompletedEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return extractWebAssistantText(data)
	}
	if ev.Type != "response.completed" && len(ev.Response.Output) == 0 {
		return extractWebAssistantText(data)
	}
	var b strings.Builder
	for _, item := range ev.Response.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if s := strings.TrimSpace(c.Text); s != "" {
				b.WriteString(s)
			}
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	return extractWebAssistantText(data)
}

func parseCodexUsage(data string) *CodexChatUsage {
	var root map[string]any
	if err := json.Unmarshal([]byte(data), &root); err != nil {
		return nil
	}
	usageRaw, _ := root["usage"].(map[string]any)
	if usageRaw == nil {
		if resp, _ := root["response"].(map[string]any); resp != nil {
			usageRaw, _ = resp["usage"].(map[string]any)
		}
	}
	if usageRaw == nil {
		return nil
	}
	inTok := int(anyNumber(usageRaw["input_tokens"]))
	outTok := int(anyNumber(usageRaw["output_tokens"]))
	if inTok == 0 && outTok == 0 {
		inTok = int(anyNumber(usageRaw["prompt_tokens"]))
		outTok = int(anyNumber(usageRaw["completion_tokens"]))
	}
	if inTok == 0 && outTok == 0 {
		return nil
	}
	total := inTok + outTok
	if t := int(anyNumber(usageRaw["total_tokens"])); t > 0 {
		total = t
	}
	return &CodexChatUsage{PromptTokens: inTok, CompletionTokens: outTok, TotalTokens: total}
}

func anyNumber(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}

func buildChatCompletionJSON(modelCode, text string, usage *CodexChatUsage) map[string]any {
	out := map[string]any{
		"id":      "chatcmpl_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelCode,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": text,
			},
			"finish_reason": "stop",
		}},
	}
	if usage != nil {
		out["usage"] = usage
	} else {
		est := estimateOpenAIUsage("", text)
		out["usage"] = est
	}
	return out
}

func estimateOpenAIUsage(prompt, completion string) map[string]int {
	pt := len(prompt)/4 + 1
	ct := len(completion)/4 + 1
	if pt < 1 {
		pt = 1
	}
	if ct < 1 {
		ct = 1
	}
	return map[string]int{
		"prompt_tokens":     pt,
		"completion_tokens": ct,
		"total_tokens":      pt + ct,
	}
}
