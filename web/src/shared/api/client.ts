import createClient from 'openapi-fetch';
import type { paths } from './schema.gen';
import { ApiError, httpStatusMessage, isApiError } from './errors';

const DEFAULT_TIMEOUT_MS = 30_000;
const LONG_TIMEOUT_MS = 90_000;

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function busyFetch(timeoutMs: number): typeof fetch {
  return async (input, init) => {
    const timeoutController = new AbortController();
    const timer = globalThis.setTimeout(() => {
      timeoutController.abort(new DOMException('request timed out', 'TimeoutError'));
    }, timeoutMs);
    const externalSignal = init?.signal;
    const signal = externalSignal
      ? AbortSignal.any([externalSignal, timeoutController.signal])
      : timeoutController.signal;
    try {
      return await fetch(input, {
        ...init,
        cache: 'no-store',
        credentials: 'same-origin',
        signal,
      });
    } catch (cause) {
      if (externalSignal?.aborted) {
        throw new ApiError({ kind: 'cancelled', message: '请求已取消。', cause });
      }
      if (timeoutController.signal.aborted) {
        throw new ApiError({
          kind: 'timeout',
          message: `请求超过 ${Math.round(timeoutMs / 1000)} 秒未完成。`,
          cause,
        });
      }
      if (cause instanceof ApiError) {
        throw cause;
      }
      throw new ApiError({ kind: 'network', message: '无法连接本地服务。', cause });
    } finally {
      globalThis.clearTimeout(timer);
    }
  };
}

const baseUrl = globalThis.location?.origin ?? 'http://127.0.0.1:18432';

/** Same-origin, no-store API client. Components must not call fetch directly. */
export const api = createClient<paths>({
  baseUrl,
  fetch: busyFetch(DEFAULT_TIMEOUT_MS),
});

/** Longer timeout for the explicit Embedding probe only. */
export const apiLong = createClient<paths>({
  baseUrl,
  fetch: busyFetch(LONG_TIMEOUT_MS),
});

interface ApiResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

function errorBody(error: unknown): { message?: string; code?: string } {
  if (!isRecord(error)) {
    return {};
  }
  return {
    message: typeof error.error === 'string' ? error.error : undefined,
    code: typeof error.code === 'string' ? error.code : undefined,
  };
}

export function httpError(response: Response, error: unknown): ApiError {
  const body = errorBody(error);
  return new ApiError({
    kind: 'http',
    status: response.status,
    code: body.code,
    message: body.message ?? httpStatusMessage(response.status),
  });
}

export function protocolError(label: string, detail = ''): ApiError {
  return new ApiError({
    kind: 'protocol',
    message: `${label}${detail ? `：${detail}` : ''}`,
  });
}

function isJSONResponse(response: Response): boolean {
  return (response.headers.get('content-type') ?? '').toLowerCase().includes('application/json');
}

/**
 * Converts an openapi-fetch result into a checked payload. HTTP errors, missing
 * JSON content types and missing bodies become explicit ApiError values rather
 * than being mistaken for empty data.
 */
export async function unwrap<T>(request: Promise<ApiResult<T>>, label: string): Promise<T> {
  let result: ApiResult<T>;
  try {
    result = await request;
  } catch (cause) {
    if (isApiError(cause)) {
      throw cause;
    }
    if (cause instanceof SyntaxError) {
      throw protocolError(label, '响应 JSON 解码失败');
    }
    throw new ApiError({ kind: 'network', message: '无法连接本地服务。', cause });
  }
  const { data, error, response } = result;
  if (!response.ok) {
    throw httpError(response, error);
  }
  if (!isJSONResponse(response)) {
    throw protocolError(label, '响应 Content-Type 不是 application/json');
  }
  if (data === undefined) {
    throw protocolError(label, '响应缺少 JSON 主体');
  }
  return data;
}

/** Raw same-origin JSON request for /readyz, whose 503 body is still a status. */
export async function rawJSON(
  path: string,
  timeoutMs = DEFAULT_TIMEOUT_MS,
  signal?: AbortSignal,
): Promise<{ status: number; body: unknown }> {
  const response = await busyFetch(timeoutMs)(path, { method: 'GET', signal });
  if (!isJSONResponse(response)) {
    throw protocolError('健康检查', '响应 Content-Type 不是 application/json');
  }
  let body: unknown;
  try {
    body = await response.json();
  } catch (cause) {
    throw new ApiError({ kind: 'protocol', message: '健康检查响应不是有效 JSON。', cause });
  }
  return { status: response.status, body };
}
