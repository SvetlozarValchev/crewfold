package buildinfo

import (
	"runtime"
	"testing"
)

func TestCurrentIsValidAndUsesRuntimeFacts(t *testing.T) {
	t.Parallel()

	info := Current()
	if err := info.Validate(); err != nil {
		t.Fatalf("Current().Validate() error = %v", err)
	}
	if info.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q, want %q", info.GoVersion, runtime.Version())
	}
	wantPlatform := runtime.GOOS + "/" + runtime.GOARCH
	if info.Platform != wantPlatform {
		t.Fatalf("Platform = %q, want %q", info.Platform, wantPlatform)
	}
}

func TestValidateRejectsInvalidBuildTime(t *testing.T) {
	t.Parallel()

	info := Current()
	info.BuiltAt = "yesterday"
	if err := info.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid build time error")
	}
}
