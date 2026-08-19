# Shell 沙箱设计文档

- 日期：2026-08-18
- 状态：已批准
- 范围：`internal/sandbox/`（新增包：Sandbox 接口 + macOS/Linux 后端 + Noop）+ `internal/tool/shell.go`（改造）+ `main.go`（`-sandbox` flag）+ 测试

## 背景

Agent 的 `shell` 工具（`internal/tool/shell.go:58`）裸跑 `bash -c`，无任何隔离。用户四项需求：网络隔离（防外发）、文件系统隔离、资源限制、工作目录隔离。经确认走**路径 A：接口抽象 + 平台原生后端**。

## 决策

| 决策点 | 结论 |
|--------|------|
| 路径 | `internal/sandbox` 接口抽象 + 平台原生后端（macOS sandbox-exec / Linux Landlock+unshare / Windows Noop） |
| 范围 | 只沙箱 shell 工具；git 保持原生（只读命令需读仓库，写操作已有确认） |
| 默认策略 | 网络拒绝 + 文件系统限制（cwd 可读写、系统只读、其余拒绝） |
| 放行机制 | shell 工具加 `network: true` 参数 → 触发既有权限确认流程 |
| 工作目录 | cwd 即工作目录（不建 worktree） |
| 启动配置 | `-sandbox on/off` flag，**默认 off**（用户显式启用才生效，保持向后兼容） |

## 架构

```
internal/sandbox/（新增包）
  ├─ sandbox.go    — Sandbox 接口 + NewSandbox(platform, cfg) 工厂 + NoopSandbox
  ├─ macos.go      — sandbox-exec 后端（seatbelt profile）
  ├─ linux.go      — Landlock（文件系统）+ unshare -n（网络）
  └─ sandbox_test.go

internal/tool/shell.go（改造）
  ├─ NewShellTool() → 无沙箱（默认，向后兼容）
  ├─ NewShellToolWithSandbox(sb) → 带沙箱
  ├─ Execute(): cmd = sb.Wrap(cmd)（sandbox 非 nil 时）
  └─ Parameters() 加 network 参数 → CheckPermission: network=true → 需确认

main.go
  └─ -sandbox flag → sandbox.NewSandbox(...) → NewShellToolWithSandbox(sb)
```

## Sandbox 接口

```go
// Sandbox 定义命令沙箱约束（网络/文件系统/资源）。
type Sandbox interface {
	// Wrap 包装 exec.Cmd，注入沙箱约束。返回新 cmd（或原地修改后返回）。
	Wrap(cmd *exec.Cmd) *exec.Cmd
	// AllowsNetwork 报告该沙箱是否允许网络（供 shell 工具判权限）。
	AllowsNetwork() bool
	// Close 释放资源（如临时 profile 文件）。
	Close() error
}
```

## macOS 后端（sandbox-exec）

`sandbox-exec` 虽被 Apple 标记 deprecated，但仍是内置、非 root 可用的最实用选项。

```go
// MacOSSandbox 用 sandbox-exec 包装命令。
type MacOSSandbox struct {
	profilePath  string // 生成的 seatbelt profile 文件路径
	allowNetwork bool
	workDir      string // 可写目录（cwd）
}
```

**seatbelt profile 规则**（Lisp 风格，优先级语义）：

```
(version 1)
(deny default)
(import "system.sb")            ; 基础系统放行
(allow network-outbound)        ; 默认放行网络（allowNetwork 控制）
(allow file-read*)              ; 读全放行（读安全）
(allow file-write* (subpath "<cwd>"))  ; 写仅限 cwd
(allow process*)
(deny network*)                 ; allowNetwork=false 时覆盖（防外发）
```

Wrap 实现：

```go
func (s *MacOSSandbox) Wrap(cmd *exec.Cmd) *exec.Cmd {
	// sandbox-exec -f <profile> -- <cmd.Args...>
	inner := append([]string{"-f", s.profilePath, "--"}, cmd.Args...)
	cmd.Args = append([]string{"sandbox-exec"}, inner...)
	return cmd
}
```

profile 文件用 `os.CreateTemp` 生成，`Close()` 时删除。

## Linux 后端（Landlock + unshare）

**Landlock**（内核 5.13+）做文件系统隔离——纯 syscall，`github.com/landlock-lsm/go-landlock` 库无外部依赖：

```go
// LinuxSandbox 用 Landlock（文件系统）+ unshare -n（网络）限制命令。
type LinuxSandbox struct {
	allowNetwork bool
	workDir      string
}

func (s *LinuxSandbox) Wrap(cmd *exec.Cmd) *exec.Cmd {
	// 文件系统：cwd 可读写，系统目录（/usr、/etc、/bin 等）只读。
	// 通过 go-landlock 的 RestrictPaths 在命令启动前应用规则。
	// 网络：内核 6.7+ 有 Landlock 网络规则；低版本用 unshare -n
	//       （空网络命名空间，非 root 可用依赖 user namespaces）。
	return cmd
}
```

**关键限制**：Landlock 文件系统隔离成熟；网络隔离内核 6.7+ 才原生支持。低版本 Linux 用 `unshare -n`（切到空网络命名空间）实现，user namespaces 可用时非 root 可执行。若均不可用 → 降级为文件系统隔离 + 网络警告。

## shell 工具改造

```go
type ShellTool struct {
	timeout time.Duration
	sandbox Sandbox // nil = 无沙箱（默认，向后兼容）
}

// NewShellTool 创建无沙箱 shell 工具（默认，向后兼容）。
func NewShellTool() *ShellTool { return &ShellTool{timeout: 30 * time.Second} }

// NewShellToolWithSandbox 创建带沙箱的 shell 工具。
func NewShellToolWithSandbox(sb Sandbox) *ShellTool {
	return &ShellTool{timeout: 30 * time.Second, sandbox: sb}
}
```

**Execute 改造**（`shell.go:58`）：

```go
	cmd := exec.CommandContext(ctx, "bash", "-c", params.Command)
	if t.sandbox != nil {
		cmd = t.sandbox.Wrap(cmd)
	}
	output, err := cmd.CombinedOutput()
```

**Parameters() 加 network 参数**：

```json
"network": {"type": "boolean", "description": "是否需要网络访问（curl 等）。默认 false（沙箱禁止网络）"}
```

**CheckPermission**：`network: true` → `PermissionResult{Allow: false, Reason: "需要网络访问，请确认"}`（走既有权限确认流程）；默认 → `Allow: true`。无沙箱时 `network` 参数忽略（直接允许）。

## main.go 集成

```go
sandboxMode := flag.String("sandbox", "off", "sandbox mode: on/off")
```

- `off`（默认）→ 无沙箱（行为与现状完全一致，向后兼容）
- `on` → 强制启用（平台不支持时降级：macOS 无 sandbox-exec / Linux 内核 < 5.13 或无 user namespaces → 警告并继续无沙箱，不阻断启动）

工厂：`sandbox.NewSandbox(platform, cfg)` 按 `runtime.GOOS` 分派。沙箱是防御层，用户显式开启才生效。

## 测试计划

| 测试 | 覆盖点 |
|------|--------|
| `TestNoopSandbox_Wrap` | Noop 不改变 cmd |
| `TestMacOSSandbox_ProfileGen` | 生成 profile 含网络/文件规则（golden 匹配） |
| `TestMacOSSandbox_WrapArgs` | cmd.Args 前置 sandbox-exec |
| `TestLinuxSandbox_Wrap` | Landlock 规则应用（文件系统） |
| `TestShellTool_NetworkPermission` | network:true → 需确认；默认 → 允许 |
| `TestShellTool_WithSandbox_Wrap` | 带沙箱时 cmd 被包装（mock Sandbox） |

集成测试（真实沙箱，macOS 才跑）：

| 测试 | 覆盖点 |
|------|--------|
| `TestShellTool_SandboxDenyNetwork` | curl 外部地址失败（证明网络被拒） |
| `TestShellTool_SandboxAllowNetwork` | network:true + 确认后 curl 成功 |

跳过机制：非 macOS 或 sandbox-exec 不可用时 `t.Skip`。

## 涉及文件

| 文件 | 改动 |
|------|------|
| `internal/sandbox/sandbox.go` | 新增：接口 + 工厂 + Noop |
| `internal/sandbox/macos.go` | 新增：sandbox-exec 后端 |
| `internal/sandbox/linux.go` | 新增：Landlock + unshare 后端 |
| `internal/sandbox/sandbox_test.go` | 新增：单元测试 |
| `internal/tool/shell.go` | 改造：sandbox 字段 + 构造函数 + Wrap + network 参数 |
| `main.go` | 新增：`-sandbox` flag + 工厂调用 |
| `CLAUDE.md` | 文档：sandbox 说明 |

## 依赖

- `github.com/landlock-lsm/go-landlock`（Linux 后端，Go 库无外部依赖）——**仅 Linux 构建时拉取**（build tag）
- macOS 用系统内置 `sandbox-exec`（无新依赖）

## 集成

- 完成标准：`go build ./...` + `go test ./...` 全绿
- 默认 `-sandbox off`，行为与现状完全一致（向后兼容）
