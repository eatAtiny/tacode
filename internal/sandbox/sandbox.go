// Package sandbox 提供命令沙箱抽象：网络隔离、文件系统隔离、资源限制。
//
// 设计（路径 A）：接口抽象 + 平台原生后端。
//   - macOS:  sandbox-exec（seatbelt profile，内置、非 root 可用）
//   - Linux:  Landlock（文件系统，纯 syscall）+ unshare -n（网络，空网络命名空间）
//   - 其他:   NoopSandbox（无隔离，警告降级）
//
// 沙箱是防御层（纵深防御第二层），默认关闭，用户显式 -sandbox on 启用。
package sandbox

import (
	"os/exec"
)

// Sandbox 定义命令沙箱约束（网络/文件系统/资源）。
type Sandbox interface {
	// Wrap 包装 exec.Cmd，注入沙箱约束。返回新 cmd（或原地修改后返回）。
	Wrap(cmd *exec.Cmd) *exec.Cmd
	// AllowsNetwork 报告该沙箱是否允许网络（供 shell 工具判权限）。
	AllowsNetwork() bool
	// Close 释放资源（如临时 profile 文件）。
	Close() error
}

// Config 沙箱配置。
type Config struct {
	AllowNetwork bool   // 是否允许网络（默认 false，防外发）
	WorkDir      string // 可写目录（工作目录，通常为 cwd）
}

// NewSandbox 按当前平台创建沙箱后端。
//
// 平台不支持时返回 NoopSandbox（无隔离）——不阻断启动，调用方可检查
// sandbox.IsActive() 决定是否提示用户。
func NewSandbox(cfg Config) Sandbox {
	return newPlatformSandbox(cfg)
}

// NoopSandbox 无隔离后端（平台不支持时降级）。
type NoopSandbox struct{}

func (s *NoopSandbox) Wrap(cmd *exec.Cmd) *exec.Cmd { return cmd }
func (s *NoopSandbox) AllowsNetwork() bool          { return true }
func (s *NoopSandbox) Close() error                 { return nil }

// IsActive 报告沙箱是否真正生效（非 Noop）。
func IsActive(s Sandbox) bool {
	_, ok := s.(*NoopSandbox)
	return !ok
}

// ──────────────────────────────────────────────────────────
// 工厂辅助：平台构建分派（避免 go:build 污染主文件）
// ──────────────────────────────────────────────────────────

// newPlatformSandbox 由平台文件提供实现（每平台恰好一个，见下）：
//   - darwin:  macos.go（//go:build darwin）→ newMacOSSandbox
//   - linux:   linux.go（//go:build linux）→ newLinuxSandbox
//   - 其他:    sandbox_other.go（//go:build !darwin && !linux）→ NoopSandbox
//
// 注意：不使用 runtime.GOOS 的 switch 分派——那会让每个平台文件在编译期
// 都引用另一平台的构造函数（未定义符号），导致各平台都无法编译。改为每个
// 平台文件各自实现 newPlatformSandbox，Go 构建系统按 build tag 只选一个。
// 若编译期报 "newPlatformSandbox undefined"，说明缺少对应平台文件。
