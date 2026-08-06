import {
  Activity,
  BookOpen,
  ChevronLeft,
  Clock3,
  Database,
  Download,
  FileText,
  Gauge,
  Menu,
  MessageSquareText,
  MoreHorizontal,
  Server,
  Settings,
  SlidersHorizontal,
  TestTube2,
  Upload,
  WandSparkles,
  X
} from 'lucide-react';
import { useEffect, useRef } from 'react';
import './MobileWorkspaceChrome.css';

export const MOBILE_TOOL_GROUPS = [
  {
    label: '创作管理',
    items: [
      { id: 'status', label: '运行状态', description: '进度、恢复与当前任务', icon: Gauge },
      { id: 'cocreate', label: '共创', description: '与 AI 讨论并整理创作结果', icon: MessageSquareText, conditional: true },
      { id: 'simulate', label: '画像', description: '仿写画像与来源管理', icon: WandSparkles },
      { id: 'settings', label: '项目设定', description: '文风、思考与项目参数', icon: SlidersHorizontal },
      { id: 'schedule', label: '定时任务', description: '设置自动恢复时间', icon: Clock3 }
    ]
  },
  {
    label: '文稿流程',
    items: [
      { id: 'adapt', label: '改编', description: '上传来源并生成改编提案', icon: FileText },
      { id: 'audit', label: '改编审计', description: '检查提案与成稿偏差', icon: TestTube2 },
      { id: 'continuation', label: '续写', description: '导入旧稿并继续创作', icon: Upload },
      { id: 'export', label: '导出', description: '生成并下载项目文稿', icon: Download }
    ]
  },
  {
    label: '系统管理',
    items: [
      { id: 'diag', label: '诊断', description: '检查项目运行问题', icon: Activity },
      { id: 'cache', label: '缓存', description: '查看上下文缓存状态', icon: Database },
      { id: 'backend', label: '后端', description: '查看后端服务状态', icon: Server },
      { id: 'models', label: '模型', description: '管理模型与服务商', icon: Settings }
    ]
  }
];

const PRIMARY_VIEW_LABELS = {
  manuscript: '专业稿件'
};

export function mobileToolLabel(toolID) {
  if (PRIMARY_VIEW_LABELS[toolID]) return PRIMARY_VIEW_LABELS[toolID];
  return MOBILE_TOOL_GROUPS
    .flatMap((group) => group.items)
    .find((item) => item.id === toolID)?.label || '工具详情';
}

const CONNECTION_LABELS = {
  live: '已连接',
  reconnecting: '正在重连',
  degraded: '连接不稳定',
  offline: '已离线',
  connected: '已连接',
  connecting: '连接中',
  disconnected: '已断开',
  error: '连接异常',
  idle: '待连接'
};

export function MobileWorkspaceChrome({
  actionsOpen,
  actions,
  connection,
  currentView,
  onCloseActions,
  onOpenActions,
  onOpenProjects,
  onOpenTools,
  onSelectManuscript,
  onSelectWriting,
  projectName
}) {
  const actionsDialogRef = useRef(null);
  const actionsTriggerRef = useRef(null);

  useEffect(() => {
    if (!actionsOpen) return undefined;
    const dialog = actionsDialogRef.current;
    dialog?.querySelector('button:not(:disabled)')?.focus();

    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCloseActions();
        globalThis.requestAnimationFrame?.(() => actionsTriggerRef.current?.focus());
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(dialog?.querySelectorAll('button:not(:disabled)') || []);
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [actionsOpen, onCloseActions]);

  const sectionLabel = currentView === 'manuscript'
    ? '专业稿件'
    : currentView === 'foundation'
      ? '设定中心'
      : '创作工作台';

  return (
    <>
      <header className="mobile-phone-topbar">
        <button aria-label="打开项目列表" className="mobile-chrome-icon-button" onClick={onOpenProjects} type="button">
          <Menu size={21} />
        </button>
        <div className="mobile-phone-title">
          <strong>{projectName || 'OpenNovel'}</strong>
          <span>{sectionLabel}</span>
        </div>
        <span
          aria-label={`连接状态：${CONNECTION_LABELS[connection] || connection}`}
          className="mobile-connection-indicator"
          data-status={connection}
          role="status"
          title={CONNECTION_LABELS[connection] || connection}
        />
        <button
          ref={actionsTriggerRef}
          aria-expanded={actionsOpen}
          aria-label="打开项目操作"
          className="mobile-chrome-icon-button"
          onClick={onOpenActions}
          type="button"
        >
          <MoreHorizontal size={22} />
        </button>
      </header>

      <nav className="mobile-phone-bottom-nav" aria-label="移动端主要导航">
        <MobileNavButton active={currentView === 'writing' || currentView === 'foundation'} icon={MessageSquareText} label="创作" onClick={onSelectWriting} />
        <MobileNavButton active={currentView === 'manuscript'} icon={BookOpen} label="稿件" onClick={onSelectManuscript} />
        <MobileNavButton active={currentView === 'tools'} icon={SlidersHorizontal} label="工具" onClick={onOpenTools} />
      </nav>

      {actionsOpen ? (
        <div className="mobile-action-backdrop" onMouseDown={onCloseActions} role="presentation">
          <section
            ref={actionsDialogRef}
            aria-label="项目操作"
            aria-modal="true"
            className="mobile-action-sheet"
            onMouseDown={(event) => event.stopPropagation()}
            role="dialog"
          >
            <header>
              <div>
                <span>当前项目</span>
                <strong>{projectName || '尚未选择项目'}</strong>
              </div>
              <button aria-label="关闭项目操作" className="mobile-chrome-icon-button" onClick={onCloseActions} type="button">
                <X size={20} />
              </button>
            </header>
            <div className="mobile-action-list">{actions}</div>
          </section>
        </div>
      ) : null}
    </>
  );
}

export function MobileToolMenu({ coCreateVisible, onClose, onSelectTool }) {
  return (
    <div className="mobile-tool-menu">
      <header className="mobile-tool-menu-heading">
        <div className="mobile-tool-menu-title-row">
          <div>
            <span>工作台</span>
            <h2>工具中心</h2>
          </div>
          <button aria-label="关闭工具中心" className="mobile-chrome-icon-button" onClick={onClose} type="button">
            <X size={20} />
          </button>
        </div>
        <p>选择一项任务，完成后可返回这里继续切换。</p>
      </header>
      {MOBILE_TOOL_GROUPS.map((group) => {
        const items = group.items.filter((item) => !item.conditional || coCreateVisible);
        return (
          <section className="mobile-tool-group" key={group.label}>
            <h3>{group.label}</h3>
            <div className="mobile-tool-grid">
              {items.map((item) => {
                const Icon = item.icon;
                return (
                  <button key={item.id} onClick={() => onSelectTool(item.id)} type="button">
                    <span className="mobile-tool-icon"><Icon size={19} /></span>
                    <span>
                      <strong>{item.label}</strong>
                      <small>{item.description}</small>
                    </span>
                  </button>
                );
              })}
            </div>
          </section>
        );
      })}
    </div>
  );
}

export function MobileToolDetailHeader({ title, onBack, onClose }) {
  return (
    <header className="mobile-tool-detail-header">
      <button aria-label="返回工具中心" className="mobile-chrome-icon-button" onClick={onBack} type="button">
        <ChevronLeft size={21} />
      </button>
      <strong>{title}</strong>
      <button aria-label="关闭工具" className="mobile-chrome-icon-button" onClick={onClose} type="button">
        <X size={20} />
      </button>
    </header>
  );
}

function MobileNavButton({ active, icon: Icon, label, onClick }) {
  return (
    <button aria-current={active ? 'page' : undefined} className={active ? 'active' : ''} onClick={onClick} type="button">
      <Icon size={20} />
      <span>{label}</span>
    </button>
  );
}
