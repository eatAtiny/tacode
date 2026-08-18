//go:build linux || solaris || aix

package bubble

import "golang.org/x/sys/unix"

// enableOPOST 在生模式下重新启用 OPOST（输出后处理）标志。
//
// term.MakeRaw 清除了 OPOST，导致 \n 不再自动转换为 \r\n，
// 造成输出换行后光标不回行首的格式化错位。这里仅恢复输出端行为。
//
// Linux/Solaris/AIX 的 termios ioctl 请求码为 TCGETS/TCSETS，
// IoctlGetTermios 的 req 参数为 int 类型（darwin/BSD 为 uint，
// 封装在 termios_tioc.go）。
func enableOPOST(fd int) {
	if tios, err := unix.IoctlGetTermios(fd, unix.TCGETS); err == nil {
		tios.Oflag |= unix.OPOST
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, tios)
	}
}
