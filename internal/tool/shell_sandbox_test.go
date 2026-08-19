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

func TestShellTool_NetworkApproval(t *testing.T) {
	args := `{"command": "curl https://x.com", "network": true}`
	st := NewShellToolWithSandbox(&mockSandbox{})

	// 未确认 → 仍需确认。
	if perm := st.CheckPermission(args); perm.Allow {
		t.Error("network:true should require confirmation before approval")
	}

	// 确认放行（AllowNetworkFor 按原始 args JSON 记录）。
	st.AllowNetworkFor(args)
	if perm := st.CheckPermission(args); !perm.Allow {
		t.Error("approved args should be allowed")
	}

	// 精确匹配：同命令不同 args JSON 不共享放行。
	if perm := st.CheckPermission(`{"command":"curl https://x.com","network":true}`); perm.Allow {
		t.Error("approval should be keyed by exact args string")
	}

	// 未请求 network 的命令不触发放行分支，也不应被放行状态影响。
	st2 := NewShellToolWithSandbox(&mockSandbox{})
	st2.AllowNetworkFor(args)
	if perm := st2.CheckPermission(`{"command": "ls -la"}`); !perm.Allow {
		t.Error("non-network command should still be allowed")
	}

	// 无沙箱工具调用 AllowNetworkFor 也应安全（map 惰性初始化）。
	stPlain := NewShellTool()
	stPlain.AllowNetworkFor(args)
	if perm := stPlain.CheckPermission(`{"command": "curl https://x.com", "network": true}`); !perm.Allow {
		t.Error("without sandbox, network param should be ignored even after AllowNetworkFor")
	}
}

func TestShellTool_NetworkApproval_DoesNotBypassDangerous(t *testing.T) {
	st := NewShellToolWithSandbox(&mockSandbox{})
	args := `{"command": "rm -rf /etc/passwd", "network": true}`

	// 放行前：需确认。
	perm := st.CheckPermission(args)
	if perm.Allow {
		t.Error("dangerous network command should require confirmation before approval")
	}

	// 放行后：危险命令仍需确认（approval 不能掩盖危险）。
	st.AllowNetworkFor(args)
	perm = st.CheckPermission(args)
	if perm.Allow {
		t.Error("dangerous command should still require confirmation after network approval")
	}
}
