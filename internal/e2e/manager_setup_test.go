package e2e

import (
	"bytes"
	"github.com/ReanSn0w/horizon/internal/manager"
)

func initializeHome(_, _, _ string, args ...string) (string, string, error) {
	home := ""
	for i, a := range args {
		if a == "--home" && i+1 < len(args) {
			home = args[i+1]
		}
	}
	var out, diag bytes.Buffer
	err := manager.Initialize(home, &out, &diag)
	return out.String(), diag.String(), err
}
