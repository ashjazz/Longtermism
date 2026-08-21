package smoke

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"
)

type privacyArtifactPublishStage string

const (
	privacyArtifactPublishAPISummary         privacyArtifactPublishStage = "api_summary"
	privacyArtifactPublishApplicationLog     privacyArtifactPublishStage = "application_log_projection"
	privacyArtifactPublishCollectorComposite privacyArtifactPublishStage = "collector_composite_proof"
	privacyArtifactPublishChatReport         privacyArtifactPublishStage = "chat_fixture_report"
	privacyArtifactPublishManifest           privacyArtifactPublishStage = "manifest"
)

type privacyArtifactStoreTestHooks struct {
	BeforePublish func(privacyArtifactPublishStage) error
}

type PrivacyArtifactStore struct {
	directory *os.File
	mu        sync.RWMutex
	closed    bool
	hooks     privacyArtifactStoreTestHooks
}

type privacyArtifactPreparedFile struct {
	stage   privacyArtifactPublishStage
	ref     string
	payload []byte
	kind    PrivacyArtifactKind
}

func OpenPrivacyArtifactStore(root string) (*PrivacyArtifactStore, error) {
	return openPrivacyArtifactStoreForTest(root, privacyArtifactStoreTestHooks{})
}

func openPrivacyArtifactStoreForTest(root string, hooks privacyArtifactStoreTestHooks) (*PrivacyArtifactStore, error) {
	directory, err := openPrivacyArtifactRoot(root)
	if err != nil {
		return nil, newPrivacyArtifactStoreError()
	}
	return &PrivacyArtifactStore{directory: directory, hooks: hooks}, nil
}

func (store *PrivacyArtifactStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed || store.directory == nil {
		return nil
	}
	store.closed = true
	if store.directory.Close() != nil {
		return newPrivacyArtifactStoreError()
	}
	return nil
}

func (store *PrivacyArtifactStore) Write(ctx context.Context, input PrivacyFixtureArtifactInput) (PrivacyFixtureArtifactRefs, error) {
	if store == nil || !privacyArtifactLockContext(ctx) {
		return PrivacyFixtureArtifactRefs{}, newPrivacyArtifactStoreError()
	}
	prepared, manifest, refs, err := preparePrivacyArtifacts(input)
	if err != nil {
		return PrivacyFixtureArtifactRefs{}, newPrivacyArtifactStoreError()
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil || int64(len(manifestPayload)) > maximumPrivacyArtifactBytes || scanPrivacyArtifactPayloads(input.ForbiddenCanary, appendPrivacyPayloads(prepared, manifestPayload)...) != nil {
		return PrivacyFixtureArtifactRefs{}, newPrivacyArtifactStoreError()
	}
	prepared = append(prepared, privacyArtifactPreparedFile{stage: privacyArtifactPublishManifest, ref: refs.ManifestRef, payload: manifestPayload})

	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed || store.directory == nil || ctx.Err() != nil {
		return PrivacyFixtureArtifactRefs{}, newPrivacyArtifactStoreError()
	}
	if err := lockPrivacyArtifactDirectory(ctx, store.directory, true); err != nil {
		return PrivacyFixtureArtifactRefs{}, newPrivacyArtifactStoreError()
	}
	defer unlockPrivacyArtifactDirectory(store.directory)
	if !validOpenPrivacyArtifactDirectory(store.directory) || privacyArtifactFinalExists(store.directory, prepared) {
		return PrivacyFixtureArtifactRefs{}, newPrivacyArtifactStoreError()
	}

	published := make([]string, 0, len(prepared))
	rollback := func() {
		for index := len(published) - 1; index >= 0; index-- {
			_ = unlinkPrivacyArtifactAt(store.directory, published[index])
		}
		_ = store.directory.Sync()
	}
	for _, file := range prepared {
		if ctx.Err() != nil || store.hooks.BeforePublish != nil && store.hooks.BeforePublish(file.stage) != nil {
			rollback()
			return PrivacyFixtureArtifactRefs{}, newPrivacyArtifactStoreError()
		}
		if err := publishPrivacyArtifactAt(store.directory, file.ref, file.payload); err != nil {
			rollback()
			return PrivacyFixtureArtifactRefs{}, newPrivacyArtifactStoreError()
		}
		published = append(published, file.ref)
	}
	return refs, nil
}

func (store *PrivacyArtifactStore) Resolve(ctx context.Context, request PrivacyArtifactResolveRequest) (PrivacyArtifactManifest, error) {
	if store == nil || !privacyArtifactLockContext(ctx) || !validPrivacyArtifactResolveRequest(request) {
		return PrivacyArtifactManifest{}, newPrivacyArtifactStoreError()
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed || store.directory == nil || ctx.Err() != nil {
		return PrivacyArtifactManifest{}, newPrivacyArtifactStoreError()
	}
	if err := lockPrivacyArtifactDirectory(ctx, store.directory, false); err != nil {
		return PrivacyArtifactManifest{}, newPrivacyArtifactStoreError()
	}
	defer unlockPrivacyArtifactDirectory(store.directory)
	manifest, err := store.resolveLocked(request)
	if err != nil {
		return PrivacyArtifactManifest{}, newPrivacyArtifactStoreError()
	}
	return clonePrivacyArtifactManifest(manifest), nil
}

func (store *PrivacyArtifactStore) Read(ctx context.Context, request PrivacyArtifactReadRequest) (PrivacyArtifactDocument, error) {
	if store == nil || !privacyArtifactLockContext(ctx) || !validPrivacyArtifactKind(request.Kind) || !validPrivacyArtifactResolveRequest(request.Manifest) {
		return PrivacyArtifactDocument{}, newPrivacyArtifactStoreError()
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed || store.directory == nil || ctx.Err() != nil {
		return PrivacyArtifactDocument{}, newPrivacyArtifactStoreError()
	}
	if err := lockPrivacyArtifactDirectory(ctx, store.directory, false); err != nil {
		return PrivacyArtifactDocument{}, newPrivacyArtifactStoreError()
	}
	defer unlockPrivacyArtifactDirectory(store.directory)
	manifest, err := store.resolveLocked(request.Manifest)
	if err != nil {
		return PrivacyArtifactDocument{}, newPrivacyArtifactStoreError()
	}
	binding, ok := privacyArtifactBindingForKind(manifest, request.Kind)
	if !ok {
		return PrivacyArtifactDocument{}, newPrivacyArtifactStoreError()
	}
	payload, err := readPrivacyArtifactAt(store.directory, binding.Ref)
	if err != nil || int64(len(payload)) != binding.SizeBytes || privacyArtifactSHA256(payload) != binding.SHA256 {
		return PrivacyArtifactDocument{}, newPrivacyArtifactStoreError()
	}
	if err := validatePersistedPrivacyArtifact(payload, request.Kind, manifest); err != nil {
		return PrivacyArtifactDocument{}, newPrivacyArtifactStoreError()
	}
	return PrivacyArtifactDocument{Kind: request.Kind, Content: append([]byte(nil), payload...)}, nil
}

func (store *PrivacyArtifactStore) resolveLocked(request PrivacyArtifactResolveRequest) (PrivacyArtifactManifest, error) {
	if !validOpenPrivacyArtifactDirectory(store.directory) {
		return PrivacyArtifactManifest{}, errPrivacyArtifactStore
	}
	payload, err := readPrivacyArtifactAt(store.directory, request.ManifestRef)
	if err != nil {
		return PrivacyArtifactManifest{}, errPrivacyArtifactStore
	}
	var manifest PrivacyArtifactManifest
	if err := strictPrivacyArtifactJSON(payload, &manifest); err != nil || !validPrivacyArtifactManifest(manifest, request) {
		return PrivacyArtifactManifest{}, errPrivacyArtifactStore
	}
	return manifest, nil
}

func preparePrivacyArtifacts(input PrivacyFixtureArtifactInput) ([]privacyArtifactPreparedFile, PrivacyArtifactManifest, PrivacyFixtureArtifactRefs, error) {
	base := privacyArtifactEnvelope{
		SchemaVersion: "1", RunID: input.RunID, Marker: input.Marker, RequestID: input.RequestID, AITraceID: input.AITraceID,
		ServiceTraceID: input.ServiceTraceID, SpanID: input.SpanID, StartedAt: input.StartedAt, Deadline: input.Deadline,
	}
	if !isSafePollMarker(input.ForbiddenCanary) || !validPrivacyArtifactIdentity(input.RunID, input.Marker, input.RequestID, input.AITraceID,
		input.ServiceTraceID, input.SpanID, input.StartedAt, input.Deadline) || !validPrivacyCategoryCounts(input.APIScanSummary) ||
		!validPrivacyApplicationProjection(input.ApplicationLogProjection, base) || !validPrivacyCollectorProof(input.CollectorCompositeProof, base) ||
		!validPrivacyChatReport(input.ChatReport, base) {
		return nil, PrivacyArtifactManifest{}, PrivacyFixtureArtifactRefs{}, errPrivacyArtifactStore
	}
	chatPayload, err := json.Marshal(input.ChatReport)
	if err != nil {
		return nil, PrivacyArtifactManifest{}, PrivacyFixtureArtifactRefs{}, errPrivacyArtifactStore
	}
	refs := derivePrivacyArtifactRefs(input.RunID, input.Marker)
	definitions := []struct {
		stage privacyArtifactPublishStage
		kind  PrivacyArtifactKind
		ref   string
		set   func(*privacyArtifactEnvelope)
	}{
		{privacyArtifactPublishAPISummary, PrivacyArtifactKindAPISummary, refs.APISummaryRef, func(document *privacyArtifactEnvelope) {
			document.APISummary = clonePrivacyCounts(input.APIScanSummary)
		}},
		{privacyArtifactPublishApplicationLog, PrivacyArtifactKindApplicationLogProjection, refs.ApplicationLogRef, func(document *privacyArtifactEnvelope) {
			projection := clonePrivacyApplicationProjection(input.ApplicationLogProjection)
			document.ApplicationLog = &projection
		}},
		{privacyArtifactPublishCollectorComposite, PrivacyArtifactKindCollectorCompositeProof, refs.CollectorArtifactRef, func(document *privacyArtifactEnvelope) {
			proof := input.CollectorCompositeProof
			document.Collector = &proof
		}},
		{privacyArtifactPublishChatReport, PrivacyArtifactKindChatFixtureReport, refs.ChatReportRef, func(document *privacyArtifactEnvelope) {
			document.ChatReport = append(json.RawMessage(nil), chatPayload...)
		}},
	}
	prepared := make([]privacyArtifactPreparedFile, 0, len(definitions))
	bindings := make([]PrivacyArtifactBinding, 0, len(definitions))
	for _, definition := range definitions {
		document := base
		document.Kind = definition.kind
		definition.set(&document)
		payload, err := json.Marshal(document)
		if err != nil || int64(len(payload)) == 0 || int64(len(payload)) > maximumPrivacyArtifactBytes {
			return nil, PrivacyArtifactManifest{}, PrivacyFixtureArtifactRefs{}, errPrivacyArtifactStore
		}
		prepared = append(prepared, privacyArtifactPreparedFile{stage: definition.stage, ref: definition.ref, payload: payload, kind: definition.kind})
		bindings = append(bindings, PrivacyArtifactBinding{Kind: definition.kind, Ref: definition.ref, SHA256: privacyArtifactSHA256(payload), SizeBytes: int64(len(payload))})
	}
	if scanPrivacyArtifactPayloads(input.ForbiddenCanary, appendPrivacyPayloads(prepared)...) != nil {
		return nil, PrivacyArtifactManifest{}, PrivacyFixtureArtifactRefs{}, errPrivacyArtifactStore
	}
	manifest := PrivacyArtifactManifest{
		SchemaVersion: "1", RunID: input.RunID, Marker: input.Marker, RequestID: input.RequestID, AITraceID: input.AITraceID,
		ServiceTraceID: input.ServiceTraceID, SpanID: input.SpanID, StartedAt: input.StartedAt, Deadline: input.Deadline, Artifacts: bindings,
	}
	return prepared, manifest, refs, nil
}

func appendPrivacyPayloads(files []privacyArtifactPreparedFile, extra ...[]byte) [][]byte {
	result := make([][]byte, 0, len(files)+len(extra))
	for _, file := range files {
		result = append(result, file.payload)
	}
	return append(result, extra...)
}

func derivePrivacyArtifactRefs(runID, marker string) PrivacyFixtureArtifactRefs {
	return PrivacyFixtureArtifactRefs{
		ManifestRef:          privacyArtifactRef("pair", runID+"\x00"+marker, "manifest"),
		APISummaryRef:        privacyArtifactRef("run", runID, "api-summary"),
		ApplicationLogRef:    privacyArtifactRef("marker", marker, "application-log"),
		CollectorArtifactRef: privacyArtifactRef("pair", runID+"\x00"+marker, "collector"),
		ChatReportRef:        privacyArtifactRef("pair", runID+"\x00"+marker, "chat-report"),
	}
}

func privacyArtifactRef(namespace, identity, suffix string) string {
	digest := privacyArtifactSHA256([]byte(namespace + "\x00" + identity))
	return "privacy-" + digest[len("sha256:"):] + "-" + suffix + ".json"
}

func validPrivacyArtifactResolveRequest(request PrivacyArtifactResolveRequest) bool {
	refs := derivePrivacyArtifactRefs(request.RunID, request.Marker)
	return request.ManifestRef == refs.ManifestRef && safePrivacyArtifactRef(request.ManifestRef) &&
		validPrivacyArtifactIdentity(request.RunID, request.Marker, request.RequestID, request.AITraceID,
			request.ServiceTraceID, request.SpanID, request.StartedAt, request.Deadline)
}

func validPrivacyArtifactManifest(manifest PrivacyArtifactManifest, request PrivacyArtifactResolveRequest) bool {
	if manifest.SchemaVersion != "1" || manifest.RunID != request.RunID || manifest.Marker != request.Marker ||
		manifest.RequestID != request.RequestID || manifest.AITraceID != request.AITraceID || manifest.ServiceTraceID != request.ServiceTraceID ||
		manifest.SpanID != request.SpanID || !manifest.StartedAt.Equal(request.StartedAt) || !manifest.Deadline.Equal(request.Deadline) || len(manifest.Artifacts) != len(privacyArtifactKinds) {
		return false
	}
	refs := derivePrivacyArtifactRefs(manifest.RunID, manifest.Marker)
	wantRefs := map[PrivacyArtifactKind]string{
		PrivacyArtifactKindAPISummary: refs.APISummaryRef, PrivacyArtifactKindApplicationLogProjection: refs.ApplicationLogRef,
		PrivacyArtifactKindCollectorCompositeProof: refs.CollectorArtifactRef, PrivacyArtifactKindChatFixtureReport: refs.ChatReportRef,
	}
	seenKinds := make(map[PrivacyArtifactKind]bool, len(manifest.Artifacts))
	seenRefs := map[string]bool{request.ManifestRef: true}
	for _, binding := range manifest.Artifacts {
		if !validPrivacyArtifactKind(binding.Kind) || seenKinds[binding.Kind] || seenRefs[binding.Ref] || binding.Ref != wantRefs[binding.Kind] ||
			!safePrivacyArtifactRef(binding.Ref) || !privacyArtifactDigest.MatchString(binding.SHA256) || binding.SizeBytes <= 0 || binding.SizeBytes > maximumPrivacyArtifactBytes {
			return false
		}
		seenKinds[binding.Kind], seenRefs[binding.Ref] = true, true
	}
	return len(seenKinds) == len(privacyArtifactKinds)
}

func privacyArtifactBindingForKind(manifest PrivacyArtifactManifest, kind PrivacyArtifactKind) (PrivacyArtifactBinding, bool) {
	for _, binding := range manifest.Artifacts {
		if binding.Kind == kind {
			return binding, true
		}
	}
	return PrivacyArtifactBinding{}, false
}

func validatePersistedPrivacyArtifact(payload []byte, kind PrivacyArtifactKind, manifest PrivacyArtifactManifest) error {
	var document privacyArtifactEnvelope
	if err := strictPrivacyArtifactJSON(payload, &document); err != nil || document.SchemaVersion != "1" || document.Kind != kind ||
		document.RunID != manifest.RunID || document.Marker != manifest.Marker || document.RequestID != manifest.RequestID ||
		document.AITraceID != manifest.AITraceID || document.ServiceTraceID != manifest.ServiceTraceID || document.SpanID != manifest.SpanID ||
		!document.StartedAt.Equal(manifest.StartedAt) || !document.Deadline.Equal(manifest.Deadline) {
		return errPrivacyArtifactStore
	}
	switch kind {
	case PrivacyArtifactKindAPISummary:
		if document.APISummary == nil || document.ApplicationLog != nil || document.Collector != nil || document.ChatReport != nil || !validPrivacyCategoryCounts(document.APISummary) {
			return errPrivacyArtifactStore
		}
	case PrivacyArtifactKindApplicationLogProjection:
		if document.APISummary != nil || document.ApplicationLog == nil || document.Collector != nil || document.ChatReport != nil || !validPrivacyApplicationProjection(*document.ApplicationLog, document) {
			return errPrivacyArtifactStore
		}
	case PrivacyArtifactKindCollectorCompositeProof:
		if document.APISummary != nil || document.ApplicationLog != nil || document.Collector == nil || document.ChatReport != nil || !validPrivacyCollectorProof(*document.Collector, document) {
			return errPrivacyArtifactStore
		}
	case PrivacyArtifactKindChatFixtureReport:
		if document.APISummary != nil || document.ApplicationLog != nil || document.Collector != nil || len(document.ChatReport) == 0 {
			return errPrivacyArtifactStore
		}
		report, err := decodePrivacyChatReport(document.ChatReport)
		if err != nil || !validPrivacyChatReport(report, document) {
			return errPrivacyArtifactStore
		}
	default:
		return errPrivacyArtifactStore
	}
	return nil
}

func decodePrivacyChatReport(payload []byte) (*SmokeReport, error) {
	var wire smokeReportJSON
	if err := strictPrivacyArtifactJSON(payload, &wire); err != nil {
		return nil, errPrivacyArtifactStore
	}
	// v3 是闭集安全契约。旧版或未知 wire 不能经由当前 builder 被静默重标为 v3，
	// 否则持久化制品会丢失原始版本事实并绕过显式升级边界。
	if wire.SchemaVersion != smokeReportSchemaVersion {
		return nil, errPrivacyArtifactStore
	}
	startedAt, err := time.Parse(time.RFC3339Nano, wire.StartedAt)
	if err != nil {
		return nil, errPrivacyArtifactStore
	}
	finishedAt, err := time.Parse(time.RFC3339Nano, wire.FinishedAt)
	if err != nil {
		return nil, errPrivacyArtifactStore
	}
	checks := make([]BackendCheckInput, 0, len(wire.Checks))
	for _, check := range wire.Checks {
		checks = append(checks, BackendCheckInput{
			Backend: check.Backend, Status: check.Status, Duration: time.Duration(check.DurationMS) * time.Millisecond,
			FailureStage: check.FailureStage, ErrorClass: check.ErrorClass, Evidence: check.Evidence,
		})
	}
	report, err := BuildSmokeReport(SmokeReportInput{
		RunID: wire.RunID, Marker: wire.Marker, Profile: wire.Profile, Scenario: wire.Scenario,
		RequestID: wire.RequestID, AITraceID: wire.AITraceID, StartedAt: startedAt, Deadline: finishedAt,
		FinishedAt: finishedAt, Versions: wire.Versions, Checks: checks,
		Cleanup: SmokeCleanupInput{Status: wire.Cleanup.Status, ResidualResources: wire.Cleanup.ResidualResources,
			TemporaryCredentials: wire.Cleanup.TemporaryCredentials, TemporaryData: wire.Cleanup.TemporaryData},
	})
	if err != nil || report.status != wire.Status {
		return nil, errPrivacyArtifactStore
	}
	return report, nil
}

func clonePrivacyApplicationProjection(input PrivacyApplicationLogProjection) PrivacyApplicationLogProjection {
	attributes := make(map[string]any, len(input.Attributes))
	for key, value := range input.Attributes {
		attributes[key] = value
	}
	input.Attributes = attributes
	return input
}

func clonePrivacyArtifactManifest(input PrivacyArtifactManifest) PrivacyArtifactManifest {
	input.Artifacts = append([]PrivacyArtifactBinding(nil), input.Artifacts...)
	return input
}

var _ PrivacyFixtureArtifactWriter = (*PrivacyArtifactStore)(nil)
