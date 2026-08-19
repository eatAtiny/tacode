//go:build darwin

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MacOSSandbox 用 sandbox-exec（seatbelt profile）包装命令。
//
// sandbox-exec 虽被 Apple 标记 deprecated，但仍是内置、非 root 可用的
// 最实用选项：一条 profile 同时做到拒绝网络 + 限制文件系统写路径。
type MacOSSandbox struct {
	profilePath  string // 临时 seatbelt profile 文件路径
	allowNetwork bool
	workDir      string // 可写目录
}

// newPlatformSandbox 创建 macOS 沙箱（构建分派入口，darwin 下实现）。
func newPlatformSandbox(cfg Config) Sandbox {
	return newMacOSSandbox(cfg)
}

// newMacOSSandbox 创建 macOS 沙箱，生成临时 profile 文件。
func newMacOSSandbox(cfg Config) *MacOSSandbox {
	s := &MacOSSandbox{
		allowNetwork: cfg.AllowNetwork,
		workDir:      cfg.WorkDir,
	}
	if s.workDir == "" {
		s.workDir, _ = os.Getwd()
	}
	// 生成临时 profile（Close 时删除）。
	if f, err := os.CreateTemp("", "agentic-sandbox-*.sb"); err == nil {
		fmt.Fprint(f, s.buildProfile())
		f.Close()
		s.profilePath = f.Name()
	}
	return s
}

// buildProfile 生成 seatbelt profile（Lisp 风格，规则有优先级语义）。
func (s *MacOSSandbox) buildProfile() string {
	var sb strings.Builder
	sb.WriteString("(version 1)\n")
	sb.WriteString("(deny default)\n")
	sb.WriteString("(import \"system.sb\")\n")
	// 文件系统：读全放行（读安全），写仅限工作目录。
	sb.WriteString("(allow file-read*)\n")
	sb.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", s.workDir))
	// 进程：放行进程创建（shell 需要 fork 子进程）。
	sb.WriteString("(allow process*)\n")
	if s.allowNetwork {
		// 网络放行（默认 deny default 已拦，这里显式允许）。
		sb.WriteString("(allow network*)\n")
	} else {
		// 网络全拒（防外发）。
		sb.WriteString("(deny network*)\n")
	}
	return sb.String()
}

// Wrap 用 sandbox-exec 包装命令。
// 最终命令: sandbox-exec -f <profile> -- <cmd.Args...>
func (s *MacOSSandbox) Wrap(cmd *exec.Cmd) *exec.Cmd {
	if s.profilePath == "" {
		return cmd // profile 生成失败 → 降级无沙箱
	}
	inner := append([]string{"-f", s.profilePath, "--"}, cmd.Args...)
	cmd.Args = append([]string{"sandbox-exec"}, inner...)
	cmd.Path = "sandbox-exec"
	return cmd
}

// AllowsNetwork 报告是否允许网络。
func (s *MacOSSandbox) AllowsNetwork() bool { return s.allowNetwork }

// Close 删除临时 profile 文件。
func (s *MacOSSandbox) Close() error {
	if s.profilePath != "" {
		return os.Remove(s.profilePath)
	}
	return nil
}

// 确保沙箱在 darwin 下可用（编译期断言）。
var _ Sandbox = (*MacOSSandbox)(nil)

// filepath 引用（避免误删 import）。
var _ = filepath.Join
