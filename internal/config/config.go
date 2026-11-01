// Package config 负责加载 config.json
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Feishu struct {
	AppID     string `json:"appId"`
	AppSecret string `json:"appSecret"`
}

type LLM struct {
	BaseURL        string   `json:"baseUrl"`
	APIKey         string   `json:"apiKey"`
	Model          string   `json:"model"`
	FallbackModels []string `json:"fallbackModels"`
	// FallbackAPIKey 备用模型用的 key;留空则与 apiKey 相同。
	// 用于"主模型走套餐 key、降级换标准 key 的免费模型"这类混合场景
	FallbackAPIKey string `json:"fallbackApiKey"`
}

type Config struct {
	Feishu Feishu `json:"feishu"`
	LLM    LLM    `json:"llm"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	if c.Feishu.AppID == "" || c.Feishu.AppSecret == "" {
		return nil, fmt.Errorf("配置缺少 feishu.appId / feishu.appSecret")
	}
	if c.LLM.BaseURL == "" || c.LLM.Model == "" {
		return nil, fmt.Errorf("配置缺少 llm.baseUrl / llm.model")
	}
	return &c, nil
}
