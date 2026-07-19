import { memo, type ComponentProps } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";
import { common } from "lowlight";

// Pin the grammar set explicitly (lowlight's `common` ≈37 languages, covering
// bash/diff/json/yaml/typescript/go/python/rust/sql/xml/markdown) so bundle
// size doesn't grow if rehype-highlight's default changes.
const rehypePlugins: ComponentProps<typeof ReactMarkdown>["rehypePlugins"] = [
  [rehypeHighlight, { languages: common }],
];

/**
 * Memoized so streaming updates elsewhere in the feed don't re-run the
 * highlight.js pass on every chunk. Token colors live in index.css
 * (`.hljs-*`) and follow the light/dark theme, replacing the dark-only
 * github-dark stylesheet.
 */
export const MarkdownViewer = memo(function MarkdownViewer({ content }: { content: string }) {
  if (!content) return null;

  return (
    <div className="prose prose-sm dark:prose-invert max-w-none break-words prose-pre:bg-muted prose-pre:text-foreground prose-code:text-foreground">
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={rehypePlugins}>
        {content}
      </ReactMarkdown>
    </div>
  );
});
