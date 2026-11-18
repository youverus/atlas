// The slash commands this window can run.
//
// The table holds what the page can do and nothing else. The terminal's own table
// has twenty-five, and most of them drive things a window does not have — a
// terminal picker, a release download, /exit, the goal and the recovery flows —
// so listing them would make the palette the one place in the interface that is
// wrong about what can run. The session commands (/new, /archive, /restore) are
// absent for a second reason: the window already draws each of them as a button
// in the rail, where the conversation they act on is the row being clicked.
// Typing "/archive" into a box to archive the thing the box is attached to is a
// worse gesture than clicking the row.
//
// /parent is absent on purpose rather than for lack of a route. The side
// conversation is a panel beside the main one, so both prompts are on screen at
// once and there is no "typing into the side one" for /parent to undo; a reader
// who types it gets the table, which is the honest answer. What /side off does
// here is still what it does in the terminal: throw the side conversation away.
//
// It lives in the page rather than behind an endpoint because the actions are
// here: the list and the thing each name does are written against the same API
// client, so a name cannot survive the removal of its handler without the
// build failing on the switch in App.tsx.

export interface Command {
  /** The name as typed, leading slash included. */
  name: string;
  /** What follows it, or empty when the command takes no argument. */
  usage: string;
  summary: string;
}

export const COMMANDS: Command[] = [
  { name: "/help", usage: "", summary: "把这张表写进对话" },
  { name: "/compact", usage: "[说明]", summary: "把旧的历史压成摘要，可以加一句要求" },
  { name: "/side", usage: "[off]", summary: "在右侧开一个临时对话，这一段不动" },
  { name: "/btw", usage: "[问题]", summary: "在侧对话里问一句，面板没开就先开" },
  { name: "/model", usage: "", summary: "打开设置，换模型或推理强度" },
  { name: "/permission", usage: "", summary: "打开设置，换工具调用的审批档位" },
  { name: "/fork", usage: "", summary: "复制这段对话开一条新分支，可以给它起名" },
];

/**
 * matches is the palette's list, or null when the palette should stay shut.
 *
 * It opens on a draft that is still one command name — "/" or "/mo" — and shuts
 * as soon as an argument starts, because a list of commands over a line that is
 * already about a model name would be covering the thing being typed.
 */
export function matches(draft: string): Command[] | null {
  const trimmed = draft.replace(/^\s+/, "");
  if (!trimmed.startsWith("/")) return null;
  if (/\s/.test(trimmed)) return null;
  return COMMANDS.filter((command) => command.name.startsWith(trimmed));
}

/** parse splits a sent line into its command and the rest, or null when the
 *  line is not a command at all. Only the first field decides: "/model a b"
 *  is the /model command with the argument "a b". */
export function parse(text: string): { name: string; argument: string } | null {
  const trimmed = text.trim();
  if (!trimmed.startsWith("/")) return null;
  const space = trimmed.indexOf(" ");
  if (space < 0) return { name: trimmed, argument: "" };
  return { name: trimmed.slice(0, space), argument: trimmed.slice(space + 1).trim() };
}

/** find is the table lookup a sent line is resolved through. */
export function find(name: string): Command | undefined {
  return COMMANDS.find((command) => command.name === name);
}

/**
 * leadingCommand is the part of a draft the composer highlights, or null.
 *
 * Only an exact name counts: "/mo" is a prefix the palette is still narrowing,
 * and painting it as a command would say it is one before it is. The rest of the
 * line is returned so the caller can draw it unchanged — a command with an
 * argument is still a command, and the highlight has to stop where the name
 * does.
 *
 * The whitespace the name was found after is returned too. The caller draws a
 * copy of the whole draft to place the highlight, and a copy missing a leading
 * space puts the box one character to the left of the name it is underlining.
 */
export function leadingCommand(draft: string): { lead: string; name: string; rest: string } | null {
  const start = draft.length - draft.replace(/^\s+/, "").length;
  const token = /^\/\S*/.exec(draft.slice(start))?.[0] ?? "";
  if (!find(token)) return null;
  return { lead: draft.slice(0, start), name: token, rest: draft.slice(start + token.length) };
}

/**
 * helpText is what /help writes into the conversation: the same table the
 * palette draws, as lines that stay readable once they are scrolled past. The
 * name column is measured rather than fixed, so a longer command added later
 * cannot push its own summary out of line.
 */
export function helpText(): string {
  const label = (command: Command) => (command.usage ? `${command.name} ${command.usage}` : command.name);
  const width = COMMANDS.reduce((widest, command) => Math.max(widest, label(command).length), 0);
  return COMMANDS.map((command) => `${label(command).padEnd(width)}   ${command.summary}`).join("\n");
}
