package localapi

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maximumAttachRuntimeCharacters    = 128
	maximumAttachExecutableCharacters = 4096
	maximumAttachArguments            = 16
	maximumAttachArgumentCharacters   = 4096
	maximumAttachEnvironment          = 8
	maximumAttachEnvironmentName      = 64
	maximumAttachEnvironmentValue     = 4096
)

// ValidateRunAttachResult enforces the one current provider-neutral attach
// contract before a runtime specification crosses or leaves the local API.
func ValidateRunAttachResult(result RunAttachResult, requestedRunID string) error {
	if result.Schema != RunAttachSchema || result.Type != "run_attach" || !validAttachRunID(requestedRunID) ||
		!validAttachRunID(result.RunID) || result.RunID != requestedRunID {
		return fmt.Errorf("run.attach returned unexpected result schema %q, type %q, or run %q for requested run %q", result.Schema, result.Type, result.RunID, requestedRunID)
	}
	if err := validateAttachText("runtime", result.Runtime, 1, maximumAttachRuntimeCharacters); err != nil {
		return err
	}
	if err := validateAttachText("executable", result.Executable, 1, maximumAttachExecutableCharacters); err != nil {
		return err
	}
	if result.Arguments == nil || len(result.Arguments) > maximumAttachArguments {
		return fmt.Errorf("run.attach returned invalid arguments: expected a present array with at most %d items", maximumAttachArguments)
	}
	for index, argument := range result.Arguments {
		if err := validateAttachText(fmt.Sprintf("argument %d", index), argument, 0, maximumAttachArgumentCharacters); err != nil {
			return err
		}
	}
	if len(result.Environment) > maximumAttachEnvironment {
		return fmt.Errorf("run.attach returned invalid environment: at most %d entries are allowed", maximumAttachEnvironment)
	}
	for name, value := range result.Environment {
		if !validAttachEnvironmentName(name) {
			return fmt.Errorf("run.attach returned invalid environment name %q", name)
		}
		if err := validateAttachText("environment value", value, 0, maximumAttachEnvironmentValue); err != nil {
			return err
		}
	}
	return nil
}

func validAttachRunID(value string) bool {
	if len(value) != len("run_")+32 || !strings.HasPrefix(value, "run_") {
		return false
	}
	for _, character := range value[len("run_"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validateAttachText(label, value string, minimum, maximum int) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("run.attach returned invalid %s text", label)
	}
	length := utf8.RuneCountInString(value)
	if length < minimum || length > maximum {
		return fmt.Errorf("run.attach returned invalid %s length %d; expected %d to %d characters", label, length, minimum, maximum)
	}
	return nil
}

func validAttachEnvironmentName(value string) bool {
	if len(value) == 0 || len(value) > maximumAttachEnvironmentName || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value[1:] {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
