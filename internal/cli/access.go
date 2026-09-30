package cli

import (
	"fmt"
	"os"
)

const inheritedAccessEnv = "HORIZON_INHERITED_ACCESS"

func effectiveAccess(requested string) (string, error) {
	if inherited, ok := os.LookupEnv(inheritedAccessEnv); ok {
		if !validAccess(inherited) {
			return "", fmt.Errorf("invalid inherited access mode %q", inherited)
		}
		return inherited, nil
	}
	if requested == "" {
		return "write", nil
	}
	if !validAccess(requested) {
		return "", fmt.Errorf("invalid access mode %q", requested)
	}
	return requested, nil
}

func validAccess(value string) bool {
	return value == "read" || value == "write" || value == "full"
}
