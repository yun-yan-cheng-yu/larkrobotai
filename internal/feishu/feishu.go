// Package feishu 封装飞书长连接事件接收与消息发送
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

var mentionRe = regexp.MustCompile(`@_user_\d+`)

// StripMentions 去掉文本中的 @占位符(如 @_user_1)
func StripMentions(text string) string {
	return strings.TrimSpace(mentionRe.ReplaceAllString(text, ""))
}

type Message struct {
	MessageID    string
	ChatID       string
	ChatType     string // p2p / group
	MsgType      string
	Text         string // text 消息的原始文本(群聊的 @占位符未清理)
	MentionCount int
}

type Bot struct {
	api *lark.Client
	// OnMsg 收到消息事件的回调;启动后可随时赋值
	OnMsg func(context.Context, Message)
}

func NewBot(appID, appSecret string) *Bot {
	return &Bot{api: lark.NewClient(appID, appSecret)}
}

// Client 返回底层 API 客户端(供其他工具包复用同一份凭据与 token 缓存)
func (b *Bot) Client() *lark.Client { return b.api }

// Run 建立长连接并阻塞,直到 ctx 取消
func (b *Bot) Run(ctx context.Context, appID, appSecret string) error {
	ed := dispatcher.NewEventDispatcher("", "")
	ed.OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
		if event.Event == nil || event.Event.Message == nil {
			return nil
		}
		m := event.Event.Message
		text := ""
		if m.Content != nil {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(*m.Content), &payload); err == nil {
				text = strings.TrimSpace(payload.Text)
			}
		}
		mentions := 0
		for _, mt := range m.Mentions {
			if mt != nil && mt.Key != nil && *mt.Key != "" {
				mentions++
			}
		}
		if b.OnMsg != nil {
			b.OnMsg(ctx, Message{
				MessageID:    deref(m.MessageId),
				ChatID:       deref(m.ChatId),
				ChatType:     deref(m.ChatType),
				MsgType:      deref(m.MessageType),
				Text:         text,
				MentionCount: mentions,
			})
		}
		return nil
	})

	ws := larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(ed),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithOnReady(func() { log.Printf("[bot] WebSocket 长连接已建立,等待消息...") }),
	)
	return ws.Start(ctx)
}

// SendText 给指定会话发一条文本消息
func (b *Bot) SendText(ctx context.Context, chatID, text string) error {
	msgType := "text"
	content, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return err
	}
	contentStr := string(content)
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(&larkim.CreateMessageReqBody{
			ReceiveId: &chatID,
			MsgType:   &msgType,
			Content:   &contentStr,
		}).
		Build()
	resp, err := b.api.Im.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("发送消息失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
