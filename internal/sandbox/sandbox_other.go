//go:build !darwin && !linux

package sandbox

import "os/exec"

// newPlatformSandbox 在非 darwin/linux 平台返回 NoopSandbox（无隔离）。
func newPlatformSandbox(cfg Config) Sandbox {
	return &NoopSandbox{}
}

var _ = exec.Command // 保留 os/exec 引用（接口一致性）
