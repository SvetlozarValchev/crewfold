package localapi

import (
	"strings"
	"testing"
)

func TestRunAttachResultEnforcesEveryPublishedBound(t *testing.T) {
	valid := RunAttachResult{
		Schema: RunAttachSchema, Type: "run_attach", RunID: "run_" + strings.Repeat("a", 32), Runtime: strings.Repeat("r", 128),
		Executable: strings.Repeat("e", 4096), Arguments: make([]string, 16),
		Environment: map[string]string{
			"A": strings.Repeat("v", 4096), "B": "", "C": "", "D": "",
			"E": "", "F": "", "G": "", "H": "",
		},
	}
	for index := range valid.Arguments {
		valid.Arguments[index] = strings.Repeat("a", 4096)
	}
	if err := ValidateRunAttachResult(valid, valid.RunID); err != nil {
		t.Fatalf("exact maximum attach contract rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*RunAttachResult)
	}{
		{name: "runtime empty", mutate: func(result *RunAttachResult) { result.Runtime = "" }},
		{name: "runtime long", mutate: func(result *RunAttachResult) { result.Runtime += "x" }},
		{name: "executable empty", mutate: func(result *RunAttachResult) { result.Executable = "" }},
		{name: "executable long", mutate: func(result *RunAttachResult) { result.Executable += "x" }},
		{name: "arguments absent", mutate: func(result *RunAttachResult) { result.Arguments = nil }},
		{name: "arguments many", mutate: func(result *RunAttachResult) { result.Arguments = append(result.Arguments, "x") }},
		{name: "argument long", mutate: func(result *RunAttachResult) { result.Arguments[0] += "x" }},
		{name: "environment many", mutate: func(result *RunAttachResult) { result.Environment["I"] = "" }},
		{name: "environment name lowercase", mutate: func(result *RunAttachResult) { result.Environment = map[string]string{"bad": "value"} }},
		{name: "environment name long", mutate: func(result *RunAttachResult) {
			result.Environment = map[string]string{"A" + strings.Repeat("B", 64): "value"}
		}},
		{name: "environment value long", mutate: func(result *RunAttachResult) { result.Environment = map[string]string{"A": strings.Repeat("v", 4097)} }},
		{name: "invalid UTF-8", mutate: func(result *RunAttachResult) { result.Runtime = string([]byte{0xff}) }},
		{name: "nul", mutate: func(result *RunAttachResult) { result.Arguments[0] = "bad\x00argument" }},
		{name: "malformed run", mutate: func(result *RunAttachResult) { result.RunID = "run_A" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			result.Arguments = append([]string(nil), valid.Arguments...)
			result.Environment = make(map[string]string, len(valid.Environment))
			for key, value := range valid.Environment {
				result.Environment[key] = value
			}
			test.mutate(&result)
			if err := ValidateRunAttachResult(result, valid.RunID); err == nil {
				t.Fatalf("invalid attach result accepted: %#v", result)
			}
		})
	}
}
