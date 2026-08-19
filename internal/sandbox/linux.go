//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"syscall"
)

// LinuxSandbox 用 Linux 原生机制隔离命令。
//
// v1 实现（务实路径，仅做可落地部分）：
//   - 网络隔离：CLONE_NEWNET（全新网络命名空间）——通过 cmd.SysProcAttr 注入，
//     子进程启动即处于空网络命名空间（无外部接口），从根上杜绝外发网络。
//     这是 exec.Cmd 可落地、可验证的部分。
//   - 文件系统隔离：Landlock（go-landlock 库）——v1 跳过，见下方局限性说明。
//
// ── 文件系统隔离的局限性说明（为什么 v1 不实现 Landlock） ──
//
// go-landlock 的 Config.RestrictPaths() 通过 landlock_restrict_self(2)
// 只限制【调用它的当前进程】（内核语义即 "restrict self"，同时设置
// PR_SET_NO_NEW_PRIVS）。子进程仅在【继承父进程 Landlock 域】时才受限
// （fork/exec 的继承语义），因此对 exec.Cmd 而言：
//
//   - 在父进程（agent 自身）调用 RestrictPaths() → 会把 agent 自己也锁死，
//     副作用过大，不可接受；
//   - os/exec 没有 fork 与 exec 之间的 pre-exec 钩子：SysProcAttr 只支持
//     Cloneflags / Chroot / Setsid 等预定义字段，无法在子进程 exec 前执行
//     任意代码，因此无法只对单个子进程施加 Landlock 规则。
//
// 可行的未来方案：内置一个小 wrapper（类似 go-landlock 自带的
// cmd/landlock-restrict：先 RestrictPaths() 再 syscall.Exec()），Wrap 时
// 把命令改为经由 wrapper 启动。这需要随二进制编译/安装 helper，v1 暂不
// 实现，先以网络隔离为主，文件系统隔离留作后续任务。
//
// 另注：go-landlock 也提供 RestrictNet（Landlock ABI v4+，内核 6.7+），但
// 同样只作用于当前进程，且是"端口白名单"而非"全拒"，语义上不如 CLONE_NEWNET
// 干净，故网络隔离也选择 unshare 方案。
type LinuxSandbox struct {
	allowNetwork bool
	workDir      string // 可写目录（预留给未来的 Landlock 文件系统隔离）
}

// newPlatformSandbox 创建 Linux 沙箱（构建分派入口，linux 下实现）。
func newPlatformSandbox(cfg Config) Sandbox {
	return newLinuxSandbox(cfg)
}

// newLinuxSandbox 创建 Linux 沙箱。
func newLinuxSandbox(cfg Config) *LinuxSandbox {
	s := &LinuxSandbox{
		allowNetwork: cfg.AllowNetwork,
		workDir:      cfg.WorkDir,
	}
	if s.workDir == "" {
		s.workDir, _ = os.Getwd()
	}
	return s
}

// Wrap 给 cmd 注入沙箱约束（原地修改并返回）。
func (s *LinuxSandbox) Wrap(cmd *exec.Cmd) *exec.Cmd {
	if !s.allowNetwork {
		// 网络隔离：CLONE_NEWNET 让子进程跑在全新的网络命名空间里
		// （只有 loopback，无外部接口），任何外发网络都会被内核拒绝。
		//
		// 注意：
		//   - 非 root 可用性依赖 user namespaces（多数发行版默认开启
		//     kernel.unprivileged_userns_clone）；否则 clone(2) 返回
		//     EPERM，命令启动失败（调用方可见错误，不会静默放行）。
		//   - 若调用方已设置 SysProcAttr（如其他命名空间），保留其
		//     Cloneflags 并合并，避免覆盖。
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNET
	}
	return cmd
}

// AllowsNetwork 报告是否允许网络。
func (s *LinuxSandbox) AllowsNetwork() bool { return s.allowNetwork }

// Close 无资源需释放（网络命名空间随子进程退出由内核清理）。
func (s *LinuxSandbox) Close() error { return nil }

// 确保沙箱在 linux 下可用（编译期断言）。
var _ Sandbox = (*LinuxSandbox)(nil)
