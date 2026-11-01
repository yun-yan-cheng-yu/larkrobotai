// Package agent 串联 LLM 与工具提供者:会话记忆、消息去重、function calling 循环
package agent

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"larkrobotai/internal/llm"
)

const (
	maxHistory    = 20   // 每个会话保留的最近消息条数
	maxToolRounds = 6    // 单次提问最多几轮工具调用;耗尽后强制模型基于已获取的信息收尾作答
	maxToolResult = 8000 // 工具结果注入上下文前的截断长度
	toolTimeout   = 120 * time.Second
)

// ToolProvider 是一组可挂给大脑的工具;Defs 在启动时调用,Call 处理具体工具
type ToolProvider interface {
	Defs() []llm.Tool
	Owns(name string) bool
	Call(ctx context.Context, name string, args map[string]any) (string, error)
}

type Agent struct {
	llm       *llm.Client
	providers []ToolProvider
	models    []llm.ModelSpec

	tools     []llm.Tool
	memory    map[string][]llm.Message // chat_id -> 历史
	memMu     sync.Mutex
	processed map[string]struct{} // message_id 去重
	procMu    sync.Mutex
}

func New(lc *llm.Client, providers []ToolProvider, models []llm.ModelSpec) *Agent {
	return &Agent{
		llm:       lc,
		providers: providers,
		models:    models,
		memory:    map[string][]llm.Message{},
		processed: map[string]struct{}{},
	}
}

// InitTools 汇总所有提供者的工具定义
func (a *Agent) InitTools() int {
	a.tools = nil
	for _, p := range a.providers {
		a.tools = append(a.tools, p.Defs()...)
	}
	return len(a.tools)
}

// FirstTime 返回该 message_id 是否第一次出现(飞书事件至少一次投递,需要去重)
func (a *Agent) FirstTime(messageID string) bool {
	a.procMu.Lock()
	defer a.procMu.Unlock()
	if _, ok := a.processed[messageID]; ok {
		return false
	}
	a.processed[messageID] = struct{}{}
	if len(a.processed) > 1000 {
		a.processed = map[string]struct{}{}
	}
	return true
}

// Handle 处理一条用户消息,返回最终回复文本
func (a *Agent) Handle(ctx context.Context, chatID, userText string) (string, error) {
	if a.llm == nil {
		return "收到!我还没有接入大脑(占位回复)", nil
	}

	a.memMu.Lock()
	history := append(a.memory[chatID], llm.Text("user", userText))
	a.memory[chatID] = history
	a.memMu.Unlock()

	messages := append([]llm.Message(nil), latest(history, maxHistory)...)

	var final string
	pending := false // 最后一轮是否停在"模型要求调工具"上
	for round := 0; round < maxToolRounds; round++ {
		choice, err := a.llm.Chat(ctx, a.models, messages, a.tools)
		if err != nil {
			return "", err
		}
		if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) > 0 {
			pending = true
			messages = append(messages, llm.Message{
				Role:      "assistant",
				Content:   choice.Message.Content, // 可能为 null
				ToolCalls: choice.Message.ToolCalls,
			})
			for _, tc := range choice.Message.ToolCalls {
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: a.runTool(ctx, tc)})
			}
			continue
		}
		pending = false
		final = choice.Message.String()
		break
	}

	if pending {
		// 工具轮次耗尽:做一次强制收尾调用——不再提供工具,逼模型基于已获取的信息作答,
		// 而不是让最后一批工具结果无人问津
		messages = append(messages, llm.Text("user",
			"工具调用次数已达上限,请不要再调用工具。请基于以上已经获取到的信息直接给出回答;没有查到的部分如实说明。"))
		choice, err := a.llm.Chat(ctx, a.models, messages, nil)
		if err != nil {
			log.Printf("[agent] 收尾调用失败: %v", err)
		} else {
			final = choice.Message.String()
		}
	}

	if strings.TrimSpace(final) == "" {
		final = "这个问题我想了一会儿没想明白,换个问法试试?"
	}

	a.memMu.Lock()
	a.memory[chatID] = append(a.memory[chatID], llm.Text("assistant", final))
	a.memMu.Unlock()
	return final, nil
}

func (a *Agent) runTool(ctx context.Context, tc llm.ToolCall) string {
	var args map[string]any
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return "工具调用失败: 参数不是合法 JSON"
		}
	}
	log.Printf("[tool] %s %s", tc.Function.Name, tc.Function.Arguments)

	for _, p := range a.providers {
		if !p.Owns(tc.Function.Name) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		out, err := p.Call(cctx, tc.Function.Name, args)
		if err != nil {
			out = "工具执行失败: " + err.Error()
		}
		return truncate(out)
	}
	return "工具调用失败: 未知工具 " + tc.Function.Name
}

func truncate(s string) string {
	if runes := []rune(s); len(runes) > maxToolResult {
		return string(runes[:maxToolResult]) + "\n...(结果过长已截断)"
	}
	return s
}

func latest(msgs []llm.Message, n int) []llm.Message {
	if len(msgs) > n {
		return msgs[len(msgs)-n:]
	}
	return msgs
}
