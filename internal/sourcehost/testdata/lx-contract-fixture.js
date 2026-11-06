/*!
 * @name 聆澜音源(赞助版)[多Key智能调度]
 * @description 支持所有平台全音质，内置多Key限流调度、请求去重、短期缓存与错误隔离
 * @version v1.3.1
 * @author 时迁酱&guoyue2010
 */
const DEV_ENABLE = false;
const UPDATE_ENABLE = false;
const API_BASE_URL = "https://source.shiqianjiang.cn/api";
const API_URL = `${API_BASE_URL}/music`;
const API_KEYS = ["contract-key-1","contract-key-2","contract-key-3","contract-key-4"];
const SCRIPT_MD5 = "c412dae33b8911864db494d23087743c";

const REQUEST_TIMEOUT_MS = 5000;
const TOTAL_REQUEST_TIMEOUT_MS = 10 * 1000;
const MAX_TRANSIENT_ATTEMPTS = 2;
const PER_KEY_MAX_CONCURRENCY = 1;
const MAX_WAIT_QUEUE_SIZE = 50;
const URL_CACHE_TTL_MS = 30 * 1000;
const URL_CACHE_MAX_ENTRIES = 200;
const TRANSIENT_COOLDOWN_BASE_MS = 15 * 1000;
const TRANSIENT_COOLDOWN_MAX_MS = 3 * 60 * 1000;
const RATE_LIMIT_COOLDOWN_MS = 60 * 1000;
const INVALID_KEY_COOLDOWN_MS = 30 * 60 * 1000;
const UPDATE_CHECK_DELAY_MS = 1500;

const MUSIC_QUALITY = {
  kw: ["128k", "320k", "flac", "flac24bit", "hires"],
  mg: ["128k", "320k", "flac", "flac24bit", "hires"],
  kg: ["128k", "320k", "flac", "flac24bit", "hires", "atmos", "master"],
  tx: ["128k", "320k", "flac", "flac24bit", "hires", "atmos", "atmos_plus", "master"],
  wy: ["128k", "320k", "flac", "flac24bit", "hires", "atmos", "master"],
};
const MUSIC_SOURCE = Object.keys(MUSIC_QUALITY);

const { EVENT_NAMES, request, on, send, env, version } = globalThis.lx;
const USER_AGENT = env ? `lx-music-${env}/${version}` : `lx-music-request/${version}`;

class TimeoutError extends Error {
  constructor(ms, message = `请求超时(${ms}ms)`) {
    super(message);
    this.name = "TimeoutError";
    this.code = "ETIMEDOUT";
  }
}

class DeadlineError extends TimeoutError {
  constructor() {
    super(TOTAL_REQUEST_TIMEOUT_MS, `获取播放地址超时(${TOTAL_REQUEST_TIMEOUT_MS}ms)`);
    this.name = "DeadlineError";
  }
}

class QueueFullError extends Error {
  constructor() {
    super("当前请求较多，请稍后重试");
    this.name = "QueueFullError";
    this.code = "EQUEUEFULL";
  }
}

class ApiError extends Error {
  constructor(message, options = {}) {
    super(message);
    this.name = "ApiError";
    this.code = options.code;
    this.retryable = options.retryable ?? false;
    this.penalizeNode = options.penalizeNode ?? false;
    this.cooldownMs = options.cooldownMs ?? null;
  }
}

const keyPool = API_KEYS.map((key, index) => ({
  index,
  key,
  cooldownUntil: 0,
  cooldownReason: "",
  inFlight: 0,
  lastUsedAt: 0,
  consecutiveFailures: 0,
  timeoutCount: 0,
  failCount: 0,
  successCount: 0,
  averageLatencyMs: 0,
}));
const waitQueue = [];
const pendingRequests = new Map();
const musicUrlCache = new Map();
let nextKeyCursor = 0;
let queueWakeTimer = null;

const nowMs = () => Date.now();

const debugLog = (...args) => {
  if (!DEV_ENABLE) return;
  console.log("[source-debug]", ...args);
};

const createHeaders = (key) => {
  const headers = {
    Accept: "application/json",
    "User-Agent": USER_AGENT,
  };
  if (key) headers["X-API-Key"] = key;
  return headers;
};

const httpFetch = (
  url,
  options = { method: "GET" },
  timeoutMs = REQUEST_TIMEOUT_MS,
  deadlineAt = Number.POSITIVE_INFINITY,
) => {
  const startedAt = nowMs();
  const remainingMs = deadlineAt - startedAt;
  if (remainingMs <= 0) return Promise.reject(new DeadlineError());
  const requestTimeoutMs = Math.min(timeoutMs, remainingMs);
  const expiresAt = startedAt + requestTimeoutMs;
  const deadlineLimited = remainingMs <= timeoutMs;
  const timeoutError = () =>
    deadlineLimited || nowMs() >= deadlineAt
      ? new DeadlineError()
      : new TimeoutError(requestTimeoutMs);

  return new Promise((resolve, reject) => {
    let settled = false;
    let cancelRequest;
    let shouldCancel = false;
    const cancelTransport = () => {
      if (typeof cancelRequest !== "function") return;
      const cancel = cancelRequest;
      cancelRequest = null;
      try {
        cancel();
      } catch (cancelError) {
        debugLog("取消请求失败", cancelError?.message ?? cancelError);
      }
    };
    const finish = (err, resp, cancel = false) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (cancel) {
        shouldCancel = true;
        cancelTransport();
      }
      if (err) reject(err);
      else resolve(resp);
    };
    const timer = setTimeout(() => finish(timeoutError(), null, true), requestTimeoutMs);
    try {
      if (nowMs() >= deadlineAt) return finish(new DeadlineError());
      cancelRequest = request(url, { ...options, timeout: requestTimeoutMs }, (err, resp) => {
        if (settled) return;
        if (nowMs() >= expiresAt) return finish(timeoutError(), null, true);
        if (deadlineLimited && (err?.code === "ETIMEDOUT" || err?.name === "TimeoutError")) {
          return finish(new DeadlineError(), null, true);
        }
        finish(err, resp, Boolean(err));
      });
      // 同步错误回调可能早于 request() 返回取消句柄。
      if (shouldCancel) cancelTransport();
    } catch (err) {
      finish(err);
    }
  });
};

const parseResponseBody = (body) => {
  if (typeof body !== "string") return body;
  try {
    return JSON.parse(body);
  } catch {
    return null;
  }
};

const getRequestCacheKey = (source, songId, quality) => {
  return JSON.stringify([source, String(songId), quality]);
};

const getCachedMusicUrl = (cacheKey) => {
  const cached = musicUrlCache.get(cacheKey);
  if (!cached) return null;
  if (cached.expiresAt <= nowMs()) {
    musicUrlCache.delete(cacheKey);
    return null;
  }

  // 刷新 Map 插入顺序，维持简单的 LRU 淘汰顺序。
  musicUrlCache.delete(cacheKey);
  musicUrlCache.set(cacheKey, cached);
  return cached.url;
};

const setCachedMusicUrl = (cacheKey, url) => {
  musicUrlCache.delete(cacheKey);
  while (musicUrlCache.size >= URL_CACHE_MAX_ENTRIES) {
    const oldestKey = musicUrlCache.keys().next().value;
    if (oldestKey === undefined) break;
    musicUrlCache.delete(oldestKey);
  }
  musicUrlCache.set(cacheKey, {
    url,
    expiresAt: nowMs() + URL_CACHE_TTL_MS,
  });
};

const reserveNextAvailableNode = (attemptedNodes) => {
  const total = keyPool.length;
  if (!total) return null;
  const ts = nowMs();
  let selectedIndex = -1;
  let selectedInFlight = Number.POSITIVE_INFINITY;

  for (let i = 0; i < total; i += 1) {
    const index = (nextKeyCursor + i) % total;
    const node = keyPool[index];
    if (attemptedNodes.has(node)) continue;
    if (node.cooldownUntil > ts) continue;
    if (node.inFlight >= PER_KEY_MAX_CONCURRENCY) continue;

    if (node.inFlight < selectedInFlight) {
      selectedIndex = index;
      selectedInFlight = node.inFlight;
      if (selectedInFlight === 0) break;
    }
  }

  if (selectedIndex < 0) return null;
  const node = keyPool[selectedIndex];
  node.inFlight += 1;
  node.lastUsedAt = ts;
  nextKeyCursor = (selectedIndex + 1) % total;
  return node;
};

const hasWaitingCandidate = (attemptedNodes, deadlineAt) => {
  return keyPool.some((node) => {
    return !attemptedNodes.has(node) && node.cooldownUntil < deadlineAt;
  });
};

const scheduleQueueWake = () => {
  clearTimeout(queueWakeTimer);
  queueWakeTimer = null;
  const ts = nowMs();
  let wakeAt = Number.POSITIVE_INFINITY;
  for (const waiter of waitQueue) {
    for (const node of keyPool) {
      if (
        !waiter.attemptedNodes.has(node) &&
        node.inFlight < PER_KEY_MAX_CONCURRENCY &&
        node.cooldownUntil > ts &&
        node.cooldownUntil < waiter.deadlineAt
      ) {
        wakeAt = Math.min(wakeAt, node.cooldownUntil);
      }
    }
  }
  if (Number.isFinite(wakeAt)) {
    queueWakeTimer = setTimeout(() => {
      queueWakeTimer = null;
      drainWaitQueue();
    }, wakeAt - ts);
  }
};

const removeWaiter = (waiter) => {
  const index = waitQueue.indexOf(waiter);
  if (index >= 0) waitQueue.splice(index, 1);
  scheduleQueueWake();
};

const acquireNode = (attemptedNodes, deadlineAt) => {
  const remainingMs = deadlineAt - nowMs();
  if (remainingMs <= 0) return Promise.reject(new DeadlineError());

  // 先服务已有等待者，避免新请求抢走刚恢复的 Key。
  drainWaitQueue();
  const node = reserveNextAvailableNode(attemptedNodes);
  if (node) return Promise.resolve(node);
  if (!hasWaitingCandidate(attemptedNodes, deadlineAt)) return Promise.resolve(null);
  if (waitQueue.length >= MAX_WAIT_QUEUE_SIZE) return Promise.reject(new QueueFullError());

  return new Promise((resolve, reject) => {
    const waiter = { attemptedNodes, deadlineAt, resolve, reject, timer: null };
    waiter.timer = setTimeout(() => {
      removeWaiter(waiter);
      reject(new DeadlineError());
    }, remainingMs);
    waitQueue.push(waiter);
    scheduleQueueWake();
  });
};

const drainWaitQueue = () => {
  const ts = nowMs();

  for (let index = 0; index < waitQueue.length; ) {
    const waiter = waitQueue[index];
    if (waiter.deadlineAt <= ts) {
      waitQueue.splice(index, 1);
      clearTimeout(waiter.timer);
      waiter.reject(new DeadlineError());
      continue;
    }

    const node = reserveNextAvailableNode(waiter.attemptedNodes);
    if (node) {
      waitQueue.splice(index, 1);
      clearTimeout(waiter.timer);
      waiter.resolve(node);
      continue;
    }

    if (!hasWaitingCandidate(waiter.attemptedNodes, waiter.deadlineAt)) {
      waitQueue.splice(index, 1);
      clearTimeout(waiter.timer);
      waiter.resolve(null);
      continue;
    }

    index += 1;
  }
  scheduleQueueWake();
};

const releaseNode = (node) => {
  node.inFlight = Math.max(0, node.inFlight - 1);
  drainWaitQueue();
};

const getAdaptiveCooldownMs = (consecutiveFailures) => {
  const exponent = Math.max(0, Math.min(consecutiveFailures - 1, 4));
  const baseCooldown = TRANSIENT_COOLDOWN_BASE_MS * 2 ** exponent;
  const jitter = Math.floor(Math.random() * 1000);
  return Math.min(TRANSIENT_COOLDOWN_MAX_MS, baseCooldown + jitter);
};

const setNodeCooldown = (node, cooldownMs, reason) => {
  node.cooldownUntil = Math.max(node.cooldownUntil, nowMs() + cooldownMs);
  node.cooldownReason = reason;
  debugLog(
    `节点${node.index + 1}进入冷却，${Math.ceil(cooldownMs / 1000)}s，原因：${reason}`,
  );
};

const recordNodeSuccess = (node, latencyMs) => {
  node.successCount += 1;
  node.consecutiveFailures = 0;
  node.cooldownUntil = 0;
  node.cooldownReason = "";
  node.averageLatencyMs = node.averageLatencyMs
    ? Math.round(node.averageLatencyMs * 0.8 + latencyMs * 0.2)
    : latencyMs;
};

const recordNodeFailure = (node, err) => {
  // 任务的排队/总预算耗尽，不是 Key 故障证据。
  if (err instanceof DeadlineError) return;
  node.failCount += 1;

  let shouldPenalize = false;
  let cooldownMs = null;
  let reason = err?.message ?? "未知错误";

  if (err?.code === "ETIMEDOUT" || err?.name === "TimeoutError") {
    node.timeoutCount += 1;
    shouldPenalize = true;
  } else if (err instanceof ApiError) {
    shouldPenalize = err.penalizeNode;
    cooldownMs = err.cooldownMs;
  } else {
    // request() 抛出的网络层错误默认视为可重试的瞬时节点故障。
    shouldPenalize = true;
  }

  if (!shouldPenalize) return;
  node.consecutiveFailures += 1;
  setNodeCooldown(
    node,
    cooldownMs ?? getAdaptiveCooldownMs(node.consecutiveFailures),
    reason,
  );
};

const isRetryableError = (err) => {
  if (err instanceof QueueFullError || err instanceof DeadlineError) return false;
  if (err instanceof ApiError) return err.retryable;
  return true;
};

const getPoolRecoverySeconds = () => {
  if (!keyPool.length) return null;
  const ts = nowMs();
  if (!keyPool.every((node) => node.cooldownUntil > ts)) return null;
  const nextRecoveryAt = Math.min(...keyPool.map((node) => node.cooldownUntil));
  return Math.max(1, Math.ceil((nextRecoveryAt - ts) / 1000));
};

const getRateLimitCooldownMs = (headers) => {
  const value = headers?.["retry-after"] ?? headers?.["Retry-After"];
  if (value === undefined || value === null || String(value).trim() === "") {
    return RATE_LIMIT_COOLDOWN_MS;
  }
  const seconds = Number(value);
  if (Number.isFinite(seconds)) return seconds >= 0 ? seconds * 1000 : RATE_LIMIT_COOLDOWN_MS;
  const retryAt = Date.parse(String(value));
  return Number.isFinite(retryAt) ? Math.max(0, retryAt - nowMs()) : RATE_LIMIT_COOLDOWN_MS;
};

const requestMusicUrlByKey = async (source, songId, quality, key, deadlineAt) => {
  const requestUrl =
    `${API_URL}/url` +
    `?source=${encodeURIComponent(source)}` +
    `&songId=${encodeURIComponent(String(songId))}` +
    `&quality=${encodeURIComponent(quality)}`;

  const requestRes = await httpFetch(
    requestUrl,
    { method: "GET", headers: createHeaders(key), follow_max: 5 },
    REQUEST_TIMEOUT_MS,
    deadlineAt,
  );

  const body = parseResponseBody(requestRes?.body);
  const httpStatus = Number(requestRes?.statusCode ?? requestRes?.status);
  const rawCode = body?.code;
  const businessCode =
    (typeof rawCode === "number" || typeof rawCode === "string") && String(rawCode).trim() !== ""
      ? Number(rawCode)
      : Number.NaN;
  const code = Number.isFinite(httpStatus) && httpStatus !== 200 ? httpStatus : businessCode;
  if (!Number.isFinite(code)) {
    throw new ApiError("响应格式异常", { retryable: true });
  }

  switch (code) {
    case 0:
    case 200:
      if (typeof body?.url !== "string" || !/^https?:\/\/[^/\s?#]+(?:[/?#][^\s]*)?$/i.test(body.url)) {
        throw new ApiError("响应缺少有效播放地址", { code, retryable: true });
      }
      return body.url;
    case 401:
    case 403:
      throw new ApiError("权限不足或Key失效", {
        code,
        retryable: true,
        penalizeNode: true,
        cooldownMs: INVALID_KEY_COOLDOWN_MS,
      });
    case 429:
      throw new ApiError("请求过速，请稍后再试", {
        code,
        retryable: true,
        penalizeNode: true,
        cooldownMs: getRateLimitCooldownMs(requestRes?.headers),
      });
    default:
      // 解析某首歌失败不代表 Key 不可用，不能封锁无关歌曲和平台。
      throw new ApiError(body?.message ?? `服务请求失败(${code})`, {
        code,
        retryable: code >= 500 && code <= 599,
      });
  }
};

const resolveMusicUrl = async (source, songId, quality) => {
  if (!keyPool.length) throw new Error("未配置可用Key");

  const deadlineAt = nowMs() + TOTAL_REQUEST_TIMEOUT_MS;
  const attemptedNodes = new Set();
  const errors = [];
  let transientFailures = 0;

  while (attemptedNodes.size < keyPool.length) {
    if (deadlineAt <= nowMs()) throw new DeadlineError();

    const node = await acquireNode(attemptedNodes, deadlineAt);
    if (!node) break;
    attemptedNodes.add(node);

    const startedAt = nowMs();
    try {
      const url = await requestMusicUrlByKey(
        source,
        songId,
        quality,
        node.key,
        deadlineAt,
      );
      recordNodeSuccess(node, nowMs() - startedAt);
      return url;
    } catch (err) {
      recordNodeFailure(node, err);
      errors.push(err?.message ?? "未知错误");
      if (!isRetryableError(err)) throw err;
      if (!(err instanceof ApiError && err.penalizeNode)) {
        transientFailures += 1;
        if (transientFailures >= MAX_TRANSIENT_ATTEMPTS) throw err;
      }
    } finally {
      releaseNode(node);
    }
  }

  const recoverSeconds = getPoolRecoverySeconds();
  const uniqueErrors = [...new Set(errors)];
  const errorSummary = uniqueErrors.join(" | ");

  if (recoverSeconds !== null) {
    throw new Error(
      `当前所有节点暂不可用，约${recoverSeconds}秒后恢复${
        errorSummary ? `：${errorSummary}` : ""
      }`,
    );
  }
  if (errorSummary) throw new Error(`所有节点请求失败：${errorSummary}`);
  throw new Error("暂无可用节点");
};

const handleGetMusicUrl = async (source, musicInfo, quality) => {
  const supportedQualities = MUSIC_QUALITY[source];
  if (!supportedQualities) throw new Error(`不支持的音源：${source ?? "未知"}`);
  if (!supportedQualities.includes(quality)) {
    throw new Error(`音源${source}不支持音质：${quality ?? "未知"}`);
  }

  const songId = [musicInfo?.hash, musicInfo?.songmid, musicInfo?.id].find(
    (value) => value !== undefined && value !== null && value !== "",
  );
  if (songId === undefined) {
    throw new Error("缺少歌曲ID");
  }

  const cacheKey = getRequestCacheKey(source, songId, quality);
  const cachedUrl = getCachedMusicUrl(cacheKey);
  if (cachedUrl) return cachedUrl;

  const pendingRequest = pendingRequests.get(cacheKey);
  if (pendingRequest) return pendingRequest;

  const task = resolveMusicUrl(source, songId, quality)
    .then((url) => {
      setCachedMusicUrl(cacheKey, url);
      return url;
    })
    .finally(() => {
      pendingRequests.delete(cacheKey);
    });

  pendingRequests.set(cacheKey, task);
  return task;
};

const checkUpdate = async () => {
  try {
    const key = API_KEYS[0] || "";
    const requestUrl =
      `${API_BASE_URL}/script` +
      `?checkUpdate=${encodeURIComponent(SCRIPT_MD5)}` +
      `&key=${encodeURIComponent(key)}`;
    const requestRes = await httpFetch(requestUrl, {
      method: "GET",
      headers: createHeaders(),
    });

    const body = parseResponseBody(requestRes?.body);
    if (body?.data) {
      send(EVENT_NAMES.updateAlert, {
        log: body.data.updateMsg,
        updateUrl: body.data.updateUrl,
      });
    }
  } catch (err) {
    debugLog("检查更新失败", err?.message ?? err);
  }
};

const musicSources = {};
MUSIC_SOURCE.forEach((item) => {
  musicSources[item] = {
    name: item,
    type: "music",
    actions: ["musicUrl"],
    qualitys: MUSIC_QUALITY[item],
  };
});

on(EVENT_NAMES.request, ({ action, source, info }) => {
  switch (action) {
    case "musicUrl":
      return handleGetMusicUrl(source, info?.musicInfo, info?.type);
    default:
      return Promise.reject(new Error("action not support"));
  }
});

send(EVENT_NAMES.inited, {
  status: true,
  openDevTools: DEV_ENABLE,
  sources: musicSources,
});
