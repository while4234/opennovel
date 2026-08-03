const DEFAULT_BASE_DELAY_MS = 1000;
const DEFAULT_MAX_DELAY_MS = 15000;
export const SSE_STALE_TIMEOUT_MS = 45_000;

export function parseSSEMessage(message) {
  if (!message || typeof message.data !== 'string') {
    return { event: null, error: new Error('事件数据为空') };
  }
  try {
    const event = JSON.parse(message.data);
    if (!event || typeof event !== 'object') {
      return { event: null, error: new Error('事件数据格式无效') };
    }
    return { event, error: null };
  } catch (error) {
    return { event: null, error };
  }
}

export function nextSSEReconnectDelay(attempt, random = Math.random, options = {}) {
  const base = Number(options.baseDelayMs || DEFAULT_BASE_DELAY_MS);
  const maximum = Number(options.maxDelayMs || DEFAULT_MAX_DELAY_MS);
  const exponent = Math.max(0, Math.min(Number(attempt) || 0, 8));
  const uncapped = base * (2 ** exponent);
  const capped = Math.min(maximum, uncapped);
  const jitter = 0.8 + (Math.max(0, Math.min(1, Number(random()) || 0)) * 0.4);
  return Math.round(capped * jitter);
}

export function isSSEConnectionStale(
  lastActivityAt,
  now = Date.now(),
  timeoutMs = SSE_STALE_TIMEOUT_MS
) {
  const lastActivity = Number(lastActivityAt || 0);
  const currentTime = Number(now || 0);
  const timeout = Number(timeoutMs || 0);
  if (!lastActivity || !currentTime || timeout <= 0 || currentTime < lastActivity) {
    return false;
  }
  return currentTime - lastActivity >= timeout;
}

export function shouldRefreshSSESnapshot(event, lastSeq, historyLimit = 0) {
  const seq = Number(event?.seq || 0);
  const previous = Number(lastSeq || 0);
  if (!seq || !previous || seq <= previous + 1) {
    return false;
  }
  if (!historyLimit) {
    return true;
  }
  return seq - previous > Number(historyLimit);
}
