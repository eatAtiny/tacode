# agentic

一个 Go ReAct Agent，具备三层记忆系统、流式 LLM 交互、可插拔 UI 和多会话管理。

## 功能概览

- **ReAct 推理循环** — 自动判断是否需要调用工具，循环执行直到获得足够信息
- **流式输出** — 实时显示 LLM 思考过程和增量文本（支持 OpenAI Stream API）
- **三层记忆系统** — L1 原始对话日志 / L2 摘要 / L3 结构化记忆，自动提取和压缩
- **异步事件驱动** — QueryEngine 层协调，queryLoop 通过 channel yield 中间事件
- **可插拔 UI** — `ui.UI` 接口，内置 BubbleUI（终端美化）和 TextUI（headless 模式）
- **权限系统** — 危险工具（shell/file）需用户确认，支持自定义权限检查器
- **多会话管理** — 每个会话独立目录存储，支持创建、切换、删除、重命名
- **临时会话** — 启动时不立即落盘，首次对话后才持久化到 manifest

## 目录结构

```text
agentic/
  main.go                              # 入口：初始化组件并启动 Agent
  go.mod
  internal/
    agent/
      runner.go                        # Runner：REPL 主循环 + 异步查询调度
      query_engine.go                  # QueryEngine：上下文构建、提示词组装、事件消费
      query_loop.go                    # queryLoop：核心 ReAct 循环（异步生成器模式）
      types.go                         # QueryEvent / QueryResult 类型定义
      permission.go                    # 工具权限检查（deny/allow/confirm）
      memory.go                        # 记忆提取和手动管理（/memory /compress）
      session.go                       # 会话命令处理 + 临时会话持久化
      compactor.go                     # s08 四步压缩管线（Go 移植，配对保护）
      compact_tool.go                  # 模型主动 compact 工具
      balance.go                       # /balance 命令：余额查询辅助 + 格式化 + 每轮展示
    llm/
      openai.go                        # OpenAI 客户端封装（Chat + ChatWithToolsStream）
      balance.go                       # DeepSeek 余额查询（/user/balance）
      retry.go                         # LLM 请求重试（429/5xx/网络错误）
    memory/
      types.go                         # MemoryEntry / Summary / Record / Event 类型
      history.go                       # L1 HistoryStore：原始对话 JSONL 存储
      summary_store.go                 # L2 SummaryStore：对话摘要存储
      store.go                         # L3 MemoryStore：结构化记忆文件（.md + frontmatter）
      event.go                         # EventStore：全量事件日志（真相源，永不截断）
      extractor.go                     # Extractor：LLM 驱动记忆提取
      retriever.go                     # Retriever：三层检索 + 上下文构建 + 自动压缩
    prompt/
      prompt.go                        # ReAct System/User 提示词模板
    session/
      session.go                       # SessionManager：会话 CRUD + manifest 管理
      picker.go                        # 交互式会话选择器（Bubble Tea）
    tool/
      tool.go                          # Tool 接口 + Registry（生成 Function Calling 定义）
      shell.go                         # Shell 工具（执行 bash 命令，30s 超时）
      file.go                          # File 工具（读写文件，8KB 读取上限）
    ui/
      ui.go                            # UI 接口定义（ReadInput / OnThink / OnDelta / OnToolCall ...）
      bubble/
        bubble.go                      # BubbleUI：终端美化 UI（Lip Gloss + Glamour 渲染）
        bubble_test.go                 # BubbleUI 测试
      text/
        text.go                        # TextUI：headless 模式（OnEvent 回调转发）
      components/
        conversation.go                # 对话历史组件（滚动、Markdown 渲染）
        input.go                       # 输入组件
        status.go                      # 状态栏组件
        toolview.go                    # 工具调用视图组件
      integration_test.go              # UI 集成测试
```

## 运行前准备

### 1. 环境要求

- Go 1.26+
- OpenAI API Key（或兼容的代理地址）

### 2. 配置环境变量

**方式一：Shell 环境变量**

```bash
export OPENAI_API_KEY="你的APIKey"
# 可选：
# export OPENAI_BASE_URL="https://api.openai.com/v1"
# export OPENAI_MODEL="gpt-4o-mini"
# export OPENAI_CONTEXT_LIMIT=128000
```

**方式二：`.env` 文件**

```bash
# 项目根目录创建 .env 文件
OPENAI_API_KEY=你的APIKey
# OPENAI_BASE_URL=https://api.openai.com/v1
# OPENAI_MODEL=gpt-4o-mini
```

### 3. 安装依赖

```bash
go mod tidy
```

### 4. 启动

```bash
go run .
```

可选参数：

```bash
go run . -sessions ./data/sessions   # 自定义会话存储目录
go run . -session <会话ID>            # 恢复指定会话
go run . -env .env                    # 自定义 env 文件路径
```

## 架构设计

### 依赖关系

```
main.go
  └─ internal/agent  (runner.go, query_engine.go, query_loop.go, types.go, permission.go,
  │                    memory.go, session.go, compactor.go, compact_tool.go, balance.go) — REPL 循环 + ReAct 编排
       ├─ internal/llm        (openai.go, balance.go, retry.go) — OpenAI 客户端（流式 + 非流式）+ 余额查询 + 重试
       ├─ internal/memory     (7 个文件) — 三层记忆存储 + 检索 + 提取
       ├─ internal/prompt     (prompt.go) — ReAct 提示词模板
       ├─ internal/session    (session.go, picker.go) — 会话管理 + 选择器
       ├─ internal/tool       (tool.go, shell.go, file.go) — 工具注册 + 实现
       └─ internal/ui         (ui.go) — UI 接口
            ├─ ui/bubble/     — BubbleUI 终端实现
            ├─ ui/text/       — TextUI headless 实现
            └─ ui/components/ — 可复用 UI 组件
```

### 两层查询架构

```
QueryEngine 层 (query_engine.go)
  ├── 构建上下文（三层记忆检索 + 自动压缩）
  ├── 构建 System/User Prompt
  ├── 调用 queryLoop 获取 event channel
  ├── 消费事件 → 通知 UI + 记录事件日志
  └── 返回最终答案

queryLoop 层 (query_loop.go)
  ├── while(true) 循环
  ├── 调用 LLM（流式） → yield Think / Delta 事件
  ├── 检查 tool_use → 执行工具 → yield ToolCall / ToolResult 事件
  ├── 权限检查 → yield Permission 事件（阻塞等待确认）
  ├── 重复调用检测 + 上下文压缩
  └── 最终回答 → yield Final 事件
```

### 三层记忆系统

| 层级 | 存储 | 文件 | 说明 |
|------|------|------|------|
| L1 | HistoryStore | `history.jsonl` | 原始对话记录，保留最近 50 轮 |
| L2 | SummaryStore | `summaries.jsonl` | LLM 提取的对话摘要，支持自动压缩合并 |
| L3 | MemoryStore | `memory/*.md` | 结构化长期记忆（frontmatter + 正文），按重要性排序 |

**记忆检索流程**：
1. L3 记忆索引 + 高重要性记忆内容
2. L2 最近 10 条摘要
3. L2 为空时降级到 EventStore（事件日志摘要）→ HistoryStore（原始日志摘要）

**自动压缩**：当上下文 token 用量超过模型窗口的 80%，自动调用 LLM 合并旧摘要。

### ReAct 推理流程

```
用户输入
  → QueryEngine 构建上下文（三层记忆检索）
  → queryLoop 异步执行：
     1. 调用 LLM（流式），实时 yield 增量文本
     2. 如果没有工具调用 → 返回最终答案
     3. 如果有工具调用 → 权限检查 → 执行工具 → yield 结果
     4. 结果反馈给 LLM → 重复步骤 1（最多 10 轮）
  → QueryEngine 消费事件，通知 UI
  → 后台提取 L2 摘要和 L3 记忆
  → 首次对话后临时会话自动落盘
```

## 内置工具

| 工具 | 功能 | 限制 |
|------|------|------|
| `shell` | 执行 bash 命令 | 30 秒超时，危险命令需确认 |
| `file` | 读写文件 | 读取上限 8KB，写操作需确认 |

添加新工具：实现 `tool.Tool` 接口，在 `main.go` 中注册。

## 交互说明

启动后进入 REPL 循环：

- 输入任务，Agent 自动判断是否需要调用工具
- 需要工具时进入 ReAct 推理循环（最多 10 步），逐步执行并思考
- 流式显示 LLM 思考过程
- 危险工具（shell/file）会弹出权限确认提示
- 查询运行中输入 `/stop` 中断当前查询
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
| `/compress` | 手动触发摘要压缩（合并旧摘要） |
| `/balance` | 查询 DeepSeek 账户余额；成功后每轮对话结束自动展示剩余额度 |
| `/memory` | 列出所有 L3 记忆 |
| `/memory add <内容>` | 手动添加一条记忆 |
| `/memory rm <name>` | 删除一条记忆 |
| `/stop` | 中断正在运行的查询 |

### 会话存储

每个会话是独立的子目录，包含多层数据：

```text
data/sessions/
  manifest.json                       # 会话索引 + 当前活跃会话
  20260621-103000-a1b2c3d4/           # 会话 A
    history.jsonl                     # L1 原始对话记录
    summaries.jsonl                   # L2 对话摘要
    events.jsonl                      # 全量事件日志（真相源）
    memory/                           # L3 结构化记忆
      MEMORY.md                       # 记忆索引
      preference-go-lang.md           # 单条记忆
      project-architecture.md         # 单条记忆
  20260621-110000-e5f6g7h8/           # 会话 B
    ...
```

## UI 系统

项目定义了 `ui.UI` 接口，所有 UI 操作都通过此接口完成，实现可插拔：

- **BubbleUI** (`ui/bubble/`)：终端美化 UI，使用 Lip Gloss 样式 + Glamour Markdown 渲染，支持流式文本、工具调用框线、权限确认
- **TextUI** (`ui/text/`)：headless 模式，通过 `OnEvent` 回调将事件转发给上层，适用于子 agent 场景
- **Components** (`ui/components/`)：可复用的 Bubble Tea 组件（对话历史、输入栏、状态栏、工具视图）

## 权限系统

- 默认权限检查器将 `shell` 和 `file` 标记为危险工具，需要用户确认
- 高风险操作（如 `rm -rf`、`chmod 777`、文件写操作）标记为需要确认
- 可通过 `SetPermissionChecker` 注入自定义权限策略
- 权限确认通过 UI 接口的 `ConfirmPermission` 方法完成

## 常见问题

1. 提示 `OPENAI_API_KEY is required`
   - 说明没有设置 API Key，请先 `export OPENAI_API_KEY=...` 或写入 `.env` 文件

2. 请求失败或超时
   - 检查网络和 `OPENAI_BASE_URL` 是否可达

3. 想查看某个会话的历史记录
   - 直接打开 `data/sessions/<id>/events.jsonl` 查看全量事件日志
   - 或打开 `data/sessions/<id>/history.jsonl` 查看原始对话

## 后续可扩展方向

- 增加非交互模式（传入初始任务自动多轮执行）
- 增加更多工具（网络请求、数据库查询等）
- 增加结构化输出（JSON Schema）
- 增加多 Agent 分工协作
- 扩展权限系统（基于用户角色、资源路径的权限控制）
- 支持更多 LLM 提供商（Anthropic、本地模型等）
