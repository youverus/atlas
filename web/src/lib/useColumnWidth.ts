// 拖出来的列宽。
//
// 宽度是页面的状态而不是样式表里的常量：左右两根列都能拖，而拖出来的宽度得活过一次
// 重新加载——每次打开都回到默认宽度的侧栏，等于没有这个功能。键与主题同一套命名
// （`atlas.…`），一起放在 localStorage 里。
//
// 指针事件收在 window 上而不是把手的捕获上：捕获只是优化（光标跑到窗口外也还回得
// 来），不能当作前提——拿不住捕获的时候（合成输入、触摸被系统收走），只靠把手的话
// 一次拖动会在第一步就断，而屏幕上的光标还停在「左右箭头」，看起来像窗口卡住了。
// 所以按下之后整段手势归 window 管，抬起来（或窗口失焦）才收。
//
// 键盘也认这个把手：它是 role="separator"，左右键各走一步。只能拖的控件在键上不存在，
// 而这一格调的是「我想看到多少信息」——那正是键盘读者也会有的需求。

import { useCallback, useEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from "react";

/** 把手落在哪条边上。右栏的把手在它的左边，所以往左拖是变宽——方向只由这一项决定。 */
export type ColumnEdge = "left" | "right";

/** 中列（对话）至少要留下的宽度。两根列都能拖，而把阅读列挤成一条缝不是读者想要的
 *  结果，是拖过头的症状。 */
const MIDDLE_FLOOR = 320;

/** 键盘每按一次走多少像素。按键没有拖动的连续性，一步要比一像素粗才用得上。 */
const KEY_STEP = 16;

/** 一个可以拖的把手。它被摊进那个 div，所以类型写在这里而不是每个调用点各写一遍。 */
export type ResizeHandleProps = {
  role: "separator";
  tabIndex: number;
  "aria-orientation": "vertical";
  "aria-label": string;
  "aria-valuenow": number;
  "aria-valuemin": number;
  "aria-valuemax": number;
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void;
  onDoubleClick: () => void;
};

export function useColumnWidth({
  storageKey,
  label,
  initial,
  min,
  max,
  edge,
}: {
  storageKey: string;
  /** 把手自己的名字，读屏时说的是「哪一列」。 */
  label: string;
  /** 默认宽度，也是双击复位的落点。 */
  initial: number;
  min: number;
  max: number;
  edge: ColumnEdge;
}): { width: number; dragging: boolean; handleProps: ResizeHandleProps } {
  const [width, setWidth] = useState(() => clampTo(read(storageKey, initial), min, max));
  const [dragging, setDragging] = useState(false);
  // 拖动起点与最新宽度。两者都走 ref：一次快速的拖动会连着送来几个 move，而每个 move
  // 读到的该是上一个 move 留下的宽度，不是上一帧渲染时的宽度。
  const origin = useRef({ x: 0, width });
  const latest = useRef(width);
  const live = useRef(false);

  const apply = useCallback(
    (next: number) => {
      const value = clampTo(next, min, max);
      latest.current = value;
      setWidth(value);
    },
    [min, max],
  );

  // 只有落地才写盘：一次拖动上百个事件，而读者要记住的是最后停在哪一格。
  const commit = useCallback(
    (next: number) => {
      apply(next);
      localStorage.setItem(storageKey, String(latest.current));
    },
    [apply, storageKey],
  );

  // 拖动期间整页都换成左右箭头：光标一离开那几像素就变回文本光标，读者会以为拖动断
  // 了；选字也要关掉，否则一次拖动会在侧栏里刷出一片蓝色选区。
  useEffect(() => {
    if (!dragging) return;
    const body = document.body.style;
    const { cursor, userSelect } = body;
    body.cursor = "col-resize";
    body.userSelect = "none";
    return () => {
      body.cursor = cursor;
      body.userSelect = userSelect;
    };
  }, [dragging]);

  // 手势挂在 window 上，而不是等着指针捕获把事件送回把手。捕获是优化，不是前提：拿不
  // 到捕获的时候（合成的指针输入、触摸被系统的滚动接管），后续的 move 会落到光标底下
  // 那个元素上——实测是对话区——于是拖动在第一步就断，而光标还停在左右箭头上。
  useEffect(() => {
    if (!dragging) return;

    // 抬起来或被系统收走都算落地：宽度留在最后一格，因为读者看到的那一格就是拖动真的
    // 停下的地方，退回去反而更奇怪。
    //
    // 这里刻意不听 window 的 blur：窗口本来就没聚焦的时候（内嵌面板、刚切过去的应用）
    // 它在第一帧就会送来一次，一次正常的拖动当场断在起点。
    const settle = () => {
      if (!live.current) return;
      live.current = false;
      setDragging(false);
      commit(latest.current);
    };

    const move = (event: PointerEvent) => {
      if (!live.current) return;
      // 一个已经松开的指针还在移动，说明 up 落在了窗口外面、没人收到它。这一格必须收：
      // 不收的话光标一直挂着「左右箭头」，下一次点击也会被当成还在拖动。
      if (event.buttons === 0) {
        settle();
        return;
      }
      const moved = event.clientX - origin.current.x;
      apply(origin.current.width + (edge === "right" ? moved : -moved));
    };

    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", settle);
    window.addEventListener("pointercancel", settle);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", settle);
      window.removeEventListener("pointercancel", settle);
    };
  }, [dragging, apply, commit, edge]);

  // 窗口变窄时重新夹一次。下限与「中列保底」是同一个承诺，而只在拖动那一刻成立的承诺
  // 等于没有：缩窗口的读者会把阅读列挤成一条缝，他并没有做错什么。代价是缩窄再放大之后
  // 回不到原来那一格——这里不另存一份「读者本来想要多宽」——但那个方向是可以恢复的，
  // 再拖一次就有。
  useEffect(() => {
    const onResize = () => apply(latest.current);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [apply]);

  const handleProps: ResizeHandleProps = {
    role: "separator",
    tabIndex: 0,
    "aria-orientation": "vertical",
    "aria-label": label,
    "aria-valuenow": width,
    "aria-valuemin": min,
    "aria-valuemax": max,
    onPointerDown: (event) => {
      // 只认主键：右键与中键在这条边上没有含义，而接管它们会吃掉上下文菜单。
      if (event.button !== 0) return;
      event.preventDefault();
      // 捕获让光标跑到窗口外也还收得到事件，但收拾手势的是上面那段 window 监听——两
      // 者都在收同一份 move，重复应用不了，因为宽度算的是「起点 + 总位移」而不是累加。
      event.currentTarget.setPointerCapture(event.pointerId);
      origin.current = { x: event.clientX, width: latest.current };
      live.current = true;
      setDragging(true);
    },
    onKeyDown: (event) => {
      const step = event.key === "ArrowLeft" ? -KEY_STEP : event.key === "ArrowRight" ? KEY_STEP : 0;
      if (step === 0) return;
      event.preventDefault();
      // 键推的是把手，不是宽度：右栏的把手在左边，往右推就是变窄。
      commit(latest.current + (edge === "right" ? step : -step));
    },
    onDoubleClick: () => commit(initial),
  };

  return { width, dragging, handleProps };
}

// clampTo keeps a width inside its own range and leaves the reading column room:
// when the window is the smaller of the two, the ceiling yields and the floor
// still wins — a floor that lost would leave the handle stuck while the column
// kept growing.
function clampTo(value: number, min: number, max: number): number {
  const ceiling = Math.max(min, Math.min(max, window.innerWidth - MIDDLE_FLOOR));
  return Math.round(Math.min(ceiling, Math.max(min, value)));
}

function read(key: string, fallback: number): number {
  const stored = Number(localStorage.getItem(key));
  return Number.isFinite(stored) && stored > 0 ? stored : fallback;
}
