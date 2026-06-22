# agentic

一个最小可运行的 Go ReAct Agent 示例项目，包含：

- 基于 `go-openai` 的 LLM 调用封装
- ReAct 推理循环（自动判断是否需要调用工具）
- 内置工具：Shell 命令执行、文件读写
- 多会话管理，每个会话独立记忆，支持交互式切换
- 终端美化输出（Lip Gloss + Glamour）

## 功能概览

- `main` 作为入口，负责初始化组件并启动 Agent
- `Runner` 负责 REPL 循环：读输入 -> ReAct 推理 -> 保存记忆
- `LLM Client` 封装 OpenAI Chat Completions 调用（含 Function Calling）
- `Memory Store` 使用 JSONL 文件存储每轮对话，自动裁剪到最近 20 条
- `Session Manager` 管理多个独立会话，支持创建、切换、删除、重命名
- `Session Picker` 基于 Bubble Tea 的交互式会话选择器（上下箭头选择，Enter 确认）
- `Tool Registry` 工具注册与调度，支持 Shell 和 File 两种内置工具
- `Prompt Builder` 生成 ReAct 格式的 system/user 提示词

## 目录结构

```text
agentic/
  main.go                              # 入口，初始化各组件
  go.mod
  internal/
    agent/
      runner.go                        # REPL 循环 + ReAct 编排
    llm/
      openai.go                        # OpenAI Chat Completions 封装
    memory/
      memory.go                        # JSONL 对话存储
    prompt/
      prompt.go                        # 提示词模板
    session/
      session.go                       # 会话管理器（SessionManager）
      picker.go                        # 交互式会话选择器（Bubble Tea）
    tool/
      tool.go                          # Tool 接口 + Registry
      shell.go                         # Shell 工具（执行 bash 命令）
      file.go                          # File 工具（读写文件）
```

## 运行前准备

1. 安装 Go（建议 1.22+）
2. 配置环境变量（二选一）：

**方式一：Shell 环境变量**

```bash
export OPENAI_API_KEY="你的APIKey"
# 可选：自定义网关地址
# export OPENAI_BASE_URL="https://api.openai.com/v1"
# 可选：自定义模型
# export OPENAI_MODEL="gpt-4o-mini"
```

**方式二：`.env` 文件**

```bash
# 项目根目录创建 .env 文件
OPENAI_API_KEY=你的APIKey
# OPENAI_BASE_URL=https://api.openai.com/v1
# OPENAI_MODEL=gpt-4o-mini
```

## 安装依赖

```bash
go mod tidy
```

## 启动项目

```bash
go run .
```

可选参数：

```bash
go run . -sessions ./data/sessions   # 自定义会话存储目录
go run . -session <会话ID>            # 恢复指定会话
go run . -env .env                    # 自定义 env 文件路径
```

## 交互说明

启动后进入 REPL 循环：

- 输入任务，Agent 自动判断是否需要调用工具
- 需要工具时进入 ReAct 推理循环（最多 10 步），逐步执行并思考
- 输入 `exit` 退出

### 会话管理命令

| 命令 | 功能 |
|------|------|
| `/new [名称]` | 创建新会话并切换过去 |
| `/list` | 打开交互式会话选择器（↑↓ 选择，Enter 切换，Esc 取消） |
| `/switch <ID>` | 按会话 ID 前缀切换 |
| `/delete <ID>` | 删除指定会话（不能删除当前活跃的） |
| `/rename <名称>` | 重命名当前会话 |
| `/current` | 显示当前会话信息 |

### 会话存储

每个会话是独立的 JSONL 文件，互不干扰：

```text
data/sessions/
  manifest.json                  # 会话索引 + 当前活跃会话
  20260621-103000-a1b2c3d4.jsonl  # 会话 A 的对话记录
  20260621-110000-e5f6g7h8.jsonl  # 会话 B 的对话记录
```

## Memory 机制

- 存储格式：JSONL（每行一条记录）
- 每条记录包含：轮次、时间、用户输入、模型输出
- 每次追加后自动裁剪，只保留最近 20 轮
- 每轮构建 Prompt 时，会读取最近记忆摘要作为上下文

## ReAct 推理流程

Agent 不会简单地把每个问题都转发给 LLM。每次用户输入：

1. 调用 LLM（附带工具定义），如果 LLM 直接返回文本 → 完成
2. 如果 LLM 请求调用工具 → 进入 ReAct 循环（最多 10 轮）：执行工具，将结果反馈给 LLM，重复直到 LLM 给出最终答案

## 内置工具

| 工具 | 功能 | 限制 |
|------|------|------|
| `shell` | 执行 bash 命令 | 30 秒超时 |
| `file` | 读写文件 | 读取上限 8KB |

添加新工具：实现 `tool.Tool` 接口，在 `main.go` 中注册。

## 常见问题

1. 提示 `OPENAI_API_KEY is required`
   - 说明没有设置 API Key，请先 `export OPENAI_API_KEY=...` 或写入 `.env` 文件

2. 请求失败或超时
   - 检查网络和 `OPENAI_BASE_URL` 是否可达

3. 想查看某个会话的历史记录
   - 直接打开 `data/sessions/` 下对应的 JSONL 文件即可

## 后续可扩展方向

- 增加非交互模式（传入初始任务自动多轮执行）
- 增加更多工具（网络请求、数据库查询等）
- 增加结构化输出（JSON Schema）
- 增加多 Agent 分工协作
