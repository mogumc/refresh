package process

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Execute struct {
	// Name is a stable, human-meaningful identifier for the process. Consumers
	// (e.g. a TUI) use it as the key for a per-process log pane. Optional; when
	// empty it defaults to the command string.
	Name      string            `toml:"name"       yaml:"name"`
	Cmd       string            `toml:"cmd"        yaml:"cmd"`        // Shell command retained for backwards-compatible configuration.
	Command   []string          `toml:"command"    yaml:"command"`    // Exact executable and arguments; mutually exclusive with Cmd.
	Env       map[string]string `toml:"env"        yaml:"env"`        // Environment additions and overrides.
	Readiness *Readiness        `toml:"readiness"  yaml:"readiness"`  // Optional readiness gate for an asynchronous process.
	ChangeDir string            `toml:"dir"        yaml:"dir"`        // If directory needs to be changed to call this command relative to the root path
	DelayNext int               `toml:"delay_next" yaml:"delay_next"` // Pause in ms held after this step completes, before the next process starts
	// ShutdownTimeout enables graceful process-tree termination for this duration
	// before Refresh escalates to a forced kill. Empty or zero preserves the
	// immediate-force legacy behavior.
	ShutdownTimeout string `toml:"shutdown_timeout" yaml:"shutdown_timeout"`
	// ExitPolicy controls what the engine does when an asynchronous process exits
	// on its own. Empty/ignore preserves the legacy behavior, shutdown ends the
	// session cleanly on a zero exit, and fail treats every exit as fatal.
	ExitPolicy ExitPolicy `toml:"exit_policy" yaml:"exit_policy"`
	// Type can have one of a few types to define how it reacts to a file change
	// background -- runs once at startup and is killed when refresh is canceled
	// once -- runs once at refresh startup but is blocking
	// blocking -- runs every refresh cycle as a blocking process
	// primary -- Is the primary process that kills the previous processes before running
	Type ExecuteType `toml:"type"       yaml:"type"`
}

type Readiness struct {
	TCP      string `toml:"tcp"      yaml:"tcp"`
	Timeout  string `toml:"timeout"  yaml:"timeout"`
	Interval string `toml:"interval" yaml:"interval"`
}

func (spec Execute) Validate() error {
	hasShell := strings.TrimSpace(spec.Cmd) != ""
	hasCommand := len(spec.Command) > 0
	if hasShell == hasCommand {
		return fmt.Errorf("exactly one of cmd or command must be provided")
	}
	if hasCommand && strings.TrimSpace(spec.Command[0]) == "" {
		return fmt.Errorf("command executable must not be empty")
	}
	if _, err := parseOptionalDuration(spec.ShutdownTimeout); err != nil {
		return fmt.Errorf("invalid shutdown timeout %q: %w", spec.ShutdownTimeout, err)
	}
	if !spec.ExitPolicy.valid() {
		return fmt.Errorf("invalid exit policy %q", spec.ExitPolicy)
	}
	if spec.Readiness != nil {
		if strings.TrimSpace(spec.Readiness.TCP) == "" {
			return fmt.Errorf("readiness tcp address must not be empty")
		}
		if _, err := readinessDuration(spec.Readiness.Timeout, 30*time.Second); err != nil {
			return fmt.Errorf("invalid readiness timeout %q: %w", spec.Readiness.Timeout, err)
		}
		if _, err := readinessDuration(spec.Readiness.Interval, 100*time.Millisecond); err != nil {
			return fmt.Errorf("invalid readiness interval %q: %w", spec.Readiness.Interval, err)
		}
	}
	return nil
}

func parseOptionalDuration(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration < 0 {
		return 0, fmt.Errorf("duration must not be negative")
	}
	return duration, nil
}

func readinessDuration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return duration, nil
}

type ExecuteType string

type ExitPolicy string

var (
	Background ExecuteType = "background"
	Once       ExecuteType = "once"
	Blocking   ExecuteType = "blocking"
	Primary    ExecuteType = "primary"
)

const (
	ExitPolicyIgnore   ExitPolicy = "ignore"
	ExitPolicyShutdown ExitPolicy = "shutdown"
	ExitPolicyFail     ExitPolicy = "fail"
)

func (policy ExitPolicy) valid() bool {
	switch policy {
	case "", ExitPolicyIgnore, ExitPolicyShutdown, ExitPolicyFail:
		return true
	default:
		return false
	}
}

var KILL_STALE = Execute{
	Cmd:  "KILL_STALE",
	Type: "blocking",
}

var REFRESH_EXEC = "REFRESH"
var KILL_EXEC = "KILL_STALE"

// generateExec builds a command run through the platform shell, so command
// strings may use quoting, pipes, &&, and redirection rather than being a bare
// argv split on spaces.
func generateExec(cmd string) *exec.Cmd {
	shell, args := shellInvocation(cmd)
	return exec.Command(shell, args...)
}

func stringToExecuteType(typing string) (ExecuteType, error) {
	switch typing {
	case "background":
		return Background, nil
	case "once":
		return Once, nil
	case "blocking":
		return Blocking, nil
	case "primary":
		return Primary, nil
	default:
		return "", fmt.Errorf("execute type of %q is invalid", typing)
	}
}
