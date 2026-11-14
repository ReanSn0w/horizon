package timer

import (
	"fmt"
	"os"

	"github.com/ReanSn0w/horizon/internal/session"
)

// prepareWorkspace is called only when a validated timer is about to submit work.
// The resolved path must still match the definition and its durable intent.
func prepareWorkspace(def Definition) error {
	if def.DefaultWorkspace {
		if err := os.MkdirAll(def.Workspace, 0700); err != nil {
			return fmt.Errorf("create timer workspace: %w", err)
		}
	}
	resolved, err := session.NormalizeWorkspace(def.Workspace)
	if err != nil {
		return err
	}
	if resolved != def.Workspace {
		return fmt.Errorf("timer workspace changed since loading; reload before submitting")
	}
	return nil
}
