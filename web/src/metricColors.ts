export function metricSeriesColor(index: number) {
  const colors = ["#0e7490", "#b0413e", "#3d7a57", "#906a1f", "#155e75", "#6e6a9a", "#58748a", "#8a5a44"];
  return colors[index % colors.length];
}
