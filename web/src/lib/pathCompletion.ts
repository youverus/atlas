import type { DirListing } from "../api";

const PATH_MENTION = /(?:^|\s)(@(?:"((?:\\[^\r\n]|[^"\\\r\n])*\\?)"?|([^\s",，。:：；;、!?！？]*)))/g;
const QUOTED_PATH = /[\s"\\,，。:：；;、!?！？]/;
const PATH_ESCAPE = /\\(?:["\\/bfnrt]|u[0-9a-fA-F]{4})/g;
const MAX_PATH_CANDIDATES = 40;

/** Identifies the session workspace used by the current file API route. */
export interface PathCompletionScope {
  session: string;
  deviceId: string;
  directory?: string;
}

/** Find a workspace path token at the caret and split its prefix into a directory and name. */
export function pathMention(draft: string, caret: number) {
  for (const match of draft.matchAll(PATH_MENTION)) {
    const token = match[1];
    const end = match.index + match[0].length;
    const start = end - token.length;
    if (caret <= start) break;
    if (caret > end) continue;

    const quoted = match[2] !== undefined;
    const content = quoted ? match[2] : match[3];
    const typed = content.slice(0, Math.max(0, caret - start - (quoted ? 2 : 1)));
    const prefix = quoted ? typed.replace(PATH_ESCAPE, (escape) => JSON.parse(`"${escape}"`) as string) : typed;
    const separator = prefix.lastIndexOf("/");
    const directory = prefix.slice(0, separator + 1);
    if (directory.startsWith("/") || directory.split("/").includes("..")) return null;
    return { start, end, directory, prefix: prefix.slice(separator + 1) };
  }
  return null;
}

/** Return server-ordered entries matching the name prefix in a single directory. */
export function matchingPaths(listing: DirListing | undefined, prefix: string) {
  const needle = prefix.toLowerCase();
  return listing?.entries
    .filter((entry) => entry.name.toLowerCase().startsWith(needle))
    .slice(0, MAX_PATH_CANDIDATES) ?? [];
}

/** Spell a relative path as a prompt mention and keep directory prefixes editable. */
export function pathInsertion(path: string, isDir: boolean, forceQuote = false) {
  const relative = isDir ? `${path}/` : path;
  const quoted = forceQuote || QUOTED_PATH.test(relative);
  const text = quoted ? `@${JSON.stringify(relative)}` : `@${relative}`;
  return { text, caretOffset: text.length - (quoted && isDir ? 1 : 0) };
}
