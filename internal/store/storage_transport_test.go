package store

import "testing"

func TestNormalizeStorageTargetHealthRepairsLegacyNullCollections(t *testing.T) {
	health := &StorageTargetHealth{}
	normalizeStorageTargetHealth(health)
	if health.Checks == nil || health.DataPlane == nil {
		t.Fatalf("health collections remain nil: %#v", health)
	}
}

func TestSelectStorageInitiatorPrefersHealthyNASPath(t *testing.T) {
	target := &StorageTarget{Health: &StorageTargetHealth{DataPlane: []StorageDataPlaneHealth{{
		ResourceID:       "compute-1",
		ComputeInitiated: StorageConnectionHealth{Status: StorageStatusHealthy},
		NASInitiated:     StorageConnectionHealth{Status: StorageStatusHealthy},
	}}}}
	if got := SelectStorageInitiator(target, "compute-1"); got != StorageInitiatorNAS {
		t.Fatalf("initiator = %q, want %q", got, StorageInitiatorNAS)
	}
}

func TestSelectStorageInitiatorFallsBackToCompute(t *testing.T) {
	tests := []struct {
		name   string
		target *StorageTarget
	}{
		{name: "no health", target: &StorageTarget{}},
		{name: "reverse unavailable", target: &StorageTarget{Health: &StorageTargetHealth{DataPlane: []StorageDataPlaneHealth{{
			ResourceID:       "compute-1",
			ComputeInitiated: StorageConnectionHealth{Status: StorageStatusHealthy},
			NASInitiated:     StorageConnectionHealth{Status: StorageStatusUnreachable},
		}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SelectStorageInitiator(tt.target, "compute-1"); got != StorageInitiatorCompute {
				t.Fatalf("initiator = %q, want %q", got, StorageInitiatorCompute)
			}
		})
	}
}
