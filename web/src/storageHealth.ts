import type { StorageDataPlaneHealth, StorageHealthCheck, StorageTargetHealth } from "./types";

export function storageDataPlane(health?: StorageTargetHealth | null): StorageDataPlaneHealth[] {
  return Array.isArray(health?.data_plane) ? health.data_plane : [];
}

export function storageChecks(health?: StorageTargetHealth | null): Record<string, StorageHealthCheck> {
  const checks = health?.checks;
  return checks && typeof checks === "object" && !Array.isArray(checks) ? checks : {};
}

export function storageTargetIsFailure(health?: StorageTargetHealth | null) {
  return Boolean(health && health.control_plane !== "healthy" && health.error);
}
