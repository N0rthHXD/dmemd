import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc';
import type { MemoryPrecision, TemporalDetail } from '../api/types';

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

function rawISOWithoutZone(value: string): string {
  return value.replace('T', ' ').replace(/(?:Z|[+-]\d\d:\d\d)$/, '').slice(0, 19);
}

function precisionFormat(precision: MemoryPrecision | undefined): string | null {
  switch (precision) {
    case 'year':
      return 'YYYY';
    case 'month':
      return 'YYYY-MM';
    case 'day':
      return 'YYYY-MM-DD';
    case 'hour':
      return 'YYYY-MM-DD HH时';
    case 'minute':
      return 'YYYY-MM-DD HH:mm';
    case 'second':
      return LOCAL_TIME_FORMAT;
    case 'unknown':
    default:
      return null;
  }
}

/**
 * Prefer the preserved source wording, then apply only the known precision.
 * A calendar value without a known timezone is never shifted by the browser.
 */
export function formatTemporal(
  value: string | null | undefined,
  detail: TemporalDetail | undefined,
  emptyText = '未知',
): string {
  if (!value || isZeroTimestamp(value)) {
    return detail?.text?.trim() || emptyText;
  }
  if (detail?.text?.trim()) {
    return detail.text.trim();
  }
  if (!detail || detail.precision === 'unknown') {
    return emptyText;
  }
  const format = precisionFormat(detail.precision);
  if (!format) {
    return emptyText;
  }
  if (!detail.timezone_known) {
    const raw = rawISOWithoutZone(value);
    if (detail.precision === 'year') {
      return raw.slice(0, 4);
    }
    if (detail.precision === 'month') {
      return raw.slice(0, 7);
    }
    if (detail.precision === 'day') {
      return raw.slice(0, 10);
    }
    if (detail.precision === 'hour') {
      return `${raw.slice(0, 10)} ${raw.slice(11, 13)}时`;
    }
    if (detail.precision === 'minute') {
      return raw.slice(0, 16);
    }
    return raw;
  }
  const parsed = dayjs(value);
  return parsed.isValid() ? parsed.format(format) : value;
}

export function formatValidInterval(
  from: string | null | undefined,
  to: string | null | undefined,
  fromDetail: TemporalDetail | undefined,
  toDetail: TemporalDetail | undefined,
): string {
  return `${formatTemporal(from, fromDetail)} 至 ${formatTemporal(to, toDetail)}`;
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
