package execution

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

func TestM22CodexAppServerProcessUsesStructuredStdioWithoutShell(t *testing.T) {
	if os.Getenv("CREWFOLD_CODEX_APPSERVER_HELPER") == "1" {
		scanner := bufio.NewScanner(os.Stdin)
		encoder := json.NewEncoder(os.Stdout)
		for scanner.Scan() {
			var request map[string]any
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				os.Exit(2)
			}
			if request["method"] == "initialize" {
				_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{
					"codexHome": "/fixture", "platformFamily": "unix", "platformOs": "linux", "userAgent": "fixture",
				}})
			}
		}
		os.Exit(0)
	}
	t.Setenv("CREWFOLD_CODEX_APPSERVER_HELPER", "1")
	transport, err := StartCodexAppServer(CodexAppServerProcessOptions{Executable: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewCodexAppServerClient(transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}
