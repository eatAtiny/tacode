# 项目上下文注入（AGENTS.md）设计文档

- 日期：2026-08-18
- 状态：已批准
- 范围：`internal/memory/retriever.go`（加载 + 缓存 + 注入）+ `internal/agent/session.go`（`/reload` 命令）+ `main.go`（启动加载）+ 测试 + CLAUDE.md

## 背景

Agent 每轮查询由 `Retriever.BuildContext(query)`（`internal/memory/retriever.go:73`）构建上下文，但只检索三层记忆。agent 对"我在哪个仓库、仓库约定是什么"一无所知——这是 Coding Agent 与通用 chatbot 的本质区别。本功能让 agent 自动读取仓库的 AGENTS.md 指令文件，提升工具调用质量。

## 决策

| 决策点 | 结论 |
|--------|------|
| 加载位置 | 从进程工作目录向上查找，到仓库根（`.git` 所在）停止 |
| 文件选择 | **只读 AGENTS.md / AGENTS**（无扩展名变体），不读 CLAUDE.md |
| 合并策略 | 不合并多级，取最近一级的指令文件（找到即停） |
| 缓存策略 | 启动加载一次 + `/reload` 命令手动刷新 |
| 注入位置 | `BuildContext` 新增 `## 项目指令` 部分，放在记忆之前 |
| 截断 | memory 包内自实现（> 64KB 截断），不引入 tool 依赖 |
| 失败处理 | 找不到/读失败 → 静默跳过，不影响现有行为 |

## 架构（方案 A）

```
main.go 启动
  └─ retriever.LoadProjectInstructions()   ← 从 cwd 向上找 AGENTS.md，读到缓存
       └─ findProjectInstructions()         ← 逐级向上，到 .git 所在目录停止
            └─ 每目录检查 AGENTS.md / AGENTS

每轮查询
  queryEngine → retriever.BuildContext(query)
    └─ 步骤 0（新增）: 项目指令非空 → parts 前置 "## 项目指令\n<content>"
    └─ 步骤 1-4: 原有记忆检索（不变）

REPL
  /reload → ClearProjectInstructions() + LoadProjectInstructions()
```

## Retriever 扩展

```go
type Retriever struct {
	history *HistoryStore
	summary *SummaryStore
	memory  *MemoryStore
	events  *EventStore
	projectInstr   string // 项目指令内容（缓存），空 = 未加载
	projectInstrSrc string // 来源路径（调试/显示用）
}

func (r *Retriever) LoadProjectInstructions() error  // 从 cwd 加载并缓存
func (r *Retriever) ClearProjectInstructions()        // 清空缓存
```

`BuildContext` 步骤 0：

```go
// ── 步骤 0: 项目指令（AGENTS.md） ──
if r.projectInstr != "" {
	parts = append(parts, "## 项目指令\n"+r.projectInstr)
}
```

**注入无 error**：项目指令缺失不报错、静默跳过；`LoadProjectInstructions` 启动时执行，失败只警告不退出。`BuildContext` 签名不变（保持 `(string, error)`）。

## 查找逻辑（findProjectInstructions）

```go
// findProjectInstructions 从 startDir 向上查找 AGENTS.md / AGENTS 文件。
// 到包含 .git 的目录（仓库根）停止，不继续向上。
// 返回找到的文件路径和内容；未找到返回 ("", nil)。
func findProjectInstructions(startDir string) (string, string, error)
```

规则：
- 候选文件名：`AGENTS.md`、`AGENTS`（无扩展名），每级目录按此顺序检查
- 找到即停（不合并多级，只取最近一级）
- 向上到达含 `.git` 的目录后停止（**包括**该目录本身——仓库根的 AGENTS.md 要读）；若一直没遇到 `.git`，到文件系统根停止
- 文件 > 64KB 截断（memory 包内自实现 `truncateProjectInstr`，复用 `EstimateTokens` 思路的 head+tail 保留）

## 依赖与循环风险

已验证：`tool` 包零内部依赖（`grep internal/tool/*.go` 无 `agentic/internal` import）；`memory` 只依赖 `llm`。`memory → tool` 虽不构成严格循环（tool 不 import memory），但会破坏 leaf 独立性——**截断在 memory 包内自实现**，不引入 tool 依赖。

## /reload REPL 命令

- `internal/agent/session.go` 的 `handleSessionCommand`（`session.go:103`）加分支：
  `/reload` → `ClearProjectInstructions()` + `LoadProjectInstructions()` → UI 提示 "✅ 已重新加载项目指令"
- 沿袭 `/compress`、`/memory` 的既有模式
- `Runner.Run` 的未知命令提示（`runner.go` 中 `"未知命令，可用: ..."` 字符串）加 `/reload`
- `LoadProjectInstructions` 内部用 `os.Getwd()` 作为起始目录

## 测试计划

| 测试 | 覆盖点 |
|------|--------|
| `TestFindProjectInstructions_Nearest` | 多级 AGENTS.md → 取最近一级 |
| `TestFindProjectInstructions_GitRoot` | 到 .git 停止，不越过仓库根 |
| `TestFindProjectInstructions_NotFound` | 无 AGENTS.md → 返回空 |
| `TestFindProjectInstructions_BareVariant` | AGENTS（无扩展名）可被识别 |
| `TestRetriever_BuildContext_WithInstructions` | 注入后 BuildContext 含 `## 项目指令` |
| `TestRetriever_BuildContext_NoInstructions` | 未加载 → 行为与现状一致 |
| `TestLoadProjectInstructions_Refresh` | 加载后改文件 → Clear+Load → 内容更新 |
| `TestTruncateProjectInstr` | 超大文件 → head+tail 保留 |

## 集成

- `main.go`：初始化 retriever 后调用 `LoadProjectInstructions()`（失败只警告）
- `CLAUDE.md`：REPL 命令表加 `/reload`；BuildContext 流程说明加项目指令步骤
- 完成标准：`go build ./...` + `go test ./...` 全绿
