import { Collapse } from 'antd';
import type { ReactNode } from 'react';

/** Keep troubleshooting information available without making it the main content. */
export function TechnicalDetails({ children, label = '技术详情' }: { children: ReactNode; label?: string }) {
  return <Collapse className="technical-details" items={[{ key: 'details', label, children }]} />;
}
