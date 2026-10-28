import { useMemo } from 'react';

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** Plain-text highlight only; never interprets the matched text as HTML. */
export function MatchHighlight({ text, terms }: { text: string; terms: string[] }) {
  const parts = useMemo(() => {
    const valid = [...new Set(terms.map((term) => term.trim()).filter(Boolean))].sort(
      (left, right) => right.length - left.length,
    );
    if (valid.length === 0) {
      return null;
    }
    const expression = new RegExp(`(${valid.map(escapeRegExp).join('|')})`, 'gi');
    return { expression, terms: new Set(valid.map((term) => term.toLocaleLowerCase())) };
  }, [terms]);

  if (!parts) {
    return <>{text}</>;
  }
  return (
    <>
      {text.split(parts.expression).map((part, index) =>
        parts.terms.has(part.toLocaleLowerCase()) ? (
          <mark key={`${part}-${index}`} style={{ padding: 0, borderRadius: 2 }}>
            {part}
          </mark>
        ) : (
          <span key={`${part}-${index}`}>{part}</span>
        ),
      )}
    </>
  );
}
