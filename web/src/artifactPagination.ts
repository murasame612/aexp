export const initialArtifactRows = 30;

export function artifactRequestLimit(showAll: boolean) {
  return showAll ? 0 : initialArtifactRows + 1;
}

export function artifactPage<T>(items: T[], showAll: boolean) {
  return {
    visibleItems: showAll ? items : items.slice(0, initialArtifactRows),
    hasMore: !showAll && items.length > initialArtifactRows
  };
}
