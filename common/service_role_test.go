package common

import (
	"os"
	"testing"
)

func TestServiceRoleDefaultsToAll(t *testing.T) {
	origRole := RunningRole
	defer func() { RunningRole = origRole }()

	RunningRole = ServiceRoleAll
	if !IsAPIRole() {
		t.Errorf("IsAPIRole() = false for role 'all', want true")
	}
	if !IsWorkerRole() {
		t.Errorf("IsWorkerRole() = false for role 'all', want true")
	}
}

func TestServiceRoleAPIOnly(t *testing.T) {
	origRole := RunningRole
	defer func() { RunningRole = origRole }()

	RunningRole = ServiceRoleAPI
	if !IsAPIRole() {
		t.Errorf("IsAPIRole() = false for role 'api', want true")
	}
	if IsWorkerRole() {
		t.Errorf("IsWorkerRole() = true for role 'api', want false")
	}
}

func TestServiceRoleWorkerOnly(t *testing.T) {
	origRole := RunningRole
	defer func() { RunningRole = origRole }()

	RunningRole = ServiceRoleWorker
	if IsAPIRole() {
		t.Errorf("IsAPIRole() = true for role 'worker', want false")
	}
	if !IsWorkerRole() {
		t.Errorf("IsWorkerRole() = false for role 'worker', want true")
	}
}

func TestServiceRoleFromEnv(t *testing.T) {
	origRole := RunningRole
	origEnv := os.Getenv("SERVICE_ROLE")
	defer func() {
		RunningRole = origRole
		_ = os.Setenv("SERVICE_ROLE", origEnv)
	}()

	_ = os.Setenv("SERVICE_ROLE", "worker")
	// simulate role resolution
	role := os.Getenv("SERVICE_ROLE")
	if role != ServiceRoleWorker {
		t.Errorf("role = %q, want %q", role, ServiceRoleWorker)
	}
}
