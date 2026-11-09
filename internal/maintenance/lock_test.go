package maintenance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGateExcludesMaintenanceAndCommands(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, ".manager"), 0700)
	shared, err := Acquire(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if unlock, err := Acquire(home, true); err == nil {
		unlock()
		t.Fatal("maintenance while running")
	}
	shared()
	exclusive, err := Acquire(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if unlock, err := Acquire(home, false); err == nil {
		unlock()
		t.Fatal("command during maintenance")
	}
	exclusive()
	shared, err = Acquire(home, false)
	if err != nil {
		t.Fatal(err)
	}
	shared()
}
