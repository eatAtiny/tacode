# Agentic 实现文档

Go ReAct Agent 的设计与实现文档。参照 Claude Code 的设计模式，用 Go 语言实现。

## 文档索引

| 章节 | 文件 | 内容 |
|------|------|------|
| 1. Agent 循环 | [01-agent-loop.md](01-agent-loop.md) | 两层查询架构、ReAct 核心、流式调用、并发工具执行、权限系统、中断机制 |
| 2. 工具系统 | [02-tools.md](02-tools.md) | Tool 接口、5 个内置工具、并发分类、ReadState、大结果持久化、ToolHook |
| 3. 记忆系统 | [03-memory.md](03-memory.md) | 三层记忆架构、上下文构建、记忆提取、自动压缩 |
| 4. UI 系统 | [04-ui.md](04-ui.md) | 可插拔 UI、BubbleUI 终端实现、TextUI 无头实现、组件系统 |

## 与 Claude Code 的对应关系

Claude Code 参考文档在 `docs/learn/`，实现文档在 `docs/impl/`：

| Claude Code 章节 | 对应实现 |
|------------------|---------|
| 01-agent-loop.md | `internal/agent/runner.go` + `query_engine.go` + `query_loop.go` |
| 02-tools.md | `internal/tool/tool.go` + 5 个工具文件 |
| (N/A) | `internal/memory/` — 三层记忆（Agentic 特有） |
| (N/A) | `internal/ui/` — 可插拔 UI（Agentic 特有） |

## 项目结构

```
main.go                          # 入口：组装依赖 + 启动 REPL
internal/
  agent/                         # Agent 核心
    runner.go                    # REPL 主循环
    query_engine.go              # 查询协调层
    query_loop.go                # ReAct 核心循环
    types.go                     # 事件类型
    permission.go                # 权限检查
    memory.go                    # 记忆命令 (/compress, /memory)
    session.go                   # 会话命令 (/new, /switch, /delete)
  llm/
    openai.go                    # OpenAI API 封装（Chat + ChatWithToolsStream）
  memory/
    types.go                     # 数据类型
    event.go                     # EventStore (真相源)
    history.go                   # HistoryStore (L1)
    summary_store.go             # SummaryStore (L2)
    store.go                     # MemoryStore (L3)
    extractor.go                 # LLM 记忆提取
    retriever.go                 # 三层检索
  prompt/
    prompt.go                    # ReAct prompt 构建
  session/
    session.go                   # 会话 CRUD
    picker.go                    # 交互式会话选择器
  tool/
    tool.go                      # Tool 接口 + Registry + TruncateResult
    shell.go                     # Shell 工具
    file.go                      # File 工具
    edit.go                      # Edit 工具
    grep.go                      # Grep 工具
    list.go                      # List 工具
  ui/
    ui.go                        # UI 接口
    bubble/bubble.go             # BubbleUI 终端实现
    text/text.go                 # TextUI 无头实现
    components/                  # 可复用 Bubble Tea 组件
```

## 参考

- Claude Code 技术文档：`docs/learn/`
- 项目规范：`CLAUDE.md`
