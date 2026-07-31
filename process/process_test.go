//go:build linux || darwin

package process

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether a pid refers to a live process (signal 0 probes without
// actually sending anything).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// waitFor polls until cond is true or the deadline elapses.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestStartReloadShutdownLifecycle(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(root); err != nil {
		t.Fatalf("SetRootDirectory: %v", err)
	}

	// A blocking step that must re-run on every cycle, and a long-lived primary
	// that must be killed and restarted on reload.
	if err := pm.AddProcess("touch marker", "blocking", ""); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcess("sleep 30", "primary", ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := pm.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	marker := filepath.Join(root, "marker")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("blocking step did not run on start: %v", err)
	}

	primary := pm.Processes[1]
	pid1 := primary.cmd.Process.Pid
	if !alive(pid1) {
		t.Fatalf("primary not running after start (pid %d)", pid1)
	}

	// Reload should re-run the blocking step and restart the primary with a new pid.
	os.Remove(marker)
	if err := pm.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("blocking step did not re-run on reload: %v", err)
	}

	pid2 := primary.cmd.Process.Pid
	if pid2 == pid1 {
		t.Fatalf("primary was not restarted (pid unchanged: %d)", pid1)
	}
	if !waitFor(func() bool { return !alive(pid1) }) {
		t.Errorf("old primary (pid %d) still alive after reload", pid1)
	}
	if !alive(pid2) {
		t.Fatalf("new primary (pid %d) not running after reload", pid2)
	}

	// Shutdown must terminate the running primary.
	pm.Shutdown()
	if !waitFor(func() bool { return !alive(pid2) }) {
		t.Errorf("primary (pid %d) still alive after shutdown", pid2)
	}
}

func TestShellFeaturesAreSupported(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}
	// Redirection and && only work when the command runs through a shell rather
	// than a bare argv split.
	if err := pm.AddProcess("echo one > out.txt && echo two >> out.txt", "blocking", ""); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcess("sleep 30", "primary", ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pm.Shutdown()

	data, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil {
		t.Fatalf("shell command did not produce output file: %v", err)
	}
	if got := string(data); got != "one\ntwo\n" {
		t.Errorf("shell features not honored, out.txt = %q", got)
	}
}

func TestStructuredCommandPreservesArgumentsAndEnvironment(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{
		Name:    "structured",
		Command: []string{"sh", "-c", `printf '%s|%s' "$1" "$WAILS_VALUE" > result`, "sh", "an argument"},
		Env:     map[string]string{"WAILS_VALUE": "environment value"},
		Type:    Blocking,
	}); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{Command: []string{"sleep", "30"}, Type: Primary}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pm.Shutdown()

	data, err := os.ReadFile(filepath.Join(root, "result"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "an argument|environment value" {
		t.Fatalf("result = %q", got)
	}
	info := pm.Snapshot()[0]
	if info.Name != "structured" || len(info.Command) != 5 {
		t.Fatalf("snapshot = %+v", info)
	}
}

func TestKillProcessTreeNilSafe(t *testing.T) {
	if err := killProcessTree(nil); err != nil {
		t.Errorf("killProcessTree(nil) = %v, want nil", err)
	}
	if err := killProcessTree(&exec.Cmd{}); err != nil {
		t.Errorf("killProcessTree on unstarted cmd = %v, want nil", err)
	}
}

func TestSetRootDirectoryDefaultsToCwd(t *testing.T) {
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(""); err != nil {
		t.Fatalf("SetRootDirectory(\"\"): %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if pm.RootDir != wd {
		t.Errorf("RootDir = %q, want cwd %q", pm.RootDir, wd)
	}
}

func TestStartWithNoProcessesErrors(t *testing.T) {
	pm := NewProcessManager()
	if err := pm.Start(context.Background()); err == nil {
		t.Fatal("expected error when starting with no processes")
	}
}

func TestOnceFailureAbortsStartup(t *testing.T) {
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcess("false", "once", ""); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcess("sleep 30", "primary", ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err == nil {
		t.Fatal("expected Start to fail when a once step exits non-zero")
	}
	if pm.Processes[1].cmd != nil {
		t.Error("primary should not have started after a once failure")
	}
	pm.Shutdown()
}

func TestStartAsyncFailsOnMissingDirectory(t *testing.T) {
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	// A ChangeDir that does not exist makes exec.Cmd.Start fail, exercising the
	// startAsync error path.
	if err := pm.AddProcess("sleep 30", "primary", "does-not-exist"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err == nil {
		t.Fatal("expected Start to fail when the working directory is missing")
	}
	pm.Shutdown()
}

func TestBlockingFailureAbortsCycle(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}
	// A blocking step that exits non-zero must abort the cycle before the primary.
	if err := pm.AddProcess("false", "blocking", ""); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcess("sleep 30", "primary", ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := pm.Start(ctx); err == nil {
		t.Fatal("expected Start to fail when a blocking step fails")
	}
	if primary := pm.Processes[1]; primary.cmd != nil {
		t.Error("primary should not have started after blocking failure")
	}
	pm.Shutdown()
}

func TestBackgroundFailureCancelsBlockingStartupStep(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}
	backgroundCmd := "while [ ! -f readiness-started ]; do sleep 0.01; done; exit 23"
	if err := pm.AddProcessSpec(Execute{Name: "frontend", Cmd: backgroundCmd, Type: Background}); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{Name: "readiness", Cmd: "touch readiness-started; sleep 30", Type: Once}); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{Name: "application", Cmd: "sleep 30", Type: Primary}); err != nil {
		t.Fatal(err)
	}
	defer pm.Shutdown()

	started := time.Now()
	err := pm.Start(context.Background())
	if err == nil {
		t.Fatal("expected Start to fail when the background process exits")
	}
	want := `background process "` + backgroundCmd + `" exited during startup`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Start error = %q, want background startup failure", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Start took %s; readiness step was not cancelled promptly", elapsed)
	}
	if primary := pm.Processes[2]; primary.cmd != nil {
		t.Error("primary should not have started after the background failure")
	}
}

func TestBackgroundFailureAfterStartupDoesNotStopPrimary(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{Name: "frontend", Cmd: "while [ ! -f stop-background ]; do sleep 0.01; done; exit 23", Type: Background}); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{Name: "application", Cmd: "sleep 30", Type: Primary}); err != nil {
		t.Fatal(err)
	}

	defer pm.Shutdown()
	if err := pm.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "stop-background"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if !waitFor(func() bool {
		return pm.Snapshot()[0].State == StateFailed
	}) {
		t.Fatal("background process did not exit")
	}

	primary := pm.Processes[1]
	if primary.cmd == nil || !alive(primary.cmd.Process.Pid) {
		t.Error("primary stopped after a post-startup background failure")
	}
}

func TestReloadFailurePreservesLastGoodPrimary(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcess("test ! -f fail-build", "blocking", ""); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcess("sleep 30", "primary", ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pm.Shutdown()
	primary := pm.Processes[1]
	pid := primary.cmd.Process.Pid

	if err := os.WriteFile(filepath.Join(root, "fail-build"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pm.Reload(ctx); err == nil {
		t.Fatal("expected reload build to fail")
	}
	if primary.cmd == nil || primary.cmd.Process.Pid != pid {
		t.Fatalf("primary changed after failed reload: got %+v, want pid %d", primary.cmd, pid)
	}
	if !alive(pid) {
		t.Fatalf("last good primary pid %d was killed after failed reload", pid)
	}
}

func TestReadinessWaitsForTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	pm := NewProcessManager()
	if err := pm.SetRootDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{
		Command: []string{"sleep", "30"}, Type: Background,
		Readiness: &Readiness{TCP: listener.Addr().String(), Timeout: "1s", Interval: "10ms"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pm.Shutdown()
}

func TestReadinessTimeoutStopsStartup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()

	pm := NewProcessManager()
	if err := pm.SetRootDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{
		Command: []string{"sleep", "30"}, Type: Background,
		Readiness: &Readiness{TCP: address, Timeout: "50ms", Interval: "10ms"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err == nil {
		t.Fatal("expected readiness timeout")
	}
	pm.Shutdown()
}

func TestGracefulShutdownSignalsProcessTreeBeforeForce(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{
		Cmd:  `trap 'echo stopped > graceful; exit 0' TERM; touch ready; while :; do sleep 1; done`,
		Type: Primary, ShutdownTimeout: "1s",
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		_, err := os.Stat(filepath.Join(root, "ready"))
		return err == nil
	}) {
		t.Fatal("process did not install its signal handler")
	}
	pm.Shutdown()
	if _, err := os.Stat(filepath.Join(root, "graceful")); err != nil {
		t.Fatalf("process did not handle graceful shutdown: %v", err)
	}
}

func TestGracefulShutdownEscalatesAfterTimeout(t *testing.T) {
	pm := NewProcessManager()
	if err := pm.SetRootDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddProcessSpec(Execute{
		Cmd: `trap '' TERM; while :; do sleep 1; done`, Type: Primary, ShutdownTimeout: "50ms",
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	pid := pm.Processes[0].cmd.Process.Pid
	pm.Shutdown()
	if !waitFor(func() bool { return !alive(pid) }) {
		t.Fatalf("process %d survived graceful shutdown escalation", pid)
	}
}
