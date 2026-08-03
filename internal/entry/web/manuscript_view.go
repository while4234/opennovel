package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const manuscriptChunkParagraphs = 40

type manuscriptTreeNode struct {
	Kind             string               `json:"kind"`
	StableID         string               `json:"stable_id"`
	ParentID         string               `json:"parent_id,omitempty"`
	DisplayOrder     int                  `json:"display_order"`
	DisplayLabel     string               `json:"display_label"`
	State            string               `json:"state"`
	HasCurrent       bool                 `json:"has_current"`
	HasCandidate     bool                 `json:"has_candidate"`
	HasHistory       bool                 `json:"has_history"`
	ActiveRevision   bool                 `json:"active_revision"`
	ContentSignature string               `json:"content_signature,omitempty"`
	CurrentSignature string               `json:"current_signature,omitempty"`
	TargetDisplay    string               `json:"target_display,omitempty"`
	SourceDisplay    string               `json:"source_display,omitempty"`
	Children         []manuscriptTreeNode `json:"children,omitempty"`
}

type manuscriptRevisionMetadata struct {
	RevisionID string `json:"revision_id"`
	Revision   int    `json:"revision"`
	Stage      string `json:"stage"`
	UpdatedAt  string `json:"updated_at"`
}

func manuscriptRevisionMetadataList(items []domain.ManuscriptRevisionRuntime) []manuscriptRevisionMetadata {
	result := make([]manuscriptRevisionMetadata, 0, len(items))
	for _, item := range items {
		result = append(result, manuscriptRevisionMetadata{RevisionID: item.RevisionID, Revision: item.Revision, Stage: item.Stage, UpdatedAt: item.UpdatedAt})
	}
	return result
}

func (s *Server) handleManuscriptWorkspaceRoute(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService, action string) bool {
	switch {
	case action == "manuscript/workspace/recovery":
		s.handleManuscriptRecovery(w, r, st)
	case action == "manuscript/workspace/tree":
		s.handleManuscriptWorkspaceTree(w, r, manifest, st, service)
	case action == "manuscript/workspace/history":
		s.handleManuscriptHistory(w, r, manifest, st)
	case strings.HasPrefix(action, "manuscript/workspace/versions/"):
		s.handleManuscriptVersion(w, r, manifest, st, strings.TrimPrefix(action, "manuscript/workspace/versions/"))
	case strings.HasPrefix(action, "manuscript/workspace/artifacts/"):
		s.handleManuscriptArtifact(w, r, manifest, st, strings.TrimPrefix(action, "manuscript/workspace/artifacts/"))
	case strings.HasPrefix(action, "manuscript/workspace/chapters/"):
		s.handleManuscriptWorkspaceChapter(w, r, manifest, st, service, strings.TrimPrefix(action, "manuscript/workspace/chapters/"))
	case action == "manuscript/workspace/restore/preview":
		s.handleManuscriptRestorePreview(w, r, manifest, st, service)
	case action == "manuscript/workspace/restore":
		s.handleManuscriptRestore(w, r, manifest, st, service)
	default:
		return false
	}
	return true
}

func (s *Server) handleManuscriptRecovery(w http.ResponseWriter, r *http.Request, st *storepkg.Store) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	recovered := true
	if r.Method == http.MethodPost {
		recovered = st.RequireManuscriptWriteReady() == nil
	}
	status := st.ManuscriptRecoveryState()
	writeJSON(w, http.StatusOK, map[string]any{
		"recovered": recovered && !status.Required,
		"recovery":  status,
	})
}

func (s *Server) handleManuscriptWorkspaceTree(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService) {
	if r.Method != http.MethodGet {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		writeManuscriptError(w, fmt.Errorf("load manuscript progress: %w", err))
		return
	}
	if progress.Phase != domain.PhaseWriting && progress.Phase != domain.PhaseComplete {
		writeManuscriptError(w, fmt.Errorf("manuscript is only readable in writing or complete phase"))
		return
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	active, err := st.ManuscriptRevisions.Active()
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	contentIndex, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	nodes, err := buildManuscriptTreeNodes(st, volumes, progress, active, contentIndex)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	activeMetadata := manuscriptActiveRevisionMetadata(active)
	mode := domain.RevisionModeNormal
	if st.Adaptation.Exists() {
		mode = domain.RevisionModeAdaptation
	}
	payload := map[string]any{"project": manifest, "phase": progress.Phase, "nodes": nodes, "active_revision": activeMetadata, "structure_revision": domain.StructureRevision(volumes), "structure_signature": domain.StructureSignature(volumes), "content_index_signature": contentIndex.Signature, "mode": mode}
	// The project manifest contains access-time metadata. Keep it in the body,
	// but derive validators only from manuscript truth.
	etag := `"` + domain.ContentSignature(mustJSON(struct {
		Phase  domain.Phase         `json:"phase"`
		Nodes  []manuscriptTreeNode `json:"nodes"`
		Active any                  `json:"active_revision"`
		Index  string               `json:"content_index_signature"`
	}{progress.Phase, nodes, activeMetadata, contentIndex.Signature})) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, payload)
}

func buildManuscriptTreeNodes(st *storepkg.Store, volumes []domain.VolumeOutline, progress *domain.Progress, active *domain.ManuscriptRevisionRuntime, contentIndex *storepkg.ManuscriptContentIndex) ([]manuscriptTreeNode, error) {
	adaptationMode := active != nil && active.Mode == domain.RevisionModeAdaptation
	if !adaptationMode {
		if plan, err := st.Adaptation.LoadPlan(); err == nil && plan != nil {
			adaptationMode = true
		}
	}
	completed := make(map[int]bool, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		completed[chapter] = true
	}
	currentSignatures := make(map[string]string)
	if contentIndex != nil {
		for _, entry := range contentIndex.Entries {
			currentSignatures[entry.StableID] = entry.CurrentSHA256
		}
	}
	result := make([]manuscriptTreeNode, 0, len(volumes))
	for vi, volume := range volumes {
		volumeID := strings.TrimSpace(volume.ID)
		if volumeID == "" {
			volumeID = domain.LegacyStructureID(st.Dir(), domain.StructureKindVolume, fmt.Sprintf("volume/%d", vi+1))
		}
		v := manuscriptTreeNode{Kind: "volume", StableID: volumeID, DisplayOrder: vi + 1, DisplayLabel: volume.Title, State: "planned", ContentSignature: signedManuscriptArtifact("volume", volumeID, volume).Signature}
		for ai, arc := range volume.Arcs {
			arcID := strings.TrimSpace(arc.ID)
			if arcID == "" {
				arcID = domain.LegacyStructureID(st.Dir(), domain.StructureKindArc, fmt.Sprintf("volume/%d/arc/%d", vi+1, ai+1))
			}
			a := manuscriptTreeNode{Kind: "arc", StableID: arcID, ParentID: volumeID, DisplayOrder: ai + 1, DisplayLabel: arc.Title, State: "planned", ContentSignature: signedManuscriptArtifact("arc", arcID, arc).Signature}
			for ci, chapter := range arc.Chapters {
				id := strings.TrimSpace(chapter.ID)
				if id == "" {
					id = domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, fmt.Sprintf("chapters/%04d", chapter.Chapter))
				}
				state := "planned"
				if completed[chapter.Chapter] {
					state = "completed"
				} else if progress.Phase == domain.PhaseWriting && chapter.Chapter == progress.CurrentChapter {
					state = "writing"
				}
				// The tree is a metadata projection. Never read prose or the full
				// runtime here; content and signatures are loaded by explicit views.
				hasCurrent := completed[chapter.Chapter] || (progress.Phase == domain.PhaseWriting && chapter.Chapter <= progress.CurrentChapter)
				hasCandidate, activeForChapter := false, false
				if active != nil {
					activeForChapter = active.Baseline.ChapterID == id
					for _, item := range active.Queue {
						if item.ChapterID == id {
							activeForChapter = true
						}
					}
					for _, candidate := range active.Candidates {
						if candidate.ChapterID == id {
							hasCandidate = true
						}
					}
				}
				if activeForChapter {
					switch active.Stage {
					case "failed":
						state = "revision_failed"
					case "audit_pending", "final_approval_pending", "ready_to_publish", "completion_revalidation_pending":
						state = "review_pending"
					case "approval_pending", "candidate_generating":
						state = "working_draft"
					default:
						if hasCandidate {
							state = "rewrite_pending"
						}
					}
				}
				hasHistory, err := st.ManuscriptRevisions.HasHistory(id)
				if err != nil {
					return nil, err
				}
				node := manuscriptTreeNode{Kind: "chapter", StableID: id, ParentID: arcID, DisplayOrder: chapter.Chapter, DisplayLabel: chapter.Title, State: state, HasCurrent: hasCurrent, HasCandidate: hasCandidate, HasHistory: hasHistory, ActiveRevision: activeForChapter, ContentSignature: signedManuscriptArtifact("outline", id, chapter).Signature, CurrentSignature: currentSignatures[id]}
				if adaptationMode {
					node.TargetDisplay = fmt.Sprintf("目标第 %d 章", chapter.Chapter)
					node.SourceDisplay = adaptationSourceDisplay(st, id)
				}
				a.Children = append(a.Children, node)
				_ = ci
			}
			v.Children = append(v.Children, a)
		}
		result = append(result, v)
	}
	return result, nil
}

func manuscriptActiveRevisionMetadata(active *domain.ManuscriptRevisionRuntime) any {
	if active == nil {
		return nil
	}
	return map[string]any{"revision_id": active.RevisionID, "revision": active.Revision, "stage": active.Stage, "updated_at": active.UpdatedAt}
}

type manuscriptArtifact struct {
	Kind      string `json:"kind"`
	StableID  string `json:"stable_id"`
	Content   any    `json:"content"`
	Signature string `json:"signature"`
}

func signedManuscriptArtifact(kind, stableID string, content any) manuscriptArtifact {
	artifact := manuscriptArtifact{Kind: kind, StableID: stableID, Content: content}
	artifact.Signature = domain.ContentSignature(mustJSON(struct {
		Kind     string `json:"kind"`
		StableID string `json:"stable_id"`
		Content  any    `json:"content"`
	}{kind, stableID, content}))
	return artifact
}

func (s *Server) handleManuscriptArtifact(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, rest string) {
	if r.Method != http.MethodGet {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	parts := strings.SplitN(strings.Trim(rest, "/"), "/", 2)
	if len(parts) != 2 {
		writeManuscriptRequestError(w, http.StatusBadRequest, "artifact kind and stable id are required")
		return
	}
	kind, stableID := parts[0], parts[1]
	if kind == "review" && strings.Contains(stableID, "/") {
		reviewParts := strings.SplitN(stableID, "/", 2)
		s.handleManuscriptReviewDetail(w, r, manifest, st, reviewParts[0], reviewParts[1])
		return
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	var artifact manuscriptArtifact
	found := false
	for volumeIndex, volume := range volumes {
		volumeID := strings.TrimSpace(volume.ID)
		if volumeID == "" {
			volumeID = domain.LegacyStructureID(st.Dir(), domain.StructureKindVolume, fmt.Sprintf("volume/%d", volumeIndex+1))
		}
		if kind == "volume" && volumeID == stableID {
			artifact, found = signedManuscriptArtifact(kind, stableID, volume), true
		}
		for _, arc := range volume.Arcs {
			for _, chapter := range arc.Chapters {
				chapterID := strings.TrimSpace(chapter.ID)
				if chapterID == "" {
					chapterID = domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, fmt.Sprintf("chapters/%04d", chapter.Chapter))
				}
				if kind == "outline" && chapterID == stableID {
					artifact, found = signedManuscriptArtifact(kind, stableID, chapter), true
				}
			}
		}
	}
	if kind == "review" {
		cursor, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		artifact, found, err = manuscriptReviewArtifact(st, stableID, cursor, limit)
		if err != nil {
			writeManuscriptError(w, err)
			return
		}
	}
	if !found {
		writeManuscriptError(w, storepkg.ErrManuscriptRevisionNotFound)
		return
	}
	etag := `"` + artifact.Signature + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "artifact": artifact})
}

func manuscriptReviewArtifact(st *storepkg.Store, stableID string, cursor, limit int) (manuscriptArtifact, bool, error) {
	history, err := st.ManuscriptRevisions.List(stableID, cursor, limit)
	if err != nil {
		return manuscriptArtifact{}, false, err
	}
	audits := make([]map[string]any, 0)
	for _, metadata := range history.Items {
		runtime, loadErr := st.ManuscriptRevisions.Load(metadata.RevisionID)
		if loadErr != nil {
			return manuscriptArtifact{}, false, loadErr
		}
		for _, candidate := range runtime.Candidates {
			if candidate.ChapterID != stableID || candidate.AuditArtifact == nil {
				continue
			}
			audits = append(audits, map[string]any{"revision_id": metadata.RevisionID, "signature": candidate.AuditArtifact.Signature, "candidate_signature": candidate.AuditArtifact.CandidateSignature, "evidence_signatures": candidate.AuditArtifact.EvidenceSignatures, "content_loaded": false})
		}
	}
	content := map[string]any{
		"status":      "none",
		"revisions":   manuscriptRevisionMetadataList(history.Items),
		"audits":      audits,
		"next_cursor": history.NextOffset,
		"has_more":    history.HasMore,
	}
	active, err := st.ManuscriptRevisions.Active()
	if err != nil {
		return manuscriptArtifact{}, false, err
	}
	if active != nil && manuscriptRuntimeHasChapter(*active, stableID) {
		content["status"], content["active_revision"] = active.Stage, manuscriptActiveRevisionMetadata(active)
	}
	return signedManuscriptArtifact("review", stableID, content), true, nil
}

func (s *Server) handleManuscriptReviewDetail(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, stableID, revisionID string) {
	runtime, err := st.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	for _, candidate := range runtime.Candidates {
		if candidate.ChapterID != stableID || candidate.AuditArtifact == nil {
			continue
		}
		view, viewErr := manuscriptCandidateAuditView(st, revisionID, candidate)
		if viewErr != nil {
			writeManuscriptError(w, viewErr)
			return
		}
		artifact := signedManuscriptArtifact("review_detail", stableID, view)
		etag := `"` + artifact.Signature + `"`
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "artifact": artifact})
		return
	}
	writeManuscriptError(w, storepkg.ErrManuscriptRevisionNotFound)
}

func manuscriptCandidateAuditView(st *storepkg.Store, revisionID string, candidate domain.ManuscriptCandidate) (map[string]any, error) {
	if _, err := st.ManuscriptRevisions.Content().Read(candidate.AuditArtifact.Report); err != nil {
		return nil, err
	}
	findingsPayload, err := st.ManuscriptRevisions.Content().Read(candidate.AuditArtifact.Findings)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"report":   "审核报告已通过完整性校验，以下仅展示面向作者的发现。",
		"findings": humanReadableAuditFindings(findingsPayload),
	}, nil
}

func humanReadableAuditFindings(payload []byte) []map[string]string {
	var raw []map[string]any
	if json.Unmarshal(payload, &raw) != nil {
		return []map[string]string{{"severity": "提示", "summary": "审核发现已记录，请按报告建议处理。"}}
	}
	result := make([]map[string]string, 0, len(raw))
	for _, finding := range raw {
		item := make(map[string]string)
		for _, field := range []string{"severity", "level", "title", "summary", "message", "action", "suggestion"} {
			value, ok := finding[field].(string)
			if !ok || strings.TrimSpace(value) == "" {
				continue
			}
			key := field
			if field == "level" {
				key = "severity"
			}
			item[key] = strings.TrimSpace(value)
		}
		if len(item) > 0 {
			result = append(result, item)
		}
	}
	return result
}

func manuscriptRuntimeHasChapter(runtime domain.ManuscriptRevisionRuntime, stableID string) bool {
	if runtime.Baseline.ChapterID == stableID {
		return true
	}
	for _, item := range runtime.Queue {
		if item.ChapterID == stableID {
			return true
		}
	}
	return false
}

func (s *Server) handleManuscriptWorkspaceChapter(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService, rest string) {
	if strings.HasSuffix(rest, "/manual-candidate") {
		stableID := strings.TrimSuffix(rest, "/manual-candidate")
		if r.Method != http.MethodPost {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !manuscriptChapterStableIDPattern.MatchString(stableID) {
			writeManuscriptRequestError(w, http.StatusBadRequest, "stable_id must be a chapter stable ID")
			return
		}
		var request struct {
			ContentSignature string `json:"content_signature"`
			Prose            string `json:"prose"`
			IdempotencyKey   string `json:"idempotency_key"`
		}
		if err := decodeJSONBody(r, &request); err != nil {
			writeManuscriptRequestError(w, http.StatusBadRequest, err.Error())
			return
		}
		runtime, err := service.SubmitManualCandidate(r.Context(), host.ManualManuscriptCandidateRequest{
			ChapterID: stableID, ExpectedProseSHA: request.ContentSignature, Prose: request.Prose,
		}, request.IdempotencyKey)
		if err != nil {
			writeManuscriptError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": runtime})
		return
	}
	if r.Method != http.MethodGet {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	stableID := strings.TrimSuffix(rest, "/content")
	if !manuscriptChapterStableIDPattern.MatchString(stableID) {
		writeManuscriptRequestError(w, http.StatusBadRequest, "stable_id must be a chapter stable ID")
		return
	}
	view := defaultString(strings.TrimSpace(r.URL.Query().Get("view")), "current")
	version := strings.TrimSpace(r.URL.Query().Get("version"))
	if view != "current" && view != "candidate" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "chapter content view must be current or candidate; use the version endpoint for history")
		return
	}
	if view == "current" && version != "" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "current view must not carry version")
		return
	}
	if view == "candidate" && version == "" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "candidate view requires version")
		return
	}
	baseline, prose, err := service.CurrentChapter(stableID)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if view == "candidate" {
		active, loadErr := st.ManuscriptRevisions.Active()
		if loadErr != nil {
			writeManuscriptError(w, loadErr)
			return
		}
		if active == nil || active.RevisionID != version {
			writeManuscriptError(w, storepkg.ErrManuscriptRevisionNotFound)
			return
		}
		found := false
		for _, candidate := range active.Candidates {
			if candidate.ChapterID == stableID {
				payload, readErr := st.ManuscriptRevisions.Content().Read(candidate.Prose)
				if readErr != nil {
					writeManuscriptError(w, readErr)
					return
				}
				prose, baseline.CurrentProseSHA256, found = string(payload), candidate.Prose.SHA256, true
				break
			}
		}
		if !found {
			writeManuscriptError(w, storepkg.ErrManuscriptRevisionNotFound)
			return
		}
	}
	writeManuscriptChunk(w, r, manifest, baseline, view, version, prose)
}

func writeManuscriptChunk(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, baseline domain.ManuscriptBaseline, view, versionID, prose string) {
	etag := `"` + baseline.CurrentProseSHA256 + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	paragraphs := splitManuscriptParagraphs(prose)
	cursor, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	if cursor < 0 {
		cursor = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = manuscriptChunkParagraphs
	}
	if cursor > len(paragraphs) {
		cursor = len(paragraphs)
	}
	end := cursor + limit
	if end > len(paragraphs) {
		end = len(paragraphs)
	}
	next := any(nil)
	if end < len(paragraphs) {
		next = end
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "chapter": map[string]any{"stable_id": baseline.ChapterID, "display_chapter": baseline.DisplayChapter, "view": defaultString(view, "current"), "version_id": strings.TrimSpace(versionID), "content_signature": baseline.CurrentProseSHA256, "paragraphs": paragraphs[cursor:end], "next_cursor": next, "total_paragraphs": len(paragraphs)}})
}

func (s *Server) handleManuscriptHistory(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store) {
	if r.Method != http.MethodGet {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, err := st.ManuscriptRevisions.List(r.URL.Query().Get("chapter_id"), offset, limit)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	payload := map[string]any{"project": manifest, "items": manuscriptRevisionMetadataList(page.Items), "next_cursor": page.NextOffset, "has_more": page.HasMore}
	etag := `"` + domain.ContentSignature(mustJSON(struct {
		Items      []manuscriptRevisionMetadata `json:"items"`
		NextCursor int                          `json:"next_cursor"`
		HasMore    bool                         `json:"has_more"`
	}{manuscriptRevisionMetadataList(page.Items), page.NextOffset, page.HasMore})) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleManuscriptVersion(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, revisionID string) {
	if r.Method != http.MethodGet {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	revisionID = strings.TrimSpace(revisionID)
	chapterID := strings.TrimSpace(r.URL.Query().Get("chapter_id"))
	if !manuscriptRevisionStableIDPattern.MatchString(revisionID) || !manuscriptChapterStableIDPattern.MatchString(chapterID) {
		writeManuscriptRequestError(w, http.StatusBadRequest, "revision_id and chapter_id must be stable IDs")
		return
	}
	runtime, err := st.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		if errors.Is(err, storepkg.ErrManuscriptRevisionNotFound) {
			writeManuscriptVersionGone(w)
			return
		}
		writeManuscriptError(w, err)
		return
	}
	for _, candidate := range runtime.Candidates {
		if candidate.ChapterID != chapterID {
			continue
		}
		prose, readErr := st.ManuscriptRevisions.Content().Read(candidate.Prose)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				writeManuscriptVersionGone(w)
				return
			}
			writeManuscriptError(w, readErr)
			return
		}
		baseline := runtime.Baseline
		baseline.ChapterID, baseline.DisplayChapter, baseline.CurrentProseSHA256 = candidate.ChapterID, candidate.DisplayChapter, candidate.Prose.SHA256
		writeManuscriptChunk(w, r, manifest, baseline, "history", revisionID, string(prose))
		return
	}
	writeManuscriptVersionGone(w)
}

func writeManuscriptVersionGone(w http.ResponseWriter) {
	writeManuscriptEnvelope(w, http.StatusGone, "version_gone", "historical version is no longer available", map[string]any{"action": "reload_history"})
}

func splitManuscriptParagraphs(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	parts := strings.Split(value, "\n")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if text := strings.TrimSpace(part); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func mustJSON(value any) []byte { payload, _ := json.Marshal(value); return payload }
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
