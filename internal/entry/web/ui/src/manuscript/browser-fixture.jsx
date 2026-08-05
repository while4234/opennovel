import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import {
  MobileToolDetailHeader,
  MobileToolMenu,
  MobileWorkspaceChrome,
  mobileToolLabel
} from '../components/MobileWorkspaceChrome.jsx';
import { ManuscriptWorkspace } from './ManuscriptWorkspace.jsx';
import { FoundationCenter } from '../foundation/FoundationCenter.jsx';
import '../styles.css';

function BrowserFixture() {
  const surface = new URLSearchParams(globalThis.location.search).get('surface');
  const foundationSurface = surface === 'foundation';
  const [controlsTarget, setControlsTarget] = useState(null);
  const [projectId, setProjectId] = useState(foundationSurface ? 'foundation-project-a' : 'browser-project');
  const [notice, setNotice] = useState('');
  if (surface === 'mobile-shell') return <MobileShellFixture />;
  if (foundationSurface) {
    return <div className="foundation-browser-fixture" style={{ display: 'flex', flexDirection: 'column', height: '100vh', minHeight: 0, overflow: 'hidden' }}>
      <div className="inline-actions"><button type="button" onClick={() => setProjectId('foundation-project-a')}>项目 A</button><button type="button" onClick={() => setProjectId('foundation-project-b')}>项目 B</button></div>
      <div aria-live="polite" role="status">{notice}</div>
      <FoundationCenter projectId={projectId} onClose={() => setNotice('返回创作')} onOpenCoreCast={() => setNotice('打开现有 CoreCast 确认')} onOpenReview={(mode) => setNotice(`打开现有 ${mode} 审核`)} />
    </div>;
  }
  return <><aside ref={setControlsTarget} /><ManuscriptWorkspace active controlsTarget={controlsTarget} projectId="browser-project" /></>;
}

function MobileShellFixture() {
  const [projectDrawerOpen, setProjectDrawerOpen] = useState(false);
  const [toolDrawerOpen, setToolDrawerOpen] = useState(false);
  const [actionsOpen, setActionsOpen] = useState(false);
  const [toolView, setToolView] = useState('menu');
  const [sideView, setSideView] = useState('status');
  const [centerView, setCenterView] = useState('writing');

  const closeLayers = () => {
    setProjectDrawerOpen(false);
    setToolDrawerOpen(false);
    setActionsOpen(false);
  };
  const openTools = () => {
    setProjectDrawerOpen(false);
    setActionsOpen(false);
    setToolView('menu');
    setToolDrawerOpen(true);
  };
  const selectTool = (tool) => {
    setSideView(tool);
    setToolView('detail');
  };

  return (
    <div className="app-shell mobile-shell-fixture">
      <nav className="mobile-workspace-nav" aria-label="紧凑工作台导航">
        <button aria-label="打开项目列表" onClick={() => {
          setToolDrawerOpen(false);
          setProjectDrawerOpen(true);
        }} type="button">项目</button>
        <strong>雾港来信</strong>
        <button aria-label="打开工具面板" onClick={openTools} type="button">工具</button>
      </nav>
      <MobileWorkspaceChrome
        actionsOpen={actionsOpen}
        connection="live"
        currentView={toolDrawerOpen ? 'tools' : centerView}
        projectName="雾港来信"
        onCloseActions={() => setActionsOpen(false)}
        onOpenActions={() => {
          setProjectDrawerOpen(false);
          setToolDrawerOpen(false);
          setActionsOpen(true);
        }}
        onOpenProjects={() => {
          setToolDrawerOpen(false);
          setActionsOpen(false);
          setProjectDrawerOpen(true);
        }}
        onOpenTools={openTools}
        onSelectManuscript={() => {
          closeLayers();
          setCenterView('manuscript');
        }}
        onSelectWriting={() => {
          closeLayers();
          setCenterView('writing');
        }}
        actions={(
          <>
            <button className="tool-button" type="button">设定中心</button>
            <button className="tool-button" type="button">保存快照</button>
            <button className="tool-button accent" type="button">恢复</button>
            <button className="tool-button danger-ghost" type="button">回退</button>
          </>
        )}
      />

      {(projectDrawerOpen || toolDrawerOpen) ? <button aria-label="关闭侧栏" className="mobile-drawer-backdrop" onClick={closeLayers} type="button" /> : null}
      <aside className={`project-pane ${projectDrawerOpen ? 'mobile-open' : ''}`} aria-label="项目导航">
        <div className="pane-header"><div className="brand-lockup"><div className="brand-mark">O</div><div><div className="eyebrow">OpenNovel</div><h1>小说工作台</h1></div></div></div>
        <div className="project-list">
          {['雾港来信', '长夜航线', '未命名手稿'].map((name, index) => <div className={`project-row ${index === 0 ? 'active' : ''}`} key={name}><button className="project-open-button" onClick={closeLayers} type="button"><span><strong>{name}</strong><small>刚刚编辑</small></span></button><button aria-label={`${name}操作`} className="project-more-button" type="button">•••</button></div>)}
        </div>
      </aside>

      <main className="writing-pane">
        {centerView === 'manuscript' ? (
          <section className="stream-area"><div className="eyebrow">专业稿件</div><h2>第一章 潮声</h2><article className="stream-round"><pre>雾从港口的旧钟楼漫下来，沿着潮湿的石阶缓慢铺开。这里用于验证横竖屏下正文不会被导航遮挡。</pre></article></section>
        ) : (
          <>
            <section className="workflow-progress"><div className="workflow-progress-header"><div><span className="workflow-progress-kicker">普通创作</span><h3>正在生成第一章</h3></div><span className="workflow-progress-status">进行中</span></div><div className="workflow-overall-meter"><span style={{ width: '62%' }} /></div></section>
            <div className="workbench-stack">
              <section className="stream-area"><article className="stream-round"><pre>雾从港口的旧钟楼漫下来，沿着潮湿的石阶缓慢铺开。主工作区保持单一滚动面，让当前内容始终占据屏幕主体。</pre></article><article className="stream-round"><pre>远处传来汽笛声，新的章节正在继续生成。</pre></article></section>
              <details className="event-feed" open><summary className="section-title"><span>事件</span><small>2</small></summary><div className="event-list"><div className="event-row running"><span className="event-dot" /><span className="event-time">16:20</span><strong>WRITE</strong><span>正在写作</span></div></div></details>
            </div>
            <form className="composer"><input aria-label="继续创作输入" placeholder="继续、补充或要求下一步..." /><button className="tool-button accent" type="button">继续</button></form>
          </>
        )}
      </main>

      <aside className={`status-pane ${toolDrawerOpen ? 'mobile-open' : ''}`} data-mobile-view={toolView} aria-label="创作与高级工具">
        <MobileToolMenu coCreateVisible onClose={closeLayers} onSelectTool={selectTool} />
        <MobileToolDetailHeader title={mobileToolLabel(sideView)} onBack={() => setToolView('menu')} onClose={closeLayers} />
        <div className="side-panel"><div className="side-content"><div className="section-title"><span>{mobileToolLabel(sideView)}</span></div><p>这里展示所选工具的完整任务内容和操作。</p><button className="tool-button accent" type="button">执行操作</button></div></div>
      </aside>
    </div>
  );
}

createRoot(document.getElementById('root')).render(<BrowserFixture />);
