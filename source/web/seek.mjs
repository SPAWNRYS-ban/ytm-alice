export function clampPosition(ms, durationMs) {
  const value = Number.isFinite(ms) ? Math.floor(ms) : 0;
  if (value < 0) return 0;
  const duration = Number.isFinite(durationMs) ? Math.floor(durationMs) : 0;
  if (duration > 0 && value > duration - 1000) {
    return Math.max(0, duration - 1000);
  }
  return value;
}
