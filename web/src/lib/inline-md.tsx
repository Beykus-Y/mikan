import type { ReactNode } from "react";

// The inline markdown release notes use: **bold**, `code` and [text](https://…). Built as
// React nodes, never as HTML, so a note cannot put markup on the page; a link that is not
// https stays text.
const TOKEN = /\*\*([^*]+)\*\*|`([^`]+)`|\[([^\]]+)\]\((https:\/\/[^\s)]+)\)/g;

export function inlineMarkdown(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(TOKEN)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    const key = out.length;
    if (m[1] !== undefined) out.push(<strong key={key}>{m[1]}</strong>);
    else if (m[2] !== undefined) out.push(<code key={key} className="mono">{m[2]}</code>);
    else
      out.push(
        <a key={key} href={m[4]} target="_blank" rel="noreferrer noopener" className="underline">
          {m[3]}
        </a>,
      );
    last = at + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
