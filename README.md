# larkrobotai
一个飞书机器人连接本地AI模型的程序。

用户在飞书里给机器人发消息 → 机器人通过 **WebSocket 长连接**把消息推到本机 → 本机调用 GLM（OpenAI 兼容接口，支持 function calling）→ AI 调用**原生飞书工具**（云文档/知识库/表格/多维表格，直接走官方 Go SDK）和**本地操作工具**（命令执行/文件读写）→ 结果回复给用户。**纯单二进制，零外部依赖**（不需要 Node.js），全程只需要出站网络，无需公网 IP。

原 Node.js 版（`C:\Users\Administrator\feishu-bot\bot.js`）的功能等价重写，旧版已停用但保留。

## 目录结构

```
larkrobotai/
├── main.go                    入口:配置加载、单实例锁、工具装配、信号处理
├── config.json                运行配置(含密钥,不进版本库)
├── config.example.json        配置模板
└── internal/
    ├── config/                配置解析
    ├── feishu/                飞书长连接事件接收 + 发消息 API
    ├── llm/                   OpenAI 兼容 chat/completions 客户端(多模型降级重试)
    ├── feishutools/            原生飞书工具:读文档/知识库/表格/多维表格(走官方 SDK)
    ├── localtools/             本地操作工具:命令执行/文件读写(本机代理模式)
    └── agent/                 会话记忆、消息去重、function calling 循环
```

## 构建与运行

```bash
cd G:\program\larkrobotai
go build -o larkrobotai.exe .
```

```bash
# 前台运行(Ctrl+C 退出);默认依次在 工作目录 → exe 同目录 找 config.json
larkrobotai.exe

# 指定配置 / 同时落盘日志
larkrobotai.exe -config D:\path\config.json -log bot.log
```

启动成功标志（控制台或日志）：

```
[tools] 已接入 11 个工具(本地+飞书)
[bot] WebSocket 长连接已建立,等待消息...
```

## 配置说明（config.json）

```jsonc
{
  "feishu": { "appId": "cli_xxx", "appSecret": "xxx" },
  "llm": {
    "baseUrl": "https://.../api",        // OpenAI 兼容接口,自动拼 /chat/completions
    "apiKey": "xxx",                     // 主模型的 key
    "model": "glm-5.3-flash",            // 主模型
    "fallbackModels": ["glm-4.7-flash"], // 降级模型,按顺序尝试
    "fallbackApiKey": ""                 // 备用模型单独的 key;留空则与 apiKey 相同
  }
}
```

降级策略：任何错误都会按顺序换下一个模型重试；429/5xx 先退避重试当前模型；401/403 等认证错误立即切换。利用 `fallbackApiKey` 可以实现"主模型走 Coding Plan key、降级换标准 key 的免费模型"的混合方案，套餐失效机器人也不会瘫。

程序不内置任何提示词，也不发送 system 消息——模型的行为完全由工具定义自描述驱动；想加人设或行为约束需要改代码（agent 的 messages 组装处）。

## 飞书云文档工具（原生，7 个）

| 工具 | 用途 |
|---|---|
| `feishu_read_doc` | 读云文档全文，直接喂链接（docx/wiki 链接自动解析） |
| `feishu_get_wiki_node` | 查知识库节点信息 |
| `feishu_sheet_meta` / `feishu_sheet_find` | 表格元数据 / 按关键字找单元格 |
| `feishu_bitable_tables` / `feishu_bitable_fields` / `feishu_bitable_records` | 多维表格：表清单 / 字段清单 / 记录搜索 |

这些工具走**应用身份（tenant_access_token）**，使用前需要在开发者后台完成一次性配置：

1. 给应用开通只读权限：`docx:document:readonly`、`wiki:wiki:readonly`、`sheets:spreadsheet`、`bitable:app:readonly`，发布版本
2. 让应用"看得见"想读的内容：文档/表格把**应用加为协作者**（或组织内链接可见），知识库空间把**机器人加为成员**

对比旧 lark-mcp 方案：旧方案走你个人身份（user token）能读你所有文档，但依赖 Node.js 且令牌过期要重新授权；原生方案零依赖、令牌自动管理，代价是要做一次上面的授权配置。

## 内建行为

- **单实例**：全局命名互斥量 `Global\larkrobotai-singleton`，重复启动立即退出
- **去重**：飞书事件至少一次投递，按 message_id 幂等
- **群聊**：只响应 @机器人 的消息，回复前剔除 @占位符
- **记忆**：每会话保留最近 20 条；系统提示词自动注入当前北京时间
- **工具循环**：单次提问最多 6 轮 function calling，工具结果截断 8000 字符，单工具 120s 超时
- **降级**：任何错误按顺序换下一个模型；认证类错误立即切换。`fallbackApiKey` 支持"主模型走 Coding Plan key、降级换标准 key 免费模型"的混合方案
- **消息之外**：非文本消息统一回复"我暂时只看得懂文字消息哦"

## ⚠️ 本地代理模式（默认开启，无配置项）

机器人默认挂载 4 个本机操作工具（跑在哪台机器就操作哪台）：

| 工具 | 能力 |
|---|---|
| `run_command` | 执行命令（Windows: cmd/PowerShell；Linux: sh/bash，自动选择），返回 exit code 与输出 |
| `read_file` | 读文本文件，按行分页（单文件上限 10MB） |
| `write_file` | 写/追加文件（目录不存在自动创建） |
| `list_directory` | 列目录 |

- 固定参数：工作目录 = 程序目录，单命令超时 120s，输出截断 8000 字符
- 命令以**运行机器人的系统账户权限**执行（本地为你的 Windows 账户，云上 systemd 默认 root），等于把那台机器的 shell 交给了能和机器人对话的人
- 每条命令、每次写文件都会记入日志（`[cmd]` / `[write]` 前缀），可审计
- 访问控制靠飞书侧：应用可用范围只放行自己人，别拉进陌生群组
- 如需禁用：删除 `main.go` 中 providers 里的 localtools 注册并重新编译

## 注册为 Windows 服务（可选）

当前为手动启动。若想恢复 NSSM 服务方式：

```bash
cd C:\Users\Administrator\feishu-bot\bin

# 旧服务还指向 Node 版,改指到新 exe
nssm.exe set FeishuBot Application G:\program\larkrobotai\larkrobotai.exe
nssm.exe set FeishuBot AppDirectory G:\program\larkrobotai
nssm.exe set FeishuBot AppStdout G:\program\larkrobotai\bot-service.log
nssm.exe set FeishuBot AppStderr G:\program\larkrobotai\bot-service.log
nssm.exe set FeishuBot Start SERVICE_AUTO_START   # 恢复开机自启(当前是手动)
nssm.exe start FeishuBot
```

不想用服务就直接前台跑：`G:\program\larkrobotai\larkrobotai.exe`。

## 从 Node 版迁移的差异

| 项 | Node 版 (feishu-bot) | Go 版 (larkrobotai) |
|---|---|---|
| 语言/体积 | Node.js,依赖 node_modules | 单 exe (~13MB) |
| 单实例锁 | bot.pid 文件 | Windows 全局互斥量(无残留问题) |
| 提示词 | 内置人设+时间注入 | 无,纯靠工具定义自描述 |
| 飞书文档工具 | lark-mcp 子进程(需 Node.js,user token) | 原生 Go SDK(零依赖,tenant token) |
| 部署物 | node_modules + 脚本 | 单 exe + config.json |
| 提示词时间 | zh-CN 本地时间 | 固定 Asia/Shanghai |
