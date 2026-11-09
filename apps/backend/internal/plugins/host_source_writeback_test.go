package plugins

import (
	"context"
	"errors"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/kandev/kandev/internal/db"
	"github.com/kandev/kandev/internal/plugins/manifest"
	"github.com/kandev/kandev/internal/plugins/state"
	pluginstore "github.com/kandev/kandev/internal/plugins/store"
	taskmodels "github.com/kandev/kandev/internal/task/models"
	"github.com/kdlbs/kandev/pluginsdk"
	_ "github.com/mattn/go-sqlite3"
)

type sourceWritebackTaskData struct {
	taskDataSource
	workspaces []*taskmodels.Workspace
}

func (s sourceWritebackTaskData) ListWorkspaces(context.Context) ([]*taskmodels.Workspace, error) {
	return s.workspaces, nil
}

type sourceIssueControllerFake struct {
	capabilities *pluginsdk.SourceIssueCapabilities
	readErr      error
	readCalls    int
	apply        func(func() error) (string, error)
	sends        int
	commentIDs   []string
	settle       func(SourceIssueCommentSettlement) (string, error)
	settles      int
}

func (f *sourceIssueControllerFake) SettleComment(_ context.Context, input SourceIssueCommentSettlement) (string, error) {
	if f.settle == nil {
		return "", &SourceIssueError{Kind: SourceIssueErrorUnsupported}
	}
	f.settles++
	return f.settle(input)
}

func (f *sourceIssueControllerFake) GetCapabilities(context.Context, string, string) (*pluginsdk.SourceIssueCapabilities, error) {
	f.readCalls++
	if f.capabilities == nil {
		return nil, f.readErr
	}
	copy := *f.capabilities
	copy.Transitions = append([]pluginsdk.SourceIssueTransition(nil), f.capabilities.Transitions...)
	return &copy, f.readErr
}

func (f *sourceIssueControllerFake) ApplyWriteback(_ context.Context, input SourceIssueWritebackInput, beforeSend func() error) (string, error) {
	f.commentIDs = append(f.commentIDs, input.CommentID)
	if f.apply != nil {
		return f.apply(beforeSend)
	}
	if err := beforeSend(); err != nil {
		return "", err
	}
	f.sends++
	return "provider-comment-1", nil
}

func TestSourceWritebackReceipts(t *testing.T) {
	ctx := context.Background()
	connection, err := sqlx.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = connection.Close() })
	commandStore, err := state.NewCommandStore(db.NewPool(connection, connection))
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	record := &pluginstore.Record{
		Manifest: manifest.Manifest{ID: "source-writer", Capabilities: manifest.Capabilities{
			APIRead: []string{"source_issues"}, APIWrite: []string{"source_issues"},
		}},
		InstallationID: "source-writer-installation",
	}
	registry := NewRegistry()
	registry.Add(record)
	svc := NewService(pluginstore.NewFSStore(t.TempDir()), registry, nil, testLogger(t))
	if err := svc.SetPluginsDir(t.TempDir()); err != nil {
		t.Fatalf("SetPluginsDir: %v", err)
	}
	svc.SetExactCommandStore(commandStore)
	t.Cleanup(func() { _ = svc.Close() })
	const workspaceID = "source-writeback-workspace"
	if _, err := svc.approvalGrant(record.InstallationID, workspaceID, 1, ManifestCapabilityDigest(record.Manifest),
		[]string{"host.v2.read:source_issues", "host.v2.write:source_issues"}, "human", "grant", "source-writeback-approval"); err != nil {
		t.Fatalf("grant source issue capabilities: %v", err)
	}
	controller := &sourceIssueControllerFake{capabilities: &pluginsdk.SourceIssueCapabilities{
		WorkspaceID: workspaceID, TaskID: "task-source-1", TaskResourceVersion: "task-v1",
		Provider: "jira", SourceID: "jira-issue-42", Identifier: "ENG-42",
		URL: "https://jira.example/browse/ENG-42", Title: "Coordinate release",
		StatusID: "status-open", StatusName: "Open", ResourceVersion: "source-v1",
		Transitions: []pluginsdk.SourceIssueTransition{{TargetID: "transition-review", TargetName: "In Review"}},
	}}
	host := svc.hostForPlugin(record.ID).(*pluginHost)
	host.commandStore = commandStore
	host.sourceIssueController = controller
	host.taskData = sourceWritebackTaskData{workspaces: []*taskmodels.Workspace{{ID: workspaceID}}}
	manager, ok := pluginsdk.HostSourceIssueWriteback(host)
	if !ok {
		t.Fatal("plugin Host does not expose source issue writeback")
	}

	observation, capabilities, readReceipt, err := manager.GetCapabilities(ctx, pluginsdk.SourceIssueCapabilitiesQuery{
		RequestID: "source-read-request", WorkspaceID: workspaceID, TaskID: "task-source-1",
	})
	if err != nil || observation == nil || observation.Status != pluginsdk.CommandApplied || capabilities == nil ||
		capabilities.SourceID != "jira-issue-42" || readReceipt == nil || readReceipt.InstallationID != record.InstallationID {
		t.Fatalf("source capabilities = result:%+v item:%+v receipt:%+v err:%v", observation, capabilities, readReceipt, err)
	}

	comment := pluginsdk.SourceIssueWritebackCommand{
		RequestID: "source-comment-request", WorkspaceID: workspaceID, TaskID: "task-source-1",
		IdempotencyKey: "source-comment-1", ExpectedTaskResourceVersion: "task-v1",
		ExpectedSourceResourceVersion: "source-v1", ApprovalRevision: 1,
		ManifestDigest: ManifestCapabilityDigest(record.Manifest), Body: "Release is ready for review.",
	}
	result, receipt, err := manager.Comment(ctx, comment)
	if err != nil || result == nil || result.Status != pluginsdk.CommandApplied || receipt == nil ||
		receipt.InstallationID != record.InstallationID || receipt.TaskID != comment.TaskID ||
		receipt.Provider != "jira" || receipt.SourceID != "jira-issue-42" || receipt.Operation != "comment" ||
		receipt.ProviderReceiptID != "provider-comment-1" || receipt.State != "completed" || controller.sends != 1 {
		t.Fatalf("source comment = result:%+v receipt:%+v sends:%d err:%v", result, receipt, controller.sends, err)
	}
	replayed, replayReceipt, err := manager.Comment(ctx, comment)
	if err != nil || replayed == nil || replayed.Status != pluginsdk.CommandAlreadyApplied || replayReceipt == nil ||
		replayReceipt.OperationID != receipt.OperationID || controller.sends != 1 {
		t.Fatalf("source comment replay = result:%+v receipt:%+v sends:%d err:%v", replayed, replayReceipt, controller.sends, err)
	}

	uncertain := comment
	uncertain.RequestID = "source-comment-timeout"
	uncertain.IdempotencyKey = "source-comment-timeout-1"
	controller.apply = func(beforeSend func() error) (string, error) {
		if err := beforeSend(); err != nil {
			return "", err
		}
		controller.sends++
		return "", errors.New("connection closed after provider accepted the comment")
	}
	unknown, unknownReceipt, err := manager.Comment(ctx, uncertain)
	if err != nil || unknown == nil || unknown.Status != pluginsdk.CommandUncertain || unknownReceipt == nil ||
		unknownReceipt.State != "uncertain" || unknownReceipt.Reason != "provider_outcome_unknown" || controller.sends != 2 {
		t.Fatalf("uncertain source comment = result:%+v receipt:%+v sends:%d err:%v", unknown, unknownReceipt, controller.sends, err)
	}
	unknownReplay, unknownReplayReceipt, err := manager.Comment(ctx, uncertain)
	if err != nil || unknownReplay == nil || unknownReplay.Status != pluginsdk.CommandUncertain || unknownReplayReceipt == nil ||
		unknownReplayReceipt.OperationID != unknownReceipt.OperationID || controller.sends != 2 {
		t.Fatalf("uncertain source comment replay = result:%+v receipt:%+v sends:%d err:%v", unknownReplay, unknownReplayReceipt, controller.sends, err)
	}
	// The comment was sent under its operation id, so a provider that can look
	// that id up settles the unknown send instead of holding it forever.
	if sentID := controller.commentIDs[len(controller.commentIDs)-1]; sentID != unknownReceipt.OperationID {
		t.Fatalf("comment sent under id %q, want operation id %q", sentID, unknownReceipt.OperationID)
	}
	controller.settle = func(input SourceIssueCommentSettlement) (string, error) {
		if input.CommentID != unknownReceipt.OperationID || input.SourceID != "jira-issue-42" || input.Body != uncertain.Body {
			t.Fatalf("settlement input = %+v", input)
		}
		return input.CommentID, nil
	}
	settled, settledReceipt, err := manager.Comment(ctx, uncertain)
	if err != nil || settled == nil || settled.Status != pluginsdk.CommandApplied || settledReceipt == nil ||
		settledReceipt.State != "completed" || settledReceipt.ProviderReceiptID != unknownReceipt.OperationID ||
		controller.settles != 1 || controller.sends != 2 {
		t.Fatalf("settled source comment = result:%+v receipt:%+v settles:%d sends:%d err:%v", settled, settledReceipt, controller.settles, controller.sends, err)
	}
	settledReplay, _, err := manager.Comment(ctx, uncertain)
	if err != nil || settledReplay == nil || settledReplay.Status != pluginsdk.CommandAlreadyApplied || controller.settles != 1 {
		t.Fatalf("settled source comment replay = result:%+v settles:%d err:%v", settledReplay, controller.settles, err)
	}
	controller.settle = nil

	stale := comment
	stale.RequestID = "source-comment-stale"
	stale.IdempotencyKey = "source-comment-stale-1"
	controller.apply = func(func() error) (string, error) {
		return "", &SourceIssueError{Kind: SourceIssueErrorStale}
	}
	staleResult, staleReceipt, err := manager.Comment(ctx, stale)
	if err != nil || staleResult == nil || staleResult.Status != pluginsdk.CommandConflict || staleReceipt == nil ||
		staleReceipt.Reason != "source_link_changed" || controller.sends != 2 {
		t.Fatalf("stale source link = result:%+v receipt:%+v sends:%d err:%v", staleResult, staleReceipt, controller.sends, err)
	}

	limited := comment
	limited.RequestID = "source-comment-limited"
	limited.IdempotencyKey = "source-comment-limited-1"
	controller.apply = func(beforeSend func() error) (string, error) {
		if err := beforeSend(); err != nil {
			return "", err
		}
		controller.sends++
		return "", &SourceIssueError{Kind: SourceIssueErrorRateLimited}
	}
	limitedResult, limitedReceipt, err := manager.Comment(ctx, limited)
	if err != nil || limitedResult == nil || limitedResult.Status != pluginsdk.CommandRateLimited || limitedReceipt == nil ||
		limitedReceipt.Reason != "provider_rate_limited" || controller.sends != 3 {
		t.Fatalf("rate-limited source write = result:%+v receipt:%+v sends:%d err:%v", limitedResult, limitedReceipt, controller.sends, err)
	}
	limitedReplay, _, err := manager.Comment(ctx, limited)
	if err != nil || limitedReplay == nil || limitedReplay.Status != pluginsdk.CommandRateLimited || controller.sends != 3 {
		t.Fatalf("rate-limited source write replay = result:%+v sends:%d err:%v", limitedReplay, controller.sends, err)
	}

	restartCommand := comment
	restartCommand.RequestID = "source-comment-restart"
	restartCommand.IdempotencyKey = "source-comment-restart-1"
	restartDigest, err := sourceIssueWritebackDigest("comment", restartCommand)
	if err != nil {
		t.Fatal(err)
	}
	commandRecord, _, err := commandStore.Admit(ctx, state.CommandIntent{
		InstallationID: record.InstallationID, WorkspaceID: workspaceID, RequestID: restartCommand.RequestID,
		IdempotencyKey: restartCommand.IdempotencyKey, Method: exactSourceIssueCommentMethod,
		CapabilityID: "host.v2.write:source_issues", PayloadDigest: restartDigest, TargetID: restartCommand.TaskID,
		ExpectedResourceVersion: restartCommand.ExpectedTaskResourceVersion + "/" + restartCommand.ExpectedSourceResourceVersion,
		ApprovalRevision:        restartCommand.ApprovalRevision, ManifestDigest: restartCommand.ManifestDigest,
	})
	if err != nil {
		t.Fatalf("admit pre-restart source write: %v", err)
	}
	prepared, _, err := commandStore.PrepareSourceWriteback(ctx, state.SourceWritebackIntent{
		OperationID: commandRecord.Intent.OperationID, InstallationID: record.InstallationID,
		WorkspaceID: workspaceID, TaskID: restartCommand.TaskID, Provider: "jira", SourceID: "jira-issue-42",
		Operation: "comment", ExpectedSourceVersion: restartCommand.ExpectedSourceResourceVersion, PayloadDigest: restartDigest,
	})
	if err != nil {
		t.Fatalf("prepare pre-restart source write: %v", err)
	}
	if _, started, err := commandStore.StartSourceWriteback(ctx, prepared.OperationID); err != nil || !started {
		t.Fatalf("start pre-restart source write = started %v, err %v", started, err)
	}
	reopenedStore, err := state.NewCommandStore(db.NewPool(connection, connection))
	if err != nil {
		t.Fatalf("reopen source writeback store: %v", err)
	}
	host.commandStore = reopenedStore
	controller.capabilities = nil
	controller.readErr = &SourceIssueError{Kind: SourceIssueErrorNotFound}
	readsBeforeReplay := controller.readCalls
	uncertainAfterRestart, restartReceipt, err := manager.Comment(ctx, restartCommand)
	if err != nil || uncertainAfterRestart == nil || uncertainAfterRestart.Status != pluginsdk.CommandUncertain ||
		restartReceipt == nil || restartReceipt.State != "uncertain" || controller.readCalls != readsBeforeReplay || controller.sends != 3 {
		t.Fatalf("recovered in-flight source write = result:%+v receipt:%+v reads:%d sends:%d err:%v",
			uncertainAfterRestart, restartReceipt, controller.readCalls, controller.sends, err)
	}

	// A plugin upgrade bumps the approval revision while a write is in flight.
	// The exact retry under the old revision gets the stored outcome, not a
	// denial the plugin would read as "never happened" and re-send.
	inFlight := comment
	inFlight.RequestID = "source-comment-upgrade"
	inFlight.IdempotencyKey = "source-comment-upgrade-1"
	inFlightDigest, err := sourceIssueWritebackDigest("comment", inFlight)
	if err != nil {
		t.Fatal(err)
	}
	inFlightRecord, _, err := reopenedStore.Admit(ctx, state.CommandIntent{
		InstallationID: record.InstallationID, WorkspaceID: workspaceID, RequestID: inFlight.RequestID,
		IdempotencyKey: inFlight.IdempotencyKey, Method: exactSourceIssueCommentMethod,
		CapabilityID: "host.v2.write:source_issues", PayloadDigest: inFlightDigest, TargetID: inFlight.TaskID,
		ExpectedResourceVersion: inFlight.ExpectedTaskResourceVersion + "/" + inFlight.ExpectedSourceResourceVersion,
		ApprovalRevision:        inFlight.ApprovalRevision, ManifestDigest: inFlight.ManifestDigest,
	})
	if err != nil {
		t.Fatalf("admit in-flight source write: %v", err)
	}
	inFlightPrepared, _, err := reopenedStore.PrepareSourceWriteback(ctx, state.SourceWritebackIntent{
		OperationID: inFlightRecord.Intent.OperationID, InstallationID: record.InstallationID,
		WorkspaceID: workspaceID, TaskID: inFlight.TaskID, Provider: "jira", SourceID: "jira-issue-42",
		Operation: "comment", ExpectedSourceVersion: inFlight.ExpectedSourceResourceVersion, PayloadDigest: inFlightDigest,
	})
	if err != nil {
		t.Fatalf("prepare in-flight source write: %v", err)
	}
	if _, started, err := reopenedStore.StartSourceWriteback(ctx, inFlightPrepared.OperationID); err != nil || !started {
		t.Fatalf("start in-flight source write = started %v, err %v", started, err)
	}
	if _, err := svc.approvalGrant(record.InstallationID, workspaceID, 2, ManifestCapabilityDigest(record.Manifest),
		[]string{"host.v2.read:source_issues", "host.v2.write:source_issues"}, "human", "grant", "source-writeback-approval-2"); err != nil {
		t.Fatalf("re-grant source issue capabilities: %v", err)
	}
	upgraded, upgradedReceipt, err := manager.Comment(ctx, inFlight)
	if err != nil || upgraded == nil || upgraded.Status != pluginsdk.CommandUncertain || upgradedReceipt == nil ||
		upgradedReceipt.OperationID != inFlightRecord.Intent.OperationID || controller.sends != 3 {
		t.Fatalf("in-flight write after upgrade = result:%+v receipt:%+v sends:%d err:%v", upgraded, upgradedReceipt, controller.sends, err)
	}
	fresh := inFlight
	fresh.IdempotencyKey = "source-comment-upgrade-new"
	if denied, _, err := manager.Comment(ctx, fresh); err != nil || denied == nil || denied.Status != pluginsdk.CommandDenied || controller.sends != 3 {
		t.Fatalf("new write under a stale revision = result:%+v sends:%d err:%v", denied, controller.sends, err)
	}
}
