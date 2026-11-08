//go:build openrc_integration

// Integration tests that drive real rc-service and rc-update. They install and
// remove a service called gopenrc-integration, so they need root and a machine
// running OpenRC:
//
//	go test -tags openrc_integration -v
//
// Without the tag these never build, so `go test ./...` stays safe to run
// anywhere.
package goOpenRC

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	testService = "gopenrc-integration"
	daemonPath  = "/usr/local/bin/" + testService
	startsPath  = "/tmp/" + testService + ".starts"
)

// writeTestDaemon installs a daemon that appends its pid to startsPath every
// time it is started, so a respawn is directly observable.
func writeTestDaemon(t *testing.T) {
	t.Helper()
	script := "#!/bin/sh\necho $$ >> " + startsPath + "\nwhile true; do sleep 1; done\n"
	if e := os.WriteFile(daemonPath, []byte(script), 0755); e != nil {
		t.Fatalf("writing test daemon: %v", e)
	}
	if e := os.Chmod(daemonPath, 0755); e != nil {
		t.Fatalf("chmod test daemon: %v", e)
	}
}

// starts returns the pid of every recorded start, oldest first.
func starts(t *testing.T) []int {
	t.Helper()
	b, e := os.ReadFile(startsPath)
	if e != nil {
		if os.IsNotExist(e) {
			return nil
		}
		t.Fatalf("reading %s: %v", startsPath, e)
	}
	var pids []int
	for _, line := range strings.Fields(string(b)) {
		if pid, e := strconv.Atoi(line); e == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// waitFor polls until condition holds or the timeout expires.
func waitFor(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return condition()
}

func TestIntegration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to write to " + InitDir)
	}
	if !Available() {
		t.Skip("needs openrc: rc-service and rc-update were not found")
	}

	// leave nothing behind from an earlier run
	if status, _ := StatusService(testService); status != nil && status.Exists {
		_ = DeleteService(testService)
	}
	_ = os.Remove(startsPath)
	writeTestDaemon(t)
	t.Cleanup(func() {
		_ = DeleteService(testService)
		_ = os.Remove(daemonPath)
		_ = os.Remove(startsPath)
	})

	service := &Service{
		Name:        testService,
		ExecStart:   daemonPath,
		Restart:     "on-failure",
		RestartSec:  "1s",
		Description: "go-openrc integration test",
		After:       "network.target",
	}

	t.Run("missing service", func(t *testing.T) {
		status, e := StatusService(testService)
		if e != nil {
			t.Fatalf("unknown service should not error: %v", e)
		}
		if status.Exists {
			t.Error("Exists should be false before create")
		}
		if e = StartService(testService); e == nil || !strings.Contains(e.Error(), "not found") {
			t.Errorf("StartService on a missing service: got %v, want ErrServiceNotFound", e)
		}
	})

	t.Run("create", func(t *testing.T) {
		if e := CreateService(service); e != nil {
			t.Fatalf("CreateService: %v", e)
		}
		info, e := os.Stat(InitDir + testService)
		if e != nil {
			t.Fatalf("init script not written: %v", e)
		}
		if mode := info.Mode().Perm(); mode != 0755 {
			t.Errorf("init script mode = %v, want 0755", mode)
		}
	})

	t.Run("status after create", func(t *testing.T) {
		status, e := StatusService(testService)
		if e != nil {
			t.Fatalf("StatusService: %v", e)
		}
		if !status.Exists {
			t.Error("Exists should be true")
		}
		if !status.Enabled {
			t.Error("Enabled should be true")
		}
		if !status.Active || status.State != "started" {
			t.Errorf("State = %q, Active = %v, want started/true", status.State, status.Active)
		}
		found := false
		for _, level := range status.Runlevels {
			if level == "default" {
				found = true
			}
		}
		if !found {
			t.Errorf("Runlevels = %v, want to contain default", status.Runlevels)
		}
		if status.MainPID == 0 {
			t.Error("MainPID should be set")
		}
	})

	t.Run("respawn after the daemon is killed", func(t *testing.T) {
		if !waitFor(10*time.Second, func() bool { return len(starts(t)) > 0 }) {
			t.Fatal("the daemon never recorded a start")
		}
		before := starts(t)
		pid := before[len(before)-1]

		// kill the supervised process, not supervise-daemon itself
		if e := syscall.Kill(pid, syscall.SIGKILL); e != nil {
			t.Fatalf("killing the daemon (pid %d): %v", pid, e)
		}

		// RestartSec is 1s, so give supervise-daemon room and then some
		if !waitFor(30*time.Second, func() bool { return len(starts(t)) > len(before) }) {
			t.Fatalf("daemon was not respawned within 30s (still %d starts)", len(before))
		}
		after := starts(t)
		if newPid := after[len(after)-1]; newPid == pid {
			t.Errorf("respawned with the same pid %d", newPid)
		} else {
			t.Logf("respawned: pid %d -> %d after %d starts", pid, newPid, len(after))
		}

		status, e := StatusService(testService)
		if e != nil {
			t.Fatalf("StatusService: %v", e)
		}
		if !status.Active {
			t.Errorf("service should be active again, State = %q", status.State)
		}
	})

	t.Run("stop", func(t *testing.T) {
		if e := StopService(testService); e != nil {
			t.Fatalf("StopService: %v", e)
		}
		status, e := StatusService(testService)
		if e != nil {
			t.Fatalf("StatusService: %v", e)
		}
		if status.Active {
			t.Errorf("service should be stopped, State = %q", status.State)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if e := DeleteService(testService); e != nil {
			t.Fatalf("DeleteService: %v", e)
		}
		if _, e := os.Stat(InitDir + testService); !os.IsNotExist(e) {
			t.Errorf("init script should be gone, stat gave %v", e)
		}
		status, e := StatusService(testService)
		if e != nil {
			t.Fatalf("StatusService: %v", e)
		}
		if status.Exists {
			t.Error("Exists should be false after delete")
		}
		if status.Enabled || len(status.Runlevels) != 0 {
			t.Errorf("should be in no runlevel, got %v", status.Runlevels)
		}
	})
}
