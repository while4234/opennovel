import { FolderOpen } from 'lucide-react';
import { useEffect, useState } from 'react';

export function useIndependentProjectScope(projects = [], { allowGlobal = false } = {}) {
  const [projectId, setProjectId] = useState(allowGlobal ? '' : projects[0]?.id || '');

  useEffect(() => {
    if (allowGlobal && !projectId) return;
    if (projects.some((project) => project.id === projectId)) return;
    setProjectId(allowGlobal ? '' : projects[0]?.id || '');
  }, [allowGlobal, projectId, projects]);

  return [projectId, setProjectId];
}

export function ProjectScopeSelector({
  allowGlobal = false,
  disabled = false,
  label = '目标项目',
  onChange,
  projects = [],
  value
}) {
  return (
    <label className="knowledge-project-selector">
      <span><FolderOpen size={16} />{label}</span>
      <select aria-label={label} disabled={disabled || (!allowGlobal && projects.length === 0)} onChange={(event) => onChange(event.target.value)} value={value}>
        {allowGlobal ? <option value="">全部项目</option> : null}
        {!allowGlobal && projects.length === 0 ? <option value="">暂无项目</option> : null}
        {projects.map((project) => <option key={project.id} value={project.id}>{project.name || project.id}</option>)}
      </select>
    </label>
  );
}

export function createScopeRequestGate() {
  let version = 0;
  return {
    begin() {
      version += 1;
      return version;
    },
    isCurrent(requestVersion) {
      return requestVersion === version;
    },
    invalidate() {
      version += 1;
    }
  };
}
