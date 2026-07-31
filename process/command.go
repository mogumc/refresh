package process

import (
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

func (p *Process) command() *exec.Cmd {
	if len(p.Command) > 0 {
		return exec.Command(p.Command[0], p.Command[1:]...)
	}
	return generateExec(p.Exec)
}

func (p *Process) displayCommand() string {
	if p.Exec != "" {
		return p.Exec
	}
	return strings.Join(p.Command, " ")
}

func cloneEnvironment(environment map[string]string) map[string]string {
	if environment == nil {
		return nil
	}
	result := make(map[string]string, len(environment))
	for name, value := range environment {
		result[name] = value
	}
	return result
}

func processEnvironment(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}
	type environmentValue struct {
		name  string
		value string
	}
	values := make(map[string]environmentValue)
	for _, item := range os.Environ() {
		name, value, found := strings.Cut(item, "=")
		if found {
			values[environmentKey(name)] = environmentValue{name: name, value: value}
		}
	}
	for name, value := range overrides {
		values[environmentKey(name)] = environmentValue{name: name, value: value}
	}
	names := make([]string, 0, len(values))
	for key := range values {
		names = append(names, key)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, key := range names {
		item := values[key]
		result = append(result, item.name+"="+item.value)
	}
	return result
}

func environmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
