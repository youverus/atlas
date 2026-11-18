// How fast the answer is arriving, measured while it arrives.
//
// The terminal prints one rate per turn, from the usage the provider reports
// when the message ends: that is the real number, and it is the one the receipt
// keeps. A window can say something the terminal cannot — how fast the words are
// landing right now — and that number can only be estimated, because no provider
// reports tokens in the middle of a message. So this counts characters: CJK at
// one token each and the rest at a quarter, which is roughly the ratio English
// text tokenizes at and keeps a Chinese answer from reading three times slower
// than it is.
//
// It is a speedometer, not an odometer. The window is the last few seconds
// rather than the whole turn, so a stalled provider shows up as a falling number
// instead of an average that never moves; when the words stop arriving for long
// enough, the hint disappears rather than sitting there claiming a rate that is
// no longer being measured.

import { useEffect, useRef, useState } from "react";
import type { Block } from "./fold";

export interface StreamRate {
  /** Estimated tokens written so far in this turn. */
  tokens: number;
  /** Estimated tokens per second over the recent window. */
  rate: number;
}

/** How much history the speedometer averages. */
const WINDOW_MS = 3000;
/** No new text for this long and the hint goes away. */
const IDLE_MS = 2000;
/** Repaint interval. Faster than a reader can read, slower than a token. */
const TICK_MS = 300;
/** Below this, a rate is mostly the measurement's own noise. */
const WARMUP_MS = 500;

/**
 * estimateTokens approximates a token count from the text on screen.
 *
 * CJK is counted one token per character: the usual ratios run from 0.6 to 1.2
 * characters per token depending on the tokenizer, and a quarter would be off
 * by more than the guess is worth. Everything else — Latin, punctuation, code —
 * is counted at four characters per token.
 */
export function estimateTokens(text: string): number {
  let wide = 0;
  let all = 0;
  for (const char of text) {
    all += 1;
    const code = char.codePointAt(0) ?? 0;
    if (
      (code >= 0x3040 && code <= 0x30ff) || // kana
      (code >= 0x3400 && code <= 0x4dbf) || // CJK extension A
      (code >= 0x4e00 && code <= 0x9fff) || // CJK unified ideographs
      (code >= 0xac00 && code <= 0xd7af) || // hangul syllables
      (code >= 0xf900 && code <= 0xfaff) // CJK compatibility ideographs
    ) {
      wide += 1;
    }
  }
  return wide + (all - wide) / 4;
}

/** turnText is what the model has written this turn: answers and thinking.
 *  Tool arguments are excluded — they are typed by the model too, but a card
 *  that is still collapsing should not make the rate jump. */
function turnText(blocks: Block[]): string {
  let start = 0;
  for (let index = blocks.length - 1; index >= 0; index -= 1) {
    if (blocks[index].role === "user") {
      start = index;
      break;
    }
  }
  let text = "";
  for (let index = start; index < blocks.length; index += 1) {
    const block = blocks[index];
    if (block.role === "answer" || block.role === "thinking") text += block.text;
  }
  return text;
}

export function useStreamRate(blocks: Block[], running: boolean): StreamRate | null {
  // The tick reads the newest text through a ref: an interval created once per
  // turn must not be torn down and rebuilt on every delta.
  const text = useRef("");
  text.current = turnText(blocks);

  const samples = useRef<Array<{ at: number; tokens: number }>>([]);
  const [hint, setHint] = useState<StreamRate | null>(null);

  useEffect(() => {
    if (!running) {
      samples.current = [];
      setHint(null);
      return;
    }
    const tick = () => {
      const now = Date.now();
      const tokens = estimateTokens(text.current);
      const last = samples.current[samples.current.length - 1];
      if (!last || tokens > last.tokens) {
        samples.current.push({ at: now, tokens });
      }
      // Keep one sample older than the window, so the measured span covers the
      // whole window instead of starting at the first sample inside it.
      while (samples.current.length > 2 && now - samples.current[0].at > WINDOW_MS) {
        samples.current.shift();
      }
      const first = samples.current[0];
      const newest = samples.current[samples.current.length - 1];
      const span = (newest.at - first.at) / 1000;
      if (now - newest.at > IDLE_MS || newest.at - first.at < WARMUP_MS || span <= 0) {
        setHint(null);
        return;
      }
      const grown = newest.tokens - first.tokens;
      if (grown <= 0) {
        setHint(null);
        return;
      }
      setHint({ tokens: Math.round(newest.tokens), rate: grown / span });
    };
    tick();
    const id = window.setInterval(tick, TICK_MS);
    return () => window.clearInterval(id);
  }, [running]);

  return hint;
}
