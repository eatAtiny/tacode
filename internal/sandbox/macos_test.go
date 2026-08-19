//go:build darwin

package sandbox

import (
	"os/exec"
	"strings"
	"testing"
)

func TestMacOSSandbox_ProfileGen(t *testing.T) {
	s := newMacOSSandbox(Config{AllowNetwork: false, WorkDir: "/tmp/wd"})
	defer s.Close()

	profile := s.buildProfile()
	if !strings.Contains(profile, "(deny network*)") {
		t.Error("profile should deny network when AllowNetwork=false")
	}
	if !strings.Contains(profile, "(allow file-write* (subpath \"/tmp/wd\"))") {
		t.Error("profile should allow writes only to workdir")
	}
}

func TestMacOSSandbox_ProfileGen_AllowNetwork(t *testing.T) {
	s := newMacOSSandbox(Config{AllowNetwork: true, WorkDir: "/tmp/wd"})
	defer s.Close()

	profile := s.buildProfile()
	if !strings.Contains(profile, "(allow network*)") {
		t.Error("profile should allow network when AllowNetwork=true")
	}
}

func TestMacOSSandbox_WrapArgs(t *testing.T) {
	s := newMacOSSandbox(Config{AllowNetwork: false})
	defer s.Close()

	cmd := exec.Command("bash", "-c", "echo hi")
	wrapped := s.Wrap(cmd)
	if wrapped.Path != "sandbox-exec" {
		t.Errorf("Path should be sandbox-exec, got %s", wrapped.Path)
	}
	if len(wrapped.Args) < 5 || wrapped.Args[0] != "sandbox-exec" {
		t.Errorf("args should start with sandbox-exec, got %v", wrapped.Args)
	}
}

func TestMacOSSandbox_DenyNetwork_Integration(t *testing.T) {
	// 集成测试：真实 sandbox-exec 拒绝网络。环境无 sandbox-exec 时跳过。
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	s := newMacOSSandbox(Config{AllowNetwork: false})
	defer s.Close()

	cmd := exec.Command("curl", "-s", "--max-time", "3", "https://example.com")
	wrapped := s.Wrap(cmd)
	out, err := wrapped.CombinedOutput()
	// 网络被拒 → curl 应失败（非零退出码或输出含拒绝信息）。
	if err == nil && strings.Contains(string(out), "<html") {
		t.Error("network should be denied in sandbox, curl succeeded")
	}
}
