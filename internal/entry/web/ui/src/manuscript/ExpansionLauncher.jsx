import { useEffect, useRef, useState } from 'react';
import { adjustManuscriptExpansion, cancelManuscriptExpansion, commandExpansionRevision, confirmManuscriptExpansion, getExpansionRevision, planManuscriptExpansion, processExpansionRevisionAudit } from './manuscript-api.js';
import { ExpansionForm } from './ExpansionForm.jsx';
import { ExpansionPreview } from './ExpansionPreview.jsx';
import { RevisionStepper } from './RevisionStepper.jsx';
import { expansionKey } from './expansion-state.js';

export const reconcileExpansionRevision = (current, incoming) => incoming || (current?.terminal ? current : null);

export const createExpansionOperationRegistry = (keyFactory = expansionKey) => {
  const operations = new Map();
  return {
    acquire(action, fingerprint, payload = null) {
      const identity = `${action}:${fingerprint}`;
      if (!operations.has(identity)) operations.set(identity, { identity, key: keyFactory(`expansion-${action}`), payload: payload == null ? null : structuredClone(payload), phase: 'pending', result: null });
      return operations.get(identity);
    },
    accept(operation, result) { operation.phase = 'accepted'; operation.result = result; },
    complete(operation) { operations.delete(operation.identity); },
  };
};

export function ExpansionLauncher({ projectId, phase, mode = 'normal', structureRevision = 1, structureSignature = '', selectedId = '', launchRequest, activeRevision, onConfirmed, hideLauncher = false, initialPreview = null, initialInstruction = '' }) {
  const [open, setOpen] = useState(Boolean(hideLauncher || initialPreview));
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState(initialPreview);
  const [confirmation, setConfirmation] = useState(null);
	const [revision, setRevision] = useState(null);
  const [error, setError] = useState('');
	const retryRevision = useRef(null);
  const [form, setForm] = useState({ location: phase === 'complete' ? 'book_end' : 'inside', sentence: initialInstruction, adjustment: 'default', referenceIds: selectedId ? [selectedId] : [] });
  const controller = useRef(null);
	const operationKeys = useRef(null);
	if (!operationKeys.current) operationKeys.current = createExpansionOperationRegistry();
	const operationKey = (action, fingerprint) => {
		return operationKeys.current.acquire(action, fingerprint);
	};
	const completeOperation = (operation) => operationKeys.current.complete(operation);
  useEffect(() => () => controller.current?.abort(), []);
	useEffect(() => {
		if (!initialPreview) return;
		setPreview(initialPreview); setOpen(true);
		setForm((old) => ({ ...old, sentence: initialInstruction || old.sentence }));
	}, [initialPreview?.preview_id, initialInstruction]);
	useEffect(() => {
		if (!confirmation && !activeRevision) return undefined;
		let stopped = false;
		const refresh = async () => { try { const data = await getExpansionRevision(projectId); const incoming = data?.revision ?? null; if (!stopped) setRevision((current) => reconcileExpansionRevision(current, incoming)); } catch { /* status is retried by polling/SSE */ } };
		refresh(); const timer = globalThis.setInterval(refresh, 2000);
		const onMutation = () => refresh(); window.addEventListener('ainovel:manuscript-mutated', onMutation);
		return () => { stopped = true; globalThis.clearInterval(timer); window.removeEventListener('ainovel:manuscript-mutated', onMutation); };
	}, [projectId, confirmation, activeRevision]);
  useEffect(() => {
    if (!launchRequest) return;
    setForm((old) => ({ ...old, location: launchRequest.location, referenceIds: launchRequest.referenceIds || [] }));
    setOpen(true);
  }, [launchRequest]);
  useEffect(() => {
    if (selectedId && !launchRequest) setForm((old) => ({ ...old, referenceIds: [selectedId] }));
  }, [selectedId]);
  async function run(task, onDefinitiveFailure) {
    controller.current?.abort(); controller.current = new AbortController(); setBusy(true); setError('');
    try { return await task(controller.current.signal); } catch (cause) { if (cause.name !== 'AbortError') { setError(cause.message); if (Number.isInteger(cause.status) && cause.status < 500 && cause.status !== 408 && cause.status !== 429) onDefinitiveFailure?.(); } return null; } finally { setBusy(false); }
  }
  async function plan() {
		const fingerprint = JSON.stringify([projectId, form.location, form.referenceIds, form.sentence.trim(), form.adjustment, structureRevision, structureSignature]);
		const operation = operationKey('plan', fingerprint);
    const data = await run((signal) => planManuscriptExpansion(projectId, { location: form.location, reference_ids: form.referenceIds, sentence: form.sentence.trim(), adjustment: form.adjustment, expected_structure_revision: structureRevision, expected_structure_signature: structureSignature, idempotency_key: operation.key }, signal));
    if (data) { completeOperation(operation); setPreview(data.preview); }
  }
  async function adjust(adjustment, edit = false) {
    if (edit) { setPreview(null); return; }
		const sentence = form.sentence.trim(), operation = operationKey('adjust', JSON.stringify([projectId, preview.preview_id, preview.base_revision, adjustment, sentence]));
    const data = await run((signal) => adjustManuscriptExpansion(projectId, { preview_id: preview.preview_id, expected_revision: preview.base_revision, adjustment, sentence, idempotency_key: operation.key }, signal));
    if (data) { completeOperation(operation); setPreview(data.preview); }
  }
  async function confirm() {
		const operation = operationKey('confirm', JSON.stringify([projectId, preview.preview_id, structureRevision]));
    const data = await run((signal) => confirmManuscriptExpansion(projectId, { preview_id: preview.preview_id, expected_revision: structureRevision, idempotency_key: operation.key }, signal));
    if (data) { completeOperation(operation); setConfirmation(data.confirmation); onConfirmed?.(data.confirmation); }
  }
  async function cancel() {
		const operation = operationKey('cancel', JSON.stringify([projectId, preview.preview_id, preview.base_revision]));
    const data = await run((signal) => cancelManuscriptExpansion(projectId, preview.preview_id, preview.base_revision, operation.key, signal));
    if (data) { completeOperation(operation); setPreview(data.preview); }
  }
	async function revisionCommand(action, message = '') {
		// A logical user command owns one key until its response is observed.
		// Polling/SSE may advance revision.revision after the server committed a
		// response that the browser lost, so the mutable server revision must not
		// participate in the client operation identity.
		const commandRevision = revision || confirmation?.revision || activeRevision;
		const operation = operationKeys.current.acquire(`revision-${action}`, JSON.stringify([projectId, message]), { projectId, action, expectedRevision: commandRevision?.revision, message });
		let data = operation.result;
		if (operation.phase !== 'accepted') {
			data = await run((signal) => commandExpansionRevision(operation.payload.projectId, operation.payload.action, operation.payload.expectedRevision, operation.key, operation.payload.message, signal), () => completeOperation(operation));
			if (!data) { retryRevision.current = { action, message }; return; }
			if (action === 'feedback') operationKeys.current.accept(operation, data);
			else completeOperation(operation);
		}
		if (action === 'feedback' && preview) {
			const repairedSentence = `${form.sentence.trim()}；按审核意见修复：${message}`;
			const repairPayload = { preview_id: preview.preview_id, expected_revision: preview.base_revision, adjustment: form.adjustment, sentence: repairedSentence };
			const repairOperation = operationKeys.current.acquire('feedback-repair', JSON.stringify([projectId, preview.preview_id, preview.base_revision, repairedSentence]), repairPayload);
			const repaired = await run((signal) => adjustManuscriptExpansion(projectId, { ...repairOperation.payload, idempotency_key: repairOperation.key }, signal), () => completeOperation(repairOperation));
			if (repaired) {
				completeOperation(repairOperation); completeOperation(operation); retryRevision.current = null; setForm((old) => ({ ...old, sentence: repairedSentence })); setPreview(repaired.preview);
				const refreshed = await getExpansionRevision(projectId);
				if (refreshed?.revision) data.revision = refreshed.revision;
			} else { retryRevision.current = { action, message }; return; }
		}
		retryRevision.current = null;
		setRevision(data.revision);
		if (action === 'request_audit') {
			const audited = await run((signal) => processExpansionRevisionAudit(projectId, signal));
			if (audited) setRevision(audited.revision);
		}
	}
  const label = phase === 'complete' ? '继续扩写' : '补剧情 / 扩写';
  return <section className="expansion-launcher">{!hideLauncher ? <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}>{label}</button> : null}
    {revision || activeRevision ? <p className="expansion-active-revision">已有修订正在进行，请先进入当前修订处理。<button type="button" onClick={() => onConfirmed?.({ revision: revision || activeRevision })}>进入当前修订</button></p> : null}
    {open ? <div className="expansion-dialog" role="region" aria-label="一句话补剧情与扩写">{!hideLauncher ? <ExpansionForm value={form} onChange={setForm} onSubmit={plan} busy={busy} mode={mode} /> : null}{error ? <div role="alert">{error}<button type="button" onClick={() => retryRevision.current ? revisionCommand(retryRevision.current.action, retryRevision.current.message) : preview ? adjust(form.adjustment) : plan()}>重试</button></div> : null}<ExpansionPreview preview={preview} onAdjust={adjust} onConfirm={revision ? undefined : confirm} onCancel={cancel} busy={busy || Boolean(revision)} />{confirmation || revision ? <RevisionStepper revision={revision || confirmation?.revision} onCommand={revisionCommand} onNavigate={() => onConfirmed?.({ revision })} /> : null}</div> : null}
  </section>;
}
