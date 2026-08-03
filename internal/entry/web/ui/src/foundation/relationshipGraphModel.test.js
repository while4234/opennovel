import { describe, expect, it } from 'vitest';
import {
  auditSignatureNamespace, buildRelationshipGraph, deterministicGraphPositions, foundationGraphLayoutKey,
  loadFoundationGraphLayout, normalizeGraphDirection, saveFoundationGraphLayout
} from './relationshipGraphModel.js';

describe('planned relationship graph model', () => {
  const characters = [
    { id: 'a', name: '甲', aliases: ['A'], tier: 'major', faction: '北境' },
    { id: 'b', name: '乙', tier: 'support', faction: '南境' },
    { id: 'c', name: '丙', tier: 'support', faction: '北境' }
  ];
  const relationships = [
    { id: 'directed', source_character_id: 'a', target_character_id: 'b', direction: 'directed', type: 'mentor', status: 'active' },
    { id: 'legacy', source_character_id: 'b', target_character_id: 'c', direction: 'mutual', type: 'ally', status: 'planned' },
    { id: 'missing', source_character_id: 'a', target_character_id: 'gone', direction: 'undirected', type: 'other', status: 'planned' }
  ];

  it('uses stable IDs, maps all direction semantics and filters missing endpoints', () => {
    const graph = buildRelationshipGraph({ characters, relationships });
    expect(graph.nodes.map((node) => node.id)).toEqual(['a', 'b', 'c']);
    expect(graph.edges.map((edge) => edge.id)).toEqual(['directed', 'legacy']);
    expect(graph.edges[0]).toEqual(expect.objectContaining({ source: 'a', target: 'b', markerStart: undefined, markerEnd: 'arrow' }));
    expect(graph.edges[1]).toEqual(expect.objectContaining({ markerStart: 'arrow', markerEnd: 'arrow' }));
    expect(graph.edges[1].label).toContain('双向');
    expect(graph.warnings).toEqual([expect.objectContaining({ code: 'missing_endpoint', relationshipID: 'missing' })]);
    expect(graph.source).toBe('StoryFoundation.relationships');
    expect(normalizeGraphDirection('undirected')).toBe('undirected');
  });

  it('supports search, tier/faction/type/status and one-hop focus without O(n²) scans', () => {
    expect(buildRelationshipGraph({ characters, relationships, filters: { search: 'A' } }).nodes.map((node) => node.id)).toEqual(['a']);
    expect(buildRelationshipGraph({ characters, relationships, filters: { faction: '北境' } }).nodes.map((node) => node.id)).toEqual(['a', 'c']);
    expect(buildRelationshipGraph({ characters, relationships, filters: { relationshipType: 'ally', relationshipStatus: 'planned' }, showIsolated: false }).nodes.map((node) => node.id)).toEqual(['b', 'c']);
    expect(buildRelationshipGraph({ characters, relationships, focusNodeID: 'b' }).nodes.map((node) => node.id)).toEqual(['a', 'b', 'c']);
    expect(buildRelationshipGraph({ characters, relationships, focusNodeID: 'a' }).nodes.map((node) => node.id)).toEqual(['a', 'b']);
  });

  it('maps 100 nodes and 300 edges deterministically with large-graph degradation', () => {
    const largeCharacters = Array.from({ length: 100 }, (_, index) => ({ id: `character-${String(index).padStart(3, '0')}`, name: `角色 ${index}`, tier: index < 5 ? 'major' : 'support', faction: `阵营 ${index % 4}` }));
    const largeRelationships = Array.from({ length: 300 }, (_, index) => ({ id: `relationship-${index}`, source_character_id: largeCharacters[index % 100].id, target_character_id: largeCharacters[(index * 7 + 1) % 100].id, direction: index % 3 === 0 ? 'directed' : index % 3 === 1 ? 'bidirectional' : 'undirected', type: 'ally', status: 'planned' }));
    const limited = buildRelationshipGraph({ characters: largeCharacters, relationships: largeRelationships });
    const all = buildRelationshipGraph({ characters: largeCharacters, relationships: largeRelationships, showAll: true });
    expect(limited.limited).toBe(true);
    expect(limited.nodes).toHaveLength(5);
    expect(all.nodes).toHaveLength(100);
    expect(all.edges).toHaveLength(300);
    const positions = deterministicGraphPositions(largeCharacters);
    expect(positions).toEqual(deterministicGraphPositions([...largeCharacters].reverse()));
    expect(positions['character-001'].x - positions['character-000'].x).toBe(430);
    expect(positions['character-010'].y - positions['character-000'].y).toBe(250);
  });

  it('isolates layout by project and audit signature and persists coordinates only', () => {
    const values = new Map();
    const storage = { getItem: (key) => values.get(key) || null, setItem: (key, value) => values.set(key, value) };
    expect(saveFoundationGraphLayout(storage, 'project', 'audit-one', { a: { x: 1, y: 2 }, b: { x: Number.NaN, y: 4 } }, () => '2026-07-19T00:00:00Z')).toBe(true);
    const raw = values.get(foundationGraphLayoutKey('project', 'audit-one'));
    expect(raw).not.toContain('premise');
    expect(raw).not.toContain('角色');
    expect(raw).not.toContain('audit-one');
    expect(foundationGraphLayoutKey('project', 'audit-one')).not.toContain('audit-one');
    expect(raw).toContain(auditSignatureNamespace('audit-one'));
    expect(loadFoundationGraphLayout(storage, 'project', 'audit-one', ['a'])).toEqual({ a: { x: 1, y: 2 } });
    expect(loadFoundationGraphLayout(storage, 'project', 'audit-two', ['a'])).toEqual({});
  });
});
