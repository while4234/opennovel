const confirmationAction = Object.freeze({
  code: 'confirm_character_candidate',
  label: '确认角色卡并继续',
  target: 'foundation.review.characters',
  centerView: 'writing',
  sideView: 'cocreate',
  tab: 'characters',
  anchor: 'foundation-review-confirm-character'
});

export function characterConfirmationRequiredFromSnapshot(snapshot = {}) {
  const workflow = objectValue(
    snapshot,
    'CharacterWorkflow',
    'characterWorkflow',
    'character_workflow'
  );
  return textValue(workflow, 'AnalysisStatus', 'analysisStatus', 'analysis_status') === 'candidate_ready' &&
    textValue(workflow, 'ReviewStatus', 'reviewStatus', 'review_status') === 'passed' &&
    textValue(workflow, 'ConfirmationStatus', 'confirmationStatus', 'confirmation_status') === 'unconfirmed';
}

export function characterConfirmationRequiredFromWorkspace(workspace) {
  return Boolean(
    workspace?.candidate &&
    workspace?.confirmationStatus === 'unconfirmed' &&
    workspace?.allowedOperations?.includes('confirm')
  );
}

export function projectNextAction(snapshot = {}) {
  const progress = objectValue(snapshot, 'WorkflowProgress', 'workflowProgress', 'workflow_progress');
  const nextAction = objectValue(progress, 'NextAction', 'nextAction', 'next_action');
  const actionID = textValue(nextAction, 'ID', 'id');
  if (actionID === confirmationAction.code || characterConfirmationRequiredFromSnapshot(snapshot)) {
    return { ...confirmationAction };
  }
  return null;
}

function objectValue(value, ...keys) {
  for (const key of keys) {
    const candidate = value?.[key];
    if (candidate && typeof candidate === 'object' && !Array.isArray(candidate)) {
      return candidate;
    }
  }
  return {};
}

function textValue(value, ...keys) {
  for (const key of keys) {
    const candidate = String(value?.[key] ?? '').trim();
    if (candidate) return candidate;
  }
  return '';
}
