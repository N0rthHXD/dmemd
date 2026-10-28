import { codeDefinition } from './codes';

export function shortId(value: string): string {
  return value.length > 14 ? `${value.slice(0, 6)}…${value.slice(-4)}` : value;
}

export function artifactName(item: { kind: string; scope_key?: string; topic?: string }): string {
  return [item.topic || item.scope_key, codeDefinition('artifactKind', item.kind).label].filter(Boolean).join(' · ');
}
