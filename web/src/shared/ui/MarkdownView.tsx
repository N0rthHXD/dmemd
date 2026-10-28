import ReactMarkdown from 'react-markdown';

/** Read-only Markdown. Raw HTML and images are never rendered. */
export function MarkdownView({ content }: { content: string }) {
  if (!content.trim()) {
    return <span style={{ color: 'var(--ant-color-text-secondary)' }}>无正文</span>;
  }
  return (
    <div className="markdown-body" style={{ lineHeight: 1.75, overflowWrap: 'anywhere' }}>
      <ReactMarkdown skipHtml disallowedElements={['img']}>
        {content}
      </ReactMarkdown>
    </div>
  );
}
