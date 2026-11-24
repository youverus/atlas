export type ControlTab =
  | "models"
  | "mcp"
  | "skills"
  | "plugins"
  | "browser"
  | "devices"
  | "remote"
  | "schedules"
  | "usage"
  | "general";

export interface SettingsSection {
  id: ControlTab;
  label: string;
  subtitle: string;
  glyph: string;
  group: "能力配置" | "系统设置";
}

export const SETTINGS_SECTIONS: SettingsSection[] = [
  {
    id: "models",
    label: "模型",
    subtitle: "切换模型、调整推理强度与编辑供应商配置",
    glyph: "◈",
    group: "能力配置",
  },
  {
    id: "mcp",
    label: "MCP",
    subtitle: "查看与刷新已连接的 MCP 服务和远程工具",
    glyph: "☍",
    group: "能力配置",
  },
  {
    id: "skills",
    label: "Skill",
    subtitle: "浏览当前工作区可用技能、预览说明并快捷调用",
    glyph: "✦",
    group: "能力配置",
  },
  {
    id: "plugins",
    label: "插件",
    subtitle: "查看已安装插件及其提供的 Skill 与 MCP 能力",
    glyph: "⊞",
    group: "能力配置",
  },
  {
    id: "browser",
    label: "浏览器",
    subtitle: "控制托管 Chrome 窗口、接管标签页与预览画面",
    glyph: "◎",
    group: "能力配置",
  },
  {
    id: "devices",
    label: "远程设备",
    subtitle: "管理 SSH 远程计算主机、二进制自动部署与模型配置同步",
    glyph: "⎔",
    group: "系统设置",
  },
  {
    id: "remote",
    label: "远程控制",
    subtitle: "配置飞书机器人长连接、配对审批、白名单与会话绑定",
    glyph: "⇄",
    group: "系统设置",
  },
  {
    id: "schedules",
    label: "定时任务",
    subtitle: "设置定时触发或固定间隔执行的代理任务，可选模型、目录与连续会话",
    glyph: "◷",
    group: "系统设置",
  },
  {
    id: "usage",
    label: "统计",
    subtitle: "查看本机各模型的 Token 用量、平均速度和每日用量",
    glyph: "▥",
    group: "系统设置",
  },
  {
    id: "general",
    label: "通用",
    subtitle: "配置执行权限策略、外观主题与热重载配置",
    glyph: "⚙",
    group: "系统设置",
  },
];

const SECTION_GROUPS: Array<SettingsSection["group"]> = ["能力配置", "系统设置"];

export function SettingsNav({
  active,
  mcpCount,
  skillCount,
  onSelect,
  onBack,
}: {
  active: ControlTab;
  mcpCount?: number;
  skillCount?: number;
  onSelect: (tab: ControlTab) => void;
  onBack: () => void;
}) {
  const badgeOf = (id: ControlTab): number | undefined => {
    if (id === "mcp" && mcpCount && mcpCount > 0) return mcpCount;
    if (id === "skills" && skillCount && skillCount > 0) return skillCount;
    return undefined;
  };

  return (
    <div className="settings-nav" aria-label="设置导航">
      <div className="settings-nav__head">
        <button type="button" className="settings-nav__back" onClick={onBack} title="返回会话列表">
          <span aria-hidden="true">←</span>
          <span>返回对话</span>
        </button>
        <span className="settings-nav__heading">设置</span>
      </div>

      <div className="settings-nav__scroll" role="tablist" aria-orientation="vertical" aria-label="设置选项">
        {SECTION_GROUPS.map((groupName) => (
          <div className="settings-nav__group" key={groupName}>
            <div className="settings-nav__group-label">{groupName}</div>
            {SETTINGS_SECTIONS.filter((item) => item.group === groupName).map((item) => {
              const selected = active === item.id;
              const badge = badgeOf(item.id);
              return (
                <button
                  key={item.id}
                  type="button"
                  role="tab"
                  aria-selected={selected}
                  className="settings-nav__item"
                  data-active={selected ? "true" : undefined}
                  onClick={() => onSelect(item.id)}
                >
                  <span className="settings-nav__glyph" aria-hidden="true">
                    {item.glyph}
                  </span>
                  <span className="settings-nav__label">{item.label}</span>
                  {badge !== undefined ? <span className="settings-nav__badge">{badge}</span> : null}
                </button>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}
