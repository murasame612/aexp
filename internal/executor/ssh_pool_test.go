package executor

import (
	"os/exec"
	"testing"
)

func TestCmdConnCloseReapsProxyCommand(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	conn := &cmdConn{stdin: stdin, stdout: stdout, cmd: cmd}
	if err := conn.Close(); err != nil {
		t.Fatalf("close proxy command: %v", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("proxy command was killed but not waited for; it can remain a zombie")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second close must be idempotent: %v", err)
	}
}
