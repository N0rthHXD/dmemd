export type ApiErrorKind =
  | 'network'
  | 'timeout'
  | 'http'
  | 'protocol'
  | 'cancelled';

interface ApiErrorInit {
  kind: ApiErrorKind;
  message: string;
  status?: number;
  code?: string;
  cause?: unknown;
}

/** One normalized failure for network, timeout, HTTP, protocol and cancellation. */
export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  readonly status?: number;
  readonly code?: string;
  readonly cause?: unknown;

  constructor(init: ApiErrorInit) {
    super(init.message);
    this.name = 'ApiError';
    this.kind = init.kind;
    this.status = init.status;
    this.code = init.code;
    this.cause = init.cause;
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError;
}

const knownCodes: Record<string, string> = {
  invalid_resolution: '暂时无法按此方式处理，请检查内容选择、适用情况或时间段。',
  conflict_stale: '相关内容已更新，请重新查看后再确认。',
  restore_conflict: '恢复的内容可能与现有记忆不一致，本次未恢复，请先核对相关内容。',
};

export function knownCodeMessage(code: string | undefined): string | undefined {
  return code ? knownCodes[code] : undefined;
}

const statusMessages: Record<number, string> = {
  400: '请求无效，请保留当前输入并检查表单。',
  404: '资源不存在，可能已被永久清除。',
  409: '版本或状态已变化，请重新读取后再操作。',
  500: '服务内部执行失败。',
  503: '服务暂不可用或数据库尚未就绪。',
};

export function errorMessage(error: unknown): string {
  if (isApiError(error)) {
    if (error.kind === 'network') {
      return '无法连接本地服务，请确认 dmemd 正在运行后重试。';
    }
    if (error.kind === 'timeout') {
      return '请求超时。写操作的结果可能未知，请勿直接重复提交。';
    }
    if (error.kind === 'protocol') {
      return '服务返回的内容暂时无法读取，请刷新后重试；如持续出现，请查看服务状态。';
    }
    if (error.kind === 'cancelled') {
      return '请求已取消。';
    }
    return knownCodeMessage(error.code) ?? statusMessages[error.status ?? 0] ?? '操作未完成，请稍后重试。';
  }
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return '发生未知错误。';
}

export function errorTitle(error: unknown): string {
  if (!isApiError(error)) {
    return '操作失败';
  }
  switch (error.kind) {
    case 'network':
      return '本地服务连接失败';
    case 'timeout':
      return '请求超时';
    case 'protocol':
      return '暂时无法读取服务响应';
    case 'cancelled':
      return '请求已取消';
    case 'http':
      return '请求未完成';
  }
}

export function isRetryableError(error: unknown): boolean {
  if (!isApiError(error)) {
    return false;
  }
  if (error.kind === 'network' || error.kind === 'timeout') {
    return true;
  }
  return error.kind === 'http' && (error.status === 500 || error.status === 503);
}

export function httpStatusMessage(status: number): string {
  return statusMessages[status] ?? `请求失败（HTTP ${status}）。`;
}
