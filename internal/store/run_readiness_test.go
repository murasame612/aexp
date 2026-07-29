package store

import (
	"context"
	"errors"
	"testing"
)

type readinessDatasetReader struct {
	versions map[string]*DatasetVersion
	err      error
}

func (r readinessDatasetReader) GetDatasetVersion(_ context.Context, id string) (*DatasetVersion, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.versions[id], nil
}

func TestCheckRunProvenanceRequiresVerifiedDatasetIdentity(t *testing.T) {
	base := RunProvenance{
		Datasets: []RunDatasetInput{{
			ID: "dataset_good_v1", DatasetID: "good", Version: "v1", ManifestSHA256: "sha256:manifest",
		}},
		Seeds:               []int64{41},
		ProjectConfigSHA256: "sha256:config",
		GitCommit:           "abcdef",
		SplitProtocol:       "split-v1",
		EvaluationProtocol:  "eval-v1",
	}
	tests := []struct {
		name       string
		provenance RunProvenance
		versions   map[string]*DatasetVersion
		wantCode   string
	}{
		{name: "missing dataset", provenance: func() RunProvenance { value := base; value.Datasets = nil; return value }(), wantCode: "dataset_missing"},
		{name: "unknown dataset", provenance: base, versions: map[string]*DatasetVersion{}, wantCode: "dataset_not_registered"},
		{name: "registered only", provenance: base, versions: map[string]*DatasetVersion{
			"dataset_good_v1": {ID: "dataset_good_v1", DatasetID: "good", Version: "v1", ManifestSHA256: "sha256:manifest", State: DatasetStateRegistered},
		}, wantCode: "dataset_not_verified"},
		{name: "manifest mismatch", provenance: base, versions: map[string]*DatasetVersion{
			"dataset_good_v1": {ID: "dataset_good_v1", DatasetID: "good", Version: "v1", ManifestSHA256: "sha256:other", State: DatasetStateVerified},
		}, wantCode: "dataset_provenance_mismatch"},
		{name: "verified", provenance: base, versions: map[string]*DatasetVersion{
			"dataset_good_v1": {ID: "dataset_good_v1", DatasetID: "good", Version: "v1", ManifestSHA256: "sha256:manifest", State: DatasetStateVerified},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blockers, err := CheckRunProvenance(context.Background(), readinessDatasetReader{versions: test.versions}, test.provenance)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantCode == "" {
				if len(blockers) != 0 {
					t.Fatalf("blockers = %#v", blockers)
				}
				return
			}
			if !hasReadinessCode(blockers, test.wantCode) {
				t.Fatalf("blockers = %#v, want %q", blockers, test.wantCode)
			}
		})
	}
}

func TestCheckStoredRunProvenanceReportsMalformedJSONWithoutMissingAlias(t *testing.T) {
	run := &Run{
		DatasetsJSON:        `{`,
		SeedsJSON:           `[41]`,
		ProjectConfigSHA256: "sha256:config",
		GitCommit:           "abcdef",
		SplitProtocol:       "split-v1",
		EvaluationProtocol:  "eval-v1",
	}
	blockers, err := CheckStoredRunProvenance(context.Background(), readinessDatasetReader{}, run)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReadinessCode(blockers, "datasets_invalid") || hasReadinessCode(blockers, "dataset_missing") {
		t.Fatalf("blockers = %#v", blockers)
	}
}

func TestCheckRunProvenanceReturnsRegistryErrors(t *testing.T) {
	_, err := CheckRunProvenance(context.Background(), readinessDatasetReader{err: errors.New("db unavailable")}, RunProvenance{
		Datasets: []RunDatasetInput{{ID: "dataset", DatasetID: "d", Version: "v1", ManifestSHA256: "hash"}},
		Seeds:    []int64{1}, ProjectConfigSHA256: "config", GitCommit: "commit", SplitProtocol: "split", EvaluationProtocol: "eval",
	})
	if err == nil {
		t.Fatal("registry error was converted into a provenance blocker")
	}
}

func hasReadinessCode(blockers []RunReadinessBlocker, code string) bool {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}
