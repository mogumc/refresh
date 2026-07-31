package engine

import (
	"reflect"
	"testing"

	"github.com/atterpac/refresh/process"
)

func TestExecListToSpecs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []process.Execute
	}{
		{
			name: "refresh marker promotes following command to primary",
			in:   []string{"go mod tidy", "go build -o ./app", "KILL_STALE", "REFRESH", "./app"},
			want: []process.Execute{
				{Cmd: "go mod tidy", Type: process.Blocking},
				{Cmd: "go build -o ./app", Type: process.Blocking},
				{Cmd: "./app", Type: process.Primary},
			},
		},
		{
			name: "no refresh marker makes the last command primary",
			in:   []string{"go build -o ./app", "./app"},
			want: []process.Execute{
				{Cmd: "go build -o ./app", Type: process.Blocking},
				{Cmd: "./app", Type: process.Primary},
			},
		},
		{
			name: "whitespace and empties are trimmed and dropped",
			in:   []string{" go build ", "", "REFRESH", " ./app "},
			want: []process.Execute{
				{Cmd: "go build", Type: process.Blocking},
				{Cmd: "./app", Type: process.Primary},
			},
		},
		{
			name: "empty list yields no specs",
			in:   []string{""},
			want: []process.Execute{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := execListToSpecs(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d specs, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if !reflect.DeepEqual(got[i], tt.want[i]) {
					t.Errorf("spec[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestVerifyExecuteRejectsMultiplePrimaries(t *testing.T) {
	e := &Engine{Config: Config{
		RootPath: ".",
		ExecStruct: []process.Execute{
			{Cmd: "a", Type: process.Primary},
			{Cmd: "b", Type: process.Primary},
		},
	}}
	if err := e.verifyExecute(); err == nil {
		t.Fatal("expected error for two primary executes")
	}
}

func TestVerifyExecuteRequiresAtLeastOne(t *testing.T) {
	e := &Engine{Config: Config{RootPath: "."}}
	if err := e.verifyExecute(); err == nil {
		t.Fatal("expected error when no executes are configured")
	}
}

func TestVerifyExecuteAcceptsStructuredCommand(t *testing.T) {
	e := &Engine{Config: Config{
		RootPath: ".",
		ExecStruct: []process.Execute{{
			Name: "app", Command: []string{"go", "run", "."},
			Env: map[string]string{"WAILS_DEV": "true"}, Type: process.Primary,
		}},
	}}
	if err := e.verifyExecute(); err != nil {
		t.Fatalf("verifyExecute: %v", err)
	}
}

func TestVerifyExecuteValidatesLifecycleContracts(t *testing.T) {
	tests := []process.Execute{
		{Command: []string{"app"}, Type: process.Primary, ShutdownTimeout: "later"},
		{Command: []string{"app"}, Type: process.Primary, Readiness: &process.Readiness{TCP: "localhost:9245", Timeout: "never"}},
		{Command: []string{"app"}, Type: process.Primary, Readiness: &process.Readiness{}},
		{Command: []string{"app"}, Type: process.Primary, ExitPolicy: "restart"},
	}
	for _, spec := range tests {
		e := &Engine{Config: Config{RootPath: ".", ExecStruct: []process.Execute{spec}}}
		if err := e.verifyExecute(); err == nil {
			t.Fatalf("expected lifecycle validation failure for %+v", spec)
		}
	}
}

func TestVerifyExecuteRejectsAmbiguousCommand(t *testing.T) {
	e := &Engine{Config: Config{
		RootPath: ".",
		ExecStruct: []process.Execute{{
			Cmd: "go run .", Command: []string{"go", "run", "."}, Type: process.Primary,
		}},
	}}
	if err := e.verifyExecute(); err == nil {
		t.Fatal("expected cmd and command combination to be rejected")
	}
}

func TestVerifyConfigRejectsAmbiguousBackgroundCommand(t *testing.T) {
	e := &Engine{Config: Config{
		RootPath:         ".",
		BackgroundStruct: process.Execute{Cmd: "npm run dev", Command: []string{"npm", "run", "dev"}},
		ExecStruct:       []process.Execute{{Command: []string{"app"}, Type: process.Primary}},
	}}
	if err := e.verifyConfig(); err == nil {
		t.Fatal("expected ambiguous background command to be rejected")
	}
}

func TestIgnoreGitYAMLAliases(t *testing.T) {
	for _, key := range []string{"git", "git_ignore"} {
		t.Run(key, func(t *testing.T) {
			e := &Engine{}
			err := e.StringtoConfigYAML("config:\n  ignore:\n    " + key + ": true\n")
			if err != nil {
				t.Fatal(err)
			}
			if !e.Config.Ignore.IgnoreGit {
				t.Fatalf("%s did not enable IgnoreGit", key)
			}
		})
	}
}

func TestBackgroundStructBecomesProcess(t *testing.T) {
	eng, err := NewEngineFromConfig(Config{
		RootPath:         ".",
		BackgroundStruct: process.Execute{Cmd: "echo bg"},
		ExecStruct:       []process.Execute{{Cmd: "./app", Type: process.Primary}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The background command is registered ahead of the configured executes.
	if got := eng.ProcessManager.GetExecutes(); len(got) != 2 || got[0] != "echo bg" || got[1] != "./app" {
		t.Errorf("executes = %v, want [echo bg, ./app]", got)
	}
}

func TestBackgroundTypeIsIgnored(t *testing.T) {
	// A type set on the background block is dropped: the background command always
	// registers as a background process regardless of what Type was configured.
	eng, err := NewEngineFromConfig(Config{
		RootPath:         ".",
		BackgroundStruct: process.Execute{Cmd: "echo bg", Type: process.Primary},
		ExecStruct:       []process.Execute{{Cmd: "./app", Type: process.Primary}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := eng.ProcessManager.Processes[0].Type; got != process.Background {
		t.Errorf("background process type = %q, want %q (type on the background block must be ignored)", got, process.Background)
	}
}

func TestNormalizeExecutesPrefersStruct(t *testing.T) {
	e := &Engine{Config: Config{
		RootPath:   ".",
		ExecStruct: []process.Execute{{Cmd: "./app", Type: process.Primary}},
		ExecList:   []string{"should", "be", "ignored"},
	}}
	e.normalizeExecutes()
	if len(e.Config.ExecStruct) != 1 || e.Config.ExecStruct[0].Cmd != "./app" {
		t.Fatalf("ExecStruct should be preferred over ExecList: %+v", e.Config.ExecStruct)
	}
}
