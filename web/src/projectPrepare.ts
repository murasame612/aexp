import type { ProjectDefinition, ProjectTarget } from "./types";

export function targetDraftErrors(target: Partial<ProjectTarget>) {
  const errors: string[] = [];
  if (!target.name?.trim()) errors.push("name is required");
  if (!target.resource_id?.trim()) errors.push("resource is required");
  if (!target.cwd?.trim()) errors.push("cwd is required");
  if (!target.prepare_command?.trim()) errors.push("prepare command is required");
  return errors;
}

export function canStartPrepare(target: ProjectTarget, hasReviewedPlan: boolean) {
  return hasReviewedPlan && target.readiness !== "checking" && Boolean(target.prepare_command?.trim());
}

export function targetConfigDrifted(project: ProjectDefinition, target: ProjectTarget) {
  return target.readiness === "ready" && Boolean(project.config_hash) && target.observed_config_hash !== project.config_hash;
}
