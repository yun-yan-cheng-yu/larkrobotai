// Package llm 是极简的 OpenAI 兼容 chat/completions 客户端,支持 function calling 与多模型降级
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Message 对话消息;Content 用 any 以兼容 assistant 发起 tool_calls 时的 null
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

func Text(role, text string) Message { return Message{Role: role, Content: text} }

func (m Message) String() string {
	s, _ := m.Content.(string)
	return s
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool 是 OpenAI function 格式的工具定义
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ModelSpec 一次调用尝试的模型;APIKey 为空时使用 Client 默认 key
type ModelSpec struct {
	Model  string
	APIKey string
}

type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// StatusError 表示 LLM 返回了非 200 状态码,调用方据此判断是否可重试
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("LLM HTTP %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []Choice `json:"choices"`
}

// ChatOnce 用指定模型和 key 请求一次
func (c *Client) ChatOnce(ctx context.Context, model, apiKey string, messages []Message, tools []Tool) (*Choice, error) {
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Tools: tools})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &StatusError{StatusCode: resp.StatusCode, Body: string(b)}
	}
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 LLM 响应: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("LLM 响应中没有 choices")
	}
	return &out.Choices[0], nil
}

// Chat 按顺序尝试模型清单,主模型失败自动降级:
// 429/5xx 退避后重试;401/403 等认证/权限错误立即换下一个模型(可能对应另一把 key);
// 全部尝试失败后返回最后一个错误
func (c *Client) Chat(ctx context.Context, models []ModelSpec, messages []Message, tools []Tool) (*Choice, error) {
	if len(models) == 0 {
		return nil, errors.New("未配置任何模型")
	}
	var lastErr error
	for attempt := 0; attempt < len(models)*2; attempt++ {
		spec := models[attempt%len(models)]
		choice, err := c.ChatOnce(ctx, spec.Model, spec.APIKey, messages, tools)
		if err == nil {
			if spec.Model != models[0].Model {
				log.Printf("[llm] 本次由降级模型 %s 应答", spec.Model)
			}
			return choice, nil
		}
		lastErr = err
		if attempt == len(models)*2-1 {
			break
		}
		var se *StatusError
		switch {
		case errors.As(err, &se) && (se.StatusCode == http.StatusTooManyRequests || se.StatusCode >= 500):
			log.Printf("[llm] %s 暂不可用(HTTP %d),重试/降级...", spec.Model, se.StatusCode)
			select {
			case <-time.After(time.Duration(2500*(attempt+1)) * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case errors.As(err, &se):
			log.Printf("[llm] %s 请求被拒(HTTP %d),切换下一模型...", spec.Model, se.StatusCode)
		default:
			log.Printf("[llm] %s 请求异常(%v),切换下一模型...", spec.Model, err)
		}
	}
	return nil, lastErr
}
