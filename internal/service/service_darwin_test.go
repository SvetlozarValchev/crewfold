//go:build darwin

package service

import (
	"strings"
	"testing"
)

func TestLaunchdPlistEscapesDaemonConfiguration(t *testing.T) {
	plist, err := launchdPlist(Config{
		Executable: "/Applications/Crewfold & Tools/crewfold",
		DataDir:    "/Users/owner/Library/Application Support/Crewfold",
		Endpoint:   "/Users/owner/Library/Caches/Crewfold/runtime/crewfold.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"<string>dev.crewfold.Crewfold</string>",
		"<string>/Applications/Crewfold &amp; Tools/crewfold</string>",
		"<string>--data-dir</string>",
		"<string>/Users/owner/Library/Application Support/Crewfold</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>ProcessType</key><string>Background</string>",
	} {
		if !strings.Contains(plist, expected) {
			t.Fatalf("launchd plist does not contain %q:\n%s", expected, plist)
		}
	}
}
