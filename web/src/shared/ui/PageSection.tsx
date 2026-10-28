import { Card } from 'antd';
import type { ReactNode } from 'react';

export function PageSection({
  title,
  extra,
  children,
  description,
}: {
  title: ReactNode;
  extra?: ReactNode;
  children: ReactNode;
  description?: ReactNode;
}) {
  return (
    <Card
      title={title}
      extra={extra}
      style={{ marginBottom: 16 }}
      styles={{ body: { paddingTop: description ? 12 : undefined } }}
    >
      {description ? (
        <div style={{ marginBottom: 12, opacity: 0.75 }}>{description}</div>
      ) : null}
      {children}
    </Card>
  );
}
