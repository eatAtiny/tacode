package sandbox

import (
	"os/exec"
	"testing"
)

func TestNoopSandbox_Wrap(t *testing.T) {
	s := &NoopSandbox{}
	cmd := exec.Command("echo", "hi")
	wrapped := s.Wrap(cmd)
	if wrapped != cmd {
		t.Error("NoopSandbox should return the same cmd")
	}
	if len(wrapped.Args) != 2 || wrapped.Args[0] != "echo" {
		t.Errorf("NoopSandbox should not modify args, got %v", wrapped.Args)
	}
}

func TestNoopSandbox_AllowsNetwork(t *testing.T) {
	s := &NoopSandbox{}
	if !s.AllowsNetwork() {
		t.Error("NoopSandbox should allow network (no restriction)")
	}
}

func TestNoopSandbox_Close(t *testing.T) {
	s := &NoopSandbox{}
	if err := s.Close(); err != nil {
		t.Errorf("NoopSandbox.Close should not error, got %v", err)
	}
}

func TestIsActive(t *testing.T) {
	if IsActive(&NoopSandbox{}) {
		t.Error("NoopSandbox should be inactive")
	}
}

func TestNewSandbox_Platform(t *testing.T) {
	s := NewSandbox(Config{})
	if s == nil {
		t.Fatal("NewSandbox should return a Sandbox")
	}
	_ = s.Close()
}
