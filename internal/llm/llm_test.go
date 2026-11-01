package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func newTestServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL), srv
}

func writeOK(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
}

// 主模型认证失败应立即切换到备用模型(可能携带另一把 key),而不是直接报错
func TestChatFallsThroughToFallbackKey(t *testing.T) {
	var m2Calls int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		switch key {
		case "Bearer bad":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"1000","message":"身份验证失败。"}}`))
		case "Bearer good":
			atomic.AddInt32(&m2Calls, 1)
			writeOK(t, w)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	})

	specs := []ModelSpec{{Model: "primary", APIKey: "bad"}, {Model: "fallback", APIKey: "good"}}
	choice, err := c.Chat(context.Background(), specs, []Message{Text("user", "hi")}, nil)
	if err != nil {
		t.Fatalf("期望降级成功,实际报错: %v", err)
	}
	if choice.Message.String() != "ok" {
		t.Fatalf("期望回复 ok,实际 %q", choice.Message.String())
	}
	if atomic.LoadInt32(&m2Calls) != 1 {
		t.Fatalf("备用模型应恰好被调用 1 次,实际 %d", m2Calls)
	}
}

// 429 应退避重试后成功
func TestChatRetriesOn429(t *testing.T) {
	var calls int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"1305","message":"该模型当前访问量过大"}}`))
			return
		}
		writeOK(t, w)
	})

	specs := []ModelSpec{{Model: "m", APIKey: "k"}}
	choice, err := c.Chat(context.Background(), specs, []Message{Text("user", "hi")}, nil)
	if err != nil {
		t.Fatalf("期望重试后成功,实际报错: %v", err)
	}
	if choice.Message.String() != "ok" {
		t.Fatalf("期望回复 ok,实际 %q", choice.Message.String())
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("应共调用 2 次,实际 %d", calls)
	}
}
