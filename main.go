// larkrobotai - 飞书长连接智能体(Go 版,纯单二进制)
// 飞书消息 -> GLM(function calling) -> 飞书云文档工具 + 本地操作工具 -> 回复
// 配置见同目录 config.json
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"larkrobotai/internal/agent"
	"larkrobotai/internal/config"
	"larkrobotai/internal/feishu"
	"larkrobotai/internal/feishutools"
	"larkrobotai/internal/llm"
	"larkrobotai/internal/localtools"
)

func main() {
	//确保单实例
	release, ok := acquireSingleton()
	if !ok {
		log.Fatal("[fatal] 已有实例在运行,本次退出")
	}
	defer release()

	// 解析命令行
	configPath := flag.String("config", "", "配置文件路径,默认依次尝试工作目录和程序目录下的 config.json")
	logPath := flag.String("log", "", "日志文件路径,留空仅输出到控制台")
	flag.Parse()

	if *configPath == "" {
		log.Fatalf("[fatal] pleae input --config:")
	}
	if *logPath == "" {
		log.Fatalf("[fatal] pleae input --log:")
	}

	f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) //0o644表示创建文件时的权限 所有者读写、组和其他只读 rw-r--r--
	if err != nil {
		log.Fatalf("[fatal] 打开日志文件失败: %v", err)
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			log.Printf("[warn] 关闭日志文件失败: %v", err)
		}
	}(f)
	log.SetOutput(io.MultiWriter(os.Stderr, f))

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("[fatal] %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) //优雅关闭
	defer stop()

	// ---------- 消息通道 ----------
	bot := feishu.NewBot(cfg.Feishu.AppID, cfg.Feishu.AppSecret)

	// ---------- 本地操作工具(默认开启,零配置) ----------
	lt := localtools.New("", "", 0, 0) // workDir=程序目录,shell 按系统自动选,超时 120s
	providers := []agent.ToolProvider{lt}
	log.Printf("[local] 本地操作工具已启用,workDir=%s shell=%s(命令会记录到日志)", lt.WorkDir(), lt.Shell())

	// ---------- 飞书云文档工具(原生,无外部依赖) ----------
	providers = append(providers, feishutools.New(bot.Client()))

	// ---------- 大脑 ----------
	// 主模型用 apiKey;备用模型用 fallbackApiKey(未配置则沿用 apiKey),实现混合 key 降级
	specs := []llm.ModelSpec{{Model: cfg.LLM.Model, APIKey: cfg.LLM.APIKey}}
	for _, m := range cfg.LLM.FallbackModels {
		key := cfg.LLM.FallbackAPIKey
		if key == "" {
			key = cfg.LLM.APIKey
		}
		specs = append(specs, llm.ModelSpec{Model: m, APIKey: key})
	}
	ag := agent.New(llm.NewClient(cfg.LLM.BaseURL), providers, specs)
	if n := ag.InitTools(); n > 0 {
		log.Printf("[tools] 已接入 %d 个工具(本地+飞书)", n)
	}

	bot.OnMsg = func(_ context.Context, m feishu.Message) {
		log.Printf("[msg] id=%s chat=%s type=%s", m.MessageID, m.ChatID, m.MsgType)
		if m.MessageID == "" || !ag.FirstTime(m.MessageID) {
			return
		}
		if m.MsgType != "text" {
			reply(bot, m.ChatID, "我暂时只看得懂文字消息哦")
			return
		}
		if m.Text == "" {
			return
		}
		// 群聊里只响应 @机器人 的消息
		if m.ChatType == "group" {
			if m.MentionCount == 0 {
				return
			}
			if m.Text = feishu.StripMentions(m.Text); m.Text == "" {
				return
			}
		}
		// AI 调用可能很久:立即返回事件回调,单独 goroutine 处理
		go func() {
			tctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			text, err := ag.Handle(tctx, m.ChatID, m.Text)
			if err != nil {
				log.Printf("[error] %v", err)
				text = "出错了: " + truncate(err.Error(), 200)
			}
			reply(bot, m.ChatID, text)
		}()
	}

	log.Printf("[bot] larkrobotai 启动,模型=%s(备选 %v)", cfg.LLM.Model, cfg.LLM.FallbackModels)
	if err := bot.Run(ctx, cfg.Feishu.AppID, cfg.Feishu.AppSecret); err != nil && ctx.Err() == nil {
		log.Fatalf("[fatal] 长连接退出: %v", err)
	}
	log.Printf("[bot] 已退出,bye")
}

func reply(bot *feishu.Bot, chatID, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := bot.SendText(ctx, chatID, text); err != nil {
		log.Printf("[error] 回复失败: %v", err)
	}
}

// 截取字符串前n项
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
