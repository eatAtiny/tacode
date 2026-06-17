# agentic

一个最小可运行的 Go Agent 示例项目，包含：

- 基于 `go-openai` 的 LLM 调用封装
- 单 Agent 交互循环（命令行输入）
- 每轮结束自动记忆落盘
- Memory 仅保留最近 20 轮
- Prompt 模板封装（每轮统一格式）

## 功能概览

- `main` 作为入口，负责初始化组件并启动 Agent
- `Runner` 负责循环流程：读输入 -> 组装 Prompt -> 调 LLM -> 保存 Memory
- `LLM Client` 封装 OpenAI Chat Completions 调用
- `Memory Store` 使用 JSONL 文件存储并裁剪到最近 20 条
- `Prompt Builder` 生成每轮 system/user 提示词

## 目录结构

```text
agentic/
  main.go
  go.mod
  internal/
    agent/
      runner.go
    llm/
      openai.go
    memory/
      memory.go
    prompt/
      prompt.go
```

## 运行前准备

1. 安装 Go（建议 1.22+，按你的本地环境为准）
2. 配置环境变量：

```bash
export OPENAI_API_KEY="你的APIKey"
# 可选：自定义网关地址
# export OPENAI_BASE_URL="https://api.openai.com/v1"
# 可选：自定义模型
# export OPENAI_MODEL="gpt-4o-mini"
```

## 安装依赖

```bash
cd /home/ubuntu/workspace/agentic
go mod tidy
```

## 启动项目

```bash
go run .
```

可选参数：

```bash
go run . -memory ./data/memory.jsonl
```

- `-memory`：记忆文件路径（默认 `./data/memory.jsonl`）

## 交互说明

启动后会进入循环：

- 你输入一条任务
- Agent 调用 LLM 生成回答
- 本轮输入/输出会写入 memory
- 输入 `exit` 可退出

## Memory 机制

- 存储格式：JSONL（每行一条记录）
- 每条记录包含：轮次、时间、用户输入、模型输出
- 每次追加后自动裁剪，只保留最近 20 轮
- 每轮构建 Prompt 时，会读取最近记忆摘要作为上下文

## 常见问题

1. 提示 `OPENAI_API_KEY is required`
   - 说明没有设置 API Key，请先 `export OPENAI_API_KEY=...`

2. 请求失败或超时
   - 检查网络和 `OPENAI_BASE_URL` 是否可达

3. 想查看历史记忆
   - 直接打开 `-memory` 对应的 JSONL 文件即可

## 后续可扩展方向

- 增加非交互模式（传入初始任务自动多轮执行）
- 增加工具调用（Tool Use）
- 增加结构化输出（JSON Schema）
- 增加多 Agent 分工协作