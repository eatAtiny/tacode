//go:build windows

package bubble

// enableOPOST 在生模式下重新启用 OPOST（输出后处理）标志。
//
// Windows 无类 unix 的 termios 机制，golang.org/x/term.MakeRaw 走 Windows
// 控制台 API 实现，不存在 OPOST 被清除的问题，这里为空实现。
func enableOPOST(fd int) {}
