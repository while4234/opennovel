import {
  getObservabilityRecommendations,
  getObservabilityUsage,
  importExternalNovel,
  importSimulationProfile,
  listNovelLibrary,
  listSimulationLibrary,
  loadNovelFromLibrary,
  loadSimulationFromLibrary,
  saveSimulationToLibrary,
  uploadAdaptationSource,
  uploadSimulationLibrary
} from '../api.js';

export const knowledgeAPI = {
  listNovels: listNovelLibrary,
  loadNovel: loadNovelFromLibrary,
  importNovel: importExternalNovel,
  uploadNovel: uploadAdaptationSource,
  listProfiles: listSimulationLibrary,
  uploadProfiles: uploadSimulationLibrary,
  loadProfile: loadSimulationFromLibrary,
  importProfile: importSimulationProfile,
  saveProfile: saveSimulationToLibrary,
  usage: getObservabilityUsage,
  recommendations: getObservabilityRecommendations
};

export function libraryItems(response) {
  if (Array.isArray(response)) return response;
  if (Array.isArray(response?.items)) return response.items;
  return [];
}

export function libraryItemName(entry) {
  return String(entry?.name || entry?.Name || entry?.file_name || entry?.FileName || '').trim();
}

export function libraryItemMeta(entry, kind) {
  const size = Number(entry?.size || entry?.Size || 0);
  const count = kind === 'novels'
    ? Number(entry?.chapter_count || entry?.ChapterCount || 0)
    : Number(entry?.source_count || entry?.SourceCount || 0);
  const countLabel = kind === 'novels' ? `${count} 章` : `${count} 个语料来源`;
  return [count ? countLabel : '', size ? formatBytes(size) : '', entry?.health_state || entry?.HealthState || '']
    .filter(Boolean)
    .join(' · ') || '可加载到所选项目';
}

function formatBytes(value) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / 1024 / 1024).toFixed(1)} MiB`;
}
