//go:build !darwin && !linux

package sandbox

// newPlatformSandbox 在非 darwin/linux 平台返回 NoopSandbox（无隔离）。
func newPlatformSandbox(cfg Config) Sandbox {
	return &NoopSandbox{}
}
