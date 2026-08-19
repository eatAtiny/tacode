package tool

import (
	"os/exec"
	"strings"
	"testing"
)

// mockSandbox 用于测试 ShellTool 的 Wrap 调用。
type mockSandbox struct{ wrapped bool }

func (m *mockSandbox) Wrap(cmd *exec.Cmd) *exec.Cmd { m.wrapped = true; return cmd }
func (m *mockSandbox) AllowsNetwork() bool          { return false }
func (m *mockSandbox) Close() error                 { return nil }

func TestShellTool_WithSandbox_Wraps(t *testing.T) {
	sb := &mockSandbox{}
	st := NewShellToolWithSandbox(sb)

	_, err := st.Execute(`{"command": "echo hi"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sb.wrapped {
		t.Error("ShellTool should wrap cmd when sandbox is set")
	}
}

func TestShellTool_NetworkPermission(t *testing.T) {
	st := NewShellToolWithSandbox(&mockSandbox{})

	// network=true → 需确认。
	perm := st.CheckPermission(`{"command": "curl https://x.com", "network": true}`)
	if perm.Allow {
		t.Error("network:true should require confirmation")
	}
	if !strings.Contains(perm.Reason, "网络") {
		t.Errorf("reason should mention network, got %q", perm.Reason)
	}

	// 默认（无 network）→ 允许（非危险命令）。
	perm = st.CheckPermission(`{"command": "ls -la"}`)
	if !perm.Allow {
		t.Error("ls should be allowed by default")
	}

	// 无沙箱时 network 参数忽略 → 允许。
	stPlain := NewShellTool()
	perm = stPlain.CheckPermission(`{"command": "curl https://x.com", "network": true}`)
	if !perm.Allow {
		t.Error("network param should be ignored without sandbox")
	}
}
