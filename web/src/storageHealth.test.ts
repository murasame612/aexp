import { describe, expect, it } from "vitest";
import { storageChecks, storageDataPlane, storageTargetIsFailure } from "./storageHealth";
import type { StorageTargetHealth } from "./types";

describe("storage health normalization", () => {
  it("treats legacy null collections as empty", () => {
    const health = { data_plane: null, checks: null } as unknown as StorageTargetHealth;
    expect(storageDataPlane(health)).toEqual([]);
    expect(storageChecks(health)).toEqual({});
  });

  it("does not turn a healthy NAS control plane into NAS failure when one compute edge is down", () => {
    const health = { control_plane: "healthy", error: "", usable: true, data_plane: [{ resource_id: "gpu-a", status: "unreachable" }] } as unknown as StorageTargetHealth;
    expect(storageTargetIsFailure(health)).toBe(false);
    expect(storageDataPlane(health)[0].status).toBe("unreachable");
  });
});
