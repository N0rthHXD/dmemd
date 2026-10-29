import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc';

dayjs.extend(utc);

export const LOCAL_TIME_FORMAT = 'YYYY-MM-DD HH:mm:ss';
export const UTC_TIME_FORMAT = 'YYYY-MM-DD HH:mm:ss [UTC]';

function isZeroTimestamp(value: string): boolean {
  return value.startsWith('0001-01-01');
}

export function formatTimestamp(value: string | null | undefined): string {
  if (!value || isZeroTimestamp(value)) {
    return '未知';
  }
  const parsed = dayjs(value);
  return parsed.isValid() ? parsed.format(LOCAL_TIME_FORMAT) : value;
}

export function formatUTCTimestamp(value: string | null | undefined): string {
  if (!value || isZeroTimestamp(value)) {
    return '未知';
  }
  const parsed = dayjs(value);
  return parsed.isValid() ? parsed.utc().format(UTC_TIME_FORMAT) : value;
}

export function timestampTitle(value: string | null | undefined): string | undefined {
  if (!value) {
    return undefined;
  }
  return `UTC 原值：${value}`;
}

export function formatTimestampWithUTC(value: string | null | undefined): string {
  if (!value) {
    return '未知';
  }
  return `${formatTimestamp(value)}（${formatUTCTimestamp(value)}）`;
}

export function toRFC3339(value: dayjs.Dayjs | null | undefined): string | undefined {
  if (!value || !value.isValid()) {
    return undefined;
  }
  return value.utc().toISOString();
}

export function fromRFC3339(value: string | null | undefined): dayjs.Dayjs | null {
  if (!value) {
    return null;
  }
  const parsed = dayjs(value);
  return parsed.isValid() ? parsed : null;
}

export function formatSeconds(value: number | undefined | null): string {
  if (value === undefined || value === null || !Number.isFinite(value)) {
    return '未知';
  }
  if (value < 60) {
    return `${Math.max(0, Math.round(value))} 秒`;
  }
  if (value < 3600) {
    return `${Math.floor(value / 60)} 分 ${Math.round(value % 60)} 秒`;
  }
  return `${Math.floor(value / 3600)} 时 ${Math.floor((value % 3600) / 60)} 分`;
}

export function formatMilliseconds(value: number | undefined | null): string {
  if (value === undefined || value === null || !Number.isFinite(value)) {
    return '未知';
  }
  if (value < 1000) {
    return `${value} ms`;
  }
  return `${(value / 1000).toFixed(2)} s`;
}

export function formatPercent(value: number | undefined | null): string {
  if (value === undefined || value === null || !Number.isFinite(value)) {
    return '未知';
  }
  return `${(value * 100).toFixed(1)}%`;
}

export function formatDateValue(value: string | null | undefined): string {
  if (!value || isZeroTimestamp(value)) {
    return '未知';
  }
  return dayjs(value).isValid() ? dayjs(value).format('YYYY-MM-DD') : value;
}
