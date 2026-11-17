import { useState } from "react";
import { api, type PendingQuestion } from "../api";

// The one control that blocks a turn: the tool goroutine is parked until one of
// these three buttons posts an answer. "always" is a separate button rather
// than a checkbox because the decision is the approval, not a modifier on it.
export function ApprovalCard({
  question,
  onSettled,
}: {
  question: PendingQuestion;
  onSettled: (id: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const answer = async (allowed: boolean, always: boolean) => {
    setBusy(true);
    setError("");
    try {
      await api.approve(question.id, allowed, always);
      onSettled(question.id);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      setBusy(false);
    }
  };

  return (
    <div className="approval">
      <div className="approval__body">
        <span className="approval__tool">{question.tool}</span> {question.subject}
        {error ? <div className="notice notice--error">{error}</div> : null}
      </div>
      <div className="approval__actions">
        <button
          type="button"
          className="button button--allow"
          disabled={busy}
          onClick={() => answer(true, false)}
        >
          允许
        </button>
        <button type="button" className="button" disabled={busy} onClick={() => answer(true, true)}>
          总是允许
        </button>
        <button
          type="button"
          className="button button--deny"
          disabled={busy}
          onClick={() => answer(false, false)}
        >
          拒绝
        </button>
      </div>
    </div>
  );
}
