// The desktop pet: a tiny page for the desktop shell's frameless companion
// window. It reads the same loopback surface as the main window — one state
// snapshot plus the shared SSE stream — and answers a single question, "is any
// session working?", by animating the whale-girl between a desk scene and a
// TV scene. Dragging and double-click ride the shell's native channels
// (wails:drag via the webkit message handler, focus via /api/shell/focus), so
// in a plain browser the page still renders and just loses those gestures.
import { StrictMode } from "react";
import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { api } from "./api";
import whaleGirl from "./assets/pet/whale-girl.png";
import "./pet.css";

/** One `status`/`sidestatus` SSE frame, mirroring httpapi.statusEvent. */
interface StatusFrame {
  session?: string;
  running: boolean;
}

// useRunningState keeps the set of busy sessions live. Main and side turns are
// tracked separately (side keys get a prefix) so a side conversation keeps the
// pet working after the main turn of the same session has ended.
function useRunningState(): { working: boolean; count: number } {
  const [sessions, setSessions] = useState<ReadonlySet<string>>(new Set());
  const [solo, setSolo] = useState(false);

  useEffect(() => {
    let alive = true;
    const apply = (frame: StatusFrame, side: boolean) => {
      // Frames without a session come from single-turn mode, where there is
      // no session key at all and the flag is the whole truth.
      if (!frame.session) {
        setSolo(frame.running);
        return;
      }
      const key = (side ? "side:" : "") + frame.session;
      setSessions(prev => {
        const next = new Set(prev);
        if (frame.running) {
          next.add(key);
        } else {
          next.delete(key);
        }
        return next;
      });
    };
    api
      .state()
      .then(state => {
        if (!alive) return;
        const running = state.runningSessions ?? [];
        setSessions(new Set(running));
        setSolo(state.running && running.length === 0);
      })
      .catch(() => {});
    const source = new EventSource("/api/events");
    source.addEventListener("status", event => applyFrame(event, frame => apply(frame, false)));
    source.addEventListener("sidestatus", event => applyFrame(event, frame => apply(frame, true)));
    return () => {
      alive = false;
      source.close();
    };
  }, []);

  return { working: solo || sessions.size > 0, count: sessions.size };
}

function applyFrame(event: Event, handle: (frame: StatusFrame) => void): void {
  try {
    handle(JSON.parse((event as MessageEvent<string>).data) as StatusFrame);
  } catch {
    // A malformed frame is not worth dying over; the next one resyncs us.
  }
}

// useNativeGestures wires the shell-only gestures. A drag is posted to the
// native handler only after real movement — arming it on mousedown would make
// AppKit swallow the double-click that has to reach the page.
function useNativeGestures(): void {
  useEffect(() => {
    const post = (message: string) => {
      const webkit = (window as unknown as {
        webkit?: { messageHandlers?: { external?: { postMessage?: (message: string) => void } } };
      }).webkit;
      webkit?.messageHandlers?.external?.postMessage?.(message);
    };
    let armed = false;
    let originX = 0;
    let originY = 0;
    const down = (event: MouseEvent) => {
      if (event.button !== 0) return;
      armed = true;
      originX = event.clientX;
      originY = event.clientY;
    };
    const move = (event: MouseEvent) => {
      if (!armed) return;
      const dx = event.clientX - originX;
      const dy = event.clientY - originY;
      if (dx * dx + dy * dy < 16) return;
      armed = false;
      post("wails:drag");
    };
    const up = () => {
      armed = false;
    };
    const doubleClick = () => {
      api.focusShell().catch(() => {});
    };
    document.addEventListener("mousedown", down);
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
    document.addEventListener("dblclick", doubleClick);
    return () => {
      document.removeEventListener("mousedown", down);
      document.removeEventListener("mousemove", move);
      document.removeEventListener("mouseup", up);
      document.removeEventListener("dblclick", doubleClick);
    };
  }, []);
}

function Pet() {
  const { working, count } = useRunningState();
  useNativeGestures();
  return (
    <div className={`pet ${working ? "pet--working" : "pet--idle"}`}>
      <div className="pet__bubble">{working ? (count > 1 ? `工作中… ×${count}` : "工作中…") : "休息中~"}</div>
      <div className="pet__stage">
        <div className="pet__shadow" />
        <div className="pet__character">
          <img src={whaleGirl} alt="Atlas 桌面宠物" draggable={false} />
        </div>
        <div className="pet__props pet__props--desk">
          <div className="pet__desk" />
          <div className="pet__laptop">
            <div className="pet__laptop__screen" />
            <div className="pet__laptop__base" />
          </div>
        </div>
        <div className="pet__props pet__props--tv">
          <div className="pet__tv">
            <div className="pet__tv__antenna" />
            <div className="pet__tv__body">
              <div className="pet__tv__screen" />
            </div>
            <div className="pet__tv__legs" />
          </div>
        </div>
      </div>
    </div>
  );
}

const root = document.getElementById("root");
if (!root) throw new Error("atlas: #root is missing from pet.html");

createRoot(root).render(
  <StrictMode>
    <Pet />
  </StrictMode>,
);
