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
//   - 网络隔离：CLONE_NEWNET + CLONE_NEWUSER（全新网络命名空间）——通过
//     cmd.SysProcAttr 注入，子进程启动即处于空网络命名空间（无外部接口），
//     从根上杜绝外发网络。这是 exec.Cmd 可落地、可验证的部分。
//   - 文件系统隔离：Landlock（go-landlock 库）——v1 跳过，见下方局限性说明。
//
// ── 非 root 如何创建网络命名空间（CLONE_NEWNET + CLONE_NEWUSER） ──
//
// CLONE_NEWNET 单独使用要求调用者在所在 user namespace 持有 CAP_SYS_ADMIN，
// 绝大多数发行版上非 root 直接 clone(2) 会返回 EPERM。因此必须同时加上
// CLONE_NEWUSER：创建一个新的 user namespace，进程在其中拥有完整能力
// （包括 CAP_SYS_ADMIN），从而能创建新的网络命名空间。两步合在一起即可让
// 非 root 用户在开启 user namespaces 的发行版上获得网络隔离。
//
// 但 CLONE_NEWUSER 有一个关键陷阱：新 user namespace 的 uid_map/gid_map 初始
// 为【空】，进程继承宿主 uid，但该 uid 在命名空间内未被映射——getuid()/
// getgid() 返回 overflow id 65534（nobody），文件访问检查也按未映射 uid
// 处理，沙箱命令将无法读写用户目录下的任何文件。因此必须【显式写入映射】：
// 把容器内 uid 0 映射到宿主真实 uid/gid（"0 → 当前用户"），使子进程成为
// 功能完整的 namespace root——既能创建网络命名空间，又保有对用户自身文件的
// 读写能力。这与 Docker/userns 沙箱的做法一致。
//
// 写入映射的机制（Go 运行时）：syscall/exec_linux.go 只有在
// SysProcAttr.UidMappings / GidMappings 非 nil 时才向 /proc/PID/uid_map、
// gid_map 写映射（forkAndExecInChild 的父/子两侧协调完成）。注意
// SysProcAttr.Credential 不触发映射写入——它只做 setgroups/setgid/setuid，
// 而空映射下 setuid 会 EPERM。另外非特权用户写 gid_map 前必须先把
// setgroups 写为 deny（GidMappingsEnableSetgroups=false 即满足）。
//
// 边界情况：
//   - 以 root 运行时不需要 user namespace，但加上也无害（root 可直接 CLONE_NEWNET）。
//   - 内核/发行版禁用 unprivileged user namespaces 时（如 Ubuntu 24.04 的
//     AppArmor 限制、kernel.unprivileged_userns_clone=0），clone(2) 返回 EPERM，
//     命令启动失败——错误对调用方可见，不会静默放行（fail-closed）。
//   - 若调用方已设置 SysProcAttr（如已有 Cloneflags 或 Credential），保留其
//     字段，只合并 Cloneflags，避免覆盖其他配置。
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
		// 网络隔离：CLONE_NEWNET + CLONE_NEWUSER 让子进程跑在全新的
		// 网络命名空间里（只有 loopback，无外部接口），任何外发网络都
		// 会被内核拒绝。见类型注释中"非 root 如何创建网络命名空间"一节。
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNET | syscall.CLONE_NEWUSER

		// 为 CLONE_NEWUSER 显式写 uid/gid 映射：容器内 uid 0 映射到宿主
		// 真实 uid/gid，使子进程成为功能完整的 namespace root（否则空映射
		// 下 getuid()=65534，一切文件访问失败）。取当前进程真实 uid/gid，
		// 因为父进程就是执行 shell 命令的用户。仅当调用方未自定义映射时
		// 才写入，避免覆盖已有配置。
		if cmd.SysProcAttr.UidMappings == nil {
			cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getuid(), Size: 1},
			}
		}
		if cmd.SysProcAttr.GidMappings == nil {
			cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getgid(), Size: 1},
			}
			// 非特权用户写 gid_map 前必须先把 setgroups 置为 deny
			// （写 gid_map 后该命名空间内 setgroups(2) 永久禁用）。
			cmd.SysProcAttr.GidMappingsEnableSetgroups = false
		}
	}
	return cmd
}

// AllowsNetwork 报告是否允许网络。
func (s *LinuxSandbox) AllowsNetwork() bool { return s.allowNetwork }

// Close 无资源需释放（网络命名空间随子进程退出由内核清理）。
func (s *LinuxSandbox) Close() error { return nil }

// 确保沙箱在 linux 下可用（编译期断言）。
var _ Sandbox = (*LinuxSandbox)(nil)
