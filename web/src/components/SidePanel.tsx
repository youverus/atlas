import { useEffect, useRef } from "react";
import type { Block } from "../lib/fold";
import type { PathCompletionScope } from "../lib/pathCompletion";
import type { ResizeHandleProps } from "../lib/useColumnWidth";
import { BlockView } from "./ChatWindow";
import { Composer } from "./Composer";

// 侧对话：常驻右侧的第二个对话，主对话仍在左边可见。
//
// 呈现方式是这一块唯一需要解释的决定。终端的 /side 是一次模式切换——提示符换
// 一个颜色，屏幕上的东西还是同一片；窗口里那样做会让「我这句话发给谁」变成一个
// 只存在于状态里的问题。改成两根并排的列之后，答案由输入框的位置回答：左边那个
// 发给主对话，右边那个发给侧对话，两个都在视野里。
//
// 因此这里不画任何「当前在侧对话」的标记，也不接 /parent：两个对话同时可见，
// 「把输入还给主对话」没有对象。
//
// inherited 是继承来的块数，用来在上面放一条分隔线：线以下是读者在面板里问的，
// 线以上是侧对话从主对话那儿拿到的那一份。按块算而不是按消息算，否则线会落在
// 一次工具调用的中间。
export function SidePanel({
  blocks,
  inherited,
  running,
  fileScope,
  stopping,
  onAsk,
  onStop,
  onClose,
  resizer,
}: {
  blocks: Block[];
  inherited: number;
  running: boolean;
  fileScope?: PathCompletionScope;
  stopping: boolean;
  onAsk: (text: string) => void;
  onStop?: () => void;
  onClose: () => void;
  /** 左边缘那根可拖的把手。宽度由页面持有（它要活过重新加载），面板只负责把它
   *  摆在边上——两边都在读同一份宽度，没有第二份。 */
  resizer: ResizeHandleProps;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);

  // 跟着末端走，但只在读者本来就在末端时：翻上去看一句话不该被下一个 token 顶
  // 回去。和主对话同一套判断（48px 之内算贴在底部）。
  useEffect(() => {
    const node = scroll.current;
    if (!node || !pinned.current) return;
    node.scrollTop = node.scrollHeight;
  }, [blocks]);

  const onScroll = () => {
    const node = scroll.current;
    if (!node) return;
    pinned.current = node.scrollHeight - node.scrollTop - node.clientHeight < 48;
  };

  // 分隔线落在线之前的那一块之后。没有线，或者线下面还是空的，就都不画：一条
  // 上面什么都没有的线只是在说「这里本来有东西」。继承的块数正好等于全部时，
  // 读者还没在这里问过任何一句。
  const seam = inherited > 0 && inherited < blocks.length ? inherited - 1 : -1;

  return (
    <section className="side" aria-label="侧对话">
      <div className="resizer resizer--left" title="拖动改变面板宽度，双击复位" {...resizer} />
      <div className="drawer__head">
        <span className="drawer__title">侧对话</span>
        <span className="side__count" title="这个面板从主对话那儿读到的块数">
          {inherited > 0 ? `继承 ${inherited} 块` : "未继承"}
        </span>
        <button
          type="button"
          className="icon-button"
          onClick={onClose}
          disabled={running}
          aria-label="关掉侧对话"
          title={running ? "这一轮还在跑，等它结束再关" : "关掉侧对话，丢掉这里面的一切（/side off）"}
        >
          ×
        </button>
      </div>

      <div className="side__scroll" ref={scroll} onScroll={onScroll}>
        {blocks.length === 0 ? (
          <p className="side__empty">
            这是主对话的临时分支。在这里问的问题只写进这个面板，主对话不受影响，它也不会存到磁盘上。
          </p>
        ) : (
          <div className="side__column">
            {blocks.map((block, index) => (
              <div key={block.id} className="side__item">
                <BlockView block={block} />
                {index === seam ? (
                  <div className="side__seam">
                    <span className="side__seam-rule" />
                    <span>以下是这里问的</span>
                    <span className="side__seam-rule" />
                  </div>
                ) : null}
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="side__foot">
        <Composer
          plain
          disabled={running}
          fileScope={fileScope}
          stopping={stopping}
          onStop={onStop}
          placeholder="问点别的…  @ 选文件"
          onSend={onAsk}
        />
      </div>
    </section>
  );
}
