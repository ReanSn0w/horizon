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

func TestReadOnlyAcquireAvoidsInitialization(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".manager"), 0700); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireReadOnly(home)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(filepath.Join(home, ".manager", "maintenance.lock")); !os.IsNotExist(err) {
		t.Fatalf("read-only acquisition initialized lock: %v", err)
	}
	initialize, err := Acquire(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireReadOnly(home); err == nil {
		release()
		t.Error("read-only acquisition bypassed exclusive maintenance")
	}
	initialize()
	release, err = AcquireReadOnly(home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if exclusive, err := Acquire(home, true); err == nil {
		exclusive()
		t.Fatal("exclusive maintenance bypassed read-only acquisition")
	}
}
