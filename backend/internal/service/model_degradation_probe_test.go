package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/modeltrace"
	"github.com/stretchr/testify/require"
)

type modelDegradationProbeRepo struct {
	AccountRepository
	mu       sync.Mutex
	accounts map[int64]*Account
}

func (r *modelDegradationProbeRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account, ok := r.accounts[id]
	if !ok {
		return nil, errors.New("not found")
	}
	clone := *account
	clone.Extra = roundTripExtra(account.Extra)
	return &clone, nil
}

func (r *modelDegradationProbeRepo) GetByIDs(ctx context.Context, ids []int64) ([]*Account, error) {
	out := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if account, err := r.GetByID(ctx, id); err == nil {
			out = append(out, account)
		}
	}
	return out, nil
}

func (r *modelDegradationProbeRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	for key, value := range roundTripExtra(updates) {
		account.Extra[key] = value
	}
	return nil
}

func (r *modelDegradationProbeRepo) ListModelDegradationProbeCandidates(context.Context) ([]ModelDegradationProbeCandidateRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rows []ModelDegradationProbeCandidateRow
	for id, account := range r.accounts {
		if _, ok := account.Extra[ModelDegradationProbeIntervalExtraKey]; ok {
			rows = append(rows, ModelDegradationProbeCandidateRow{ID: id, Extra: roundTripExtra(account.Extra)})
		}
	}
	return rows, nil
}

func (r *modelDegradationProbeRepo) snapshot(t *testing.T, id int64) *ModelDegradationProbeSnapshot {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := DecodeModelDegradationProbeSnapshot(r.accounts[id].Extra)
	require.NotNil(t, snapshot)
	return snapshot
}

// roundTripExtra 模拟 JSONB 落库后的值形态（数字变 float64、结构体变 map）。
func roundTripExtra(extra map[string]any) map[string]any {
	raw, _ := json.Marshal(extra)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

type modelDegradationProbeTesterStub struct {
	mu      sync.Mutex
	replies []*ScheduledTestResult
	prompts []string
	models  []string
}

func (s *modelDegradationProbeTesterStub) RunTestBackgroundWithPrompt(_ context.Context, _ int64, modelID, prompt string) (*ScheduledTestResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, prompt)
	s.models = append(s.models, modelID)
	reply := s.replies[0]
	if len(s.replies) > 1 {
		s.replies = s.replies[1:]
	}
	return reply, nil
}

type modelDegradationProbePauserStub struct {
	pauses []string
	untils []time.Time
	repo   *modelDegradationProbeRepo
}

func (p *modelDegradationProbePauserStub) PauseAccountTempUnschedulable(_ context.Context, account *Account, until time.Time, reason, _ string) error {
	p.pauses = append(p.pauses, reason)
	p.untils = append(p.untils, until)
	p.repo.mu.Lock()
	stored := p.repo.accounts[account.ID]
	stored.TempUnschedulableUntil = &until
	stored.TempUnschedulableReason = reason
	p.repo.mu.Unlock()
	return nil
}

type modelDegradationReferenceTexts struct {
	astra string
	other string
}

func loadModelDegradationReferenceTexts(t *testing.T) modelDegradationReferenceTexts {
	t.Helper()
	raw, err := os.ReadFile("../pkg/modeltrace/testdata/parity.json")
	require.NoError(t, err)
	var fixture struct {
		Single []struct {
			Text string `json:"text"`
			Top  string `json:"top"`
		} `json:"single"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	var texts modelDegradationReferenceTexts
	for _, item := range fixture.Single {
		if item.Top == ModelDegradationProbeExpectedModel && texts.astra == "" {
			texts.astra = item.Text
		}
		if item.Top == "gpt-6-luna" && texts.other == "" {
			texts.other = item.Text
		}
	}
	require.NotEmpty(t, texts.astra)
	require.NotEmpty(t, texts.other)
	return texts
}

func newModelDegradationProbeTestService(repo *modelDegradationProbeRepo, tester *modelDegradationProbeTesterStub, now *time.Time) (*ModelDegradationProbeService, *modelDegradationProbePauserStub) {
	pauser := &modelDegradationProbePauserStub{repo: repo}
	svc := NewModelDegradationProbeService(repo, nil, nil)
	svc.tester = tester
	svc.pauser = pauser
	svc.challenge = func() modeltrace.Challenge {
		return modeltrace.Challenge{Prompt: "probe prompt", ExpectedCount: 218}
	}
	svc.now = func() time.Time { return *now }
	return svc, pauser
}

func successReply(text string) *ScheduledTestResult {
	return &ScheduledTestResult{Status: "success", ResponseText: text, LatencyMs: 1200}
}

func TestModelDegradationProbeNormalAndDegraded(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbeIntervalExtraKey: 30,
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply(texts.astra), successReply(texts.other)}}
	svc, pauser := newModelDegradationProbeTestService(repo, tester, &now)

	require.NoError(t, svc.RunDue(context.Background()))
	snapshot := repo.snapshot(t, 1)
	require.Equal(t, ModelDegradationProbeStatusNormal, snapshot.Status)
	require.Equal(t, ModelDegradationProbeExpectedModel, snapshot.TopModel)
	require.Equal(t, now.Add(30*time.Minute), snapshot.NextProbeAt)
	require.Equal(t, []string{"probe prompt"}, tester.prompts)
	require.Equal(t, []string{ModelDegradationProbeExpectedModel}, tester.models)

	// 未到期不检测。
	now = now.Add(10 * time.Minute)
	require.NoError(t, svc.RunDue(context.Background()))
	require.Len(t, tester.prompts, 1)

	now = now.Add(20 * time.Minute)
	require.NoError(t, svc.RunDue(context.Background()))
	snapshot = repo.snapshot(t, 1)
	require.Equal(t, ModelDegradationProbeStatusDegraded, snapshot.Status)
	require.Equal(t, "gpt-6-luna", snapshot.TopModel)
	require.Equal(t, 1, snapshot.ConsecutiveDegraded)
	require.Nil(t, snapshot.PausedUntil, "pause threshold 0 只展示")
	require.Empty(t, pauser.pauses)
}

func TestModelDegradationProbePausesForConfiguredDurationAndCoolsDown(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	now := start
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		7: {ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbeIntervalExtraKey:        10,
			ModelDegradationProbePauseThresholdExtraKey:  2,
			ModelDegradationProbePauseMinutesExtraKey:    90,
			ModelDegradationProbeCooldownMinutesExtraKey: 120,
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{
		successReply(texts.other),
		successReply(texts.other),
		successReply(texts.astra),
	}}
	svc, pauser := newModelDegradationProbeTestService(repo, tester, &now)
	run := func() { require.NoError(t, svc.RunDue(context.Background())) }

	// 第一次疑似降智：未达阈值不暂停，但进入 120 分钟停检。
	run()
	snapshot := repo.snapshot(t, 7)
	require.Equal(t, ModelDegradationProbeStatusDegraded, snapshot.Status)
	require.Nil(t, snapshot.PausedUntil)
	require.Equal(t, start.Add(120*time.Minute), *snapshot.ProbeResumeAt)
	require.Equal(t, start.Add(120*time.Minute), snapshot.NextProbeAt)

	now = start.Add(119 * time.Minute)
	run()
	require.Len(t, tester.prompts, 1, "停检期间不自动检测")

	// 第二次疑似降智达到阈值：暂停 90 分钟。
	now = start.Add(120 * time.Minute)
	run()
	snapshot = repo.snapshot(t, 7)
	require.Equal(t, 2, snapshot.ConsecutiveDegraded)
	require.Equal(t, []time.Time{now.Add(90 * time.Minute)}, pauser.untils)
	require.Equal(t, now.Add(90*time.Minute), *snapshot.PausedUntil)
	require.True(t, strings.Contains(pauser.pauses[0], "gpt-6-luna"))

	// 恢复正常：计数清零，按检测间隔排下一次，不主动解除暂停。
	now = now.Add(120 * time.Minute)
	run()
	snapshot = repo.snapshot(t, 7)
	require.Equal(t, ModelDegradationProbeStatusNormal, snapshot.Status)
	require.Zero(t, snapshot.ConsecutiveDegraded)
	require.Nil(t, snapshot.ProbeResumeAt)
	require.Nil(t, snapshot.PausedUntil, "暂停已过期不再展示")
	require.Equal(t, now.Add(10*time.Minute), snapshot.NextProbeAt)
	require.Len(t, pauser.pauses, 1)
}

func TestModelDegradationProbeFailureKeepsCountWithoutPausing(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		3: {
			ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
			Extra: map[string]any{
				ModelDegradationProbeIntervalExtraKey:       5,
				ModelDegradationProbePauseThresholdExtraKey: 1,
				ModelDegradationProbeExtraKey: map[string]any{
					"status": "degraded", "consecutive_degraded": 1,
					"last_attempt_at": now.Add(-time.Hour), "next_probe_at": now.Add(-time.Minute),
				},
			},
		},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{{Status: "failed", ErrorMessage: "API returned 502"}}}
	svc, pauser := newModelDegradationProbeTestService(repo, tester, &now)

	require.NoError(t, svc.RunDue(context.Background()))
	snapshot := repo.snapshot(t, 3)
	require.Equal(t, ModelDegradationProbeStatusFailed, snapshot.Status)
	require.Equal(t, "API returned 502", snapshot.LastError)
	require.Equal(t, 1, snapshot.ConsecutiveDegraded)
	require.Empty(t, pauser.pauses, "检测失败不触发暂停")
}

func TestModelDegradationProbeSkipsDisabledAndUnsupportedAccounts(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbeIntervalExtraKey: 0,
		}},
		2: {ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbeIntervalExtraKey: 5,
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply("")}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

	require.NoError(t, svc.RunDue(context.Background()))
	require.Empty(t, tester.prompts)

	_, err := svc.ProbeAccount(context.Background(), 2)
	require.ErrorIs(t, err, ErrModelDegradationProbeAccountInvalid)
}

func TestModelDegradationProbeShortAnswerIsFailure(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply("抱歉，我不能这样做。")}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ModelDegradationProbeStatusFailed, snapshot.Status)
	require.Equal(t, "valid numbers 0/120", snapshot.LastError)
	require.True(t, snapshot.NextProbeAt.IsZero(), "未配置间隔时手动检测不排下一次")
}

func TestModelDegradationProbeDueFollowsCurrentSettings(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	account := func(status string, interval, cooldown int) *Account {
		return &Account{Extra: roundTripExtra(map[string]any{
			ModelDegradationProbeIntervalExtraKey:        interval,
			ModelDegradationProbeCooldownMinutesExtraKey: cooldown,
			ModelDegradationProbeExtraKey: ModelDegradationProbeSnapshot{
				Status:        status,
				LastAttemptAt: now.Add(-20 * time.Minute),
				NextProbeAt:   now.Add(23 * time.Hour),
			},
		})}
	}
	require.True(t, modelDegradationProbeDue(account(ModelDegradationProbeStatusNormal, 15, 0), now), "调小间隔立即生效")
	require.False(t, modelDegradationProbeDue(account(ModelDegradationProbeStatusNormal, 30, 0), now))
	require.False(t, modelDegradationProbeDue(account(ModelDegradationProbeStatusNormal, 0, 0), now))
	require.False(t, modelDegradationProbeDue(account(ModelDegradationProbeStatusDegraded, 15, 60), now), "降智后按停检时长")
	require.True(t, modelDegradationProbeDue(account(ModelDegradationProbeStatusDegraded, 15, 0), now))
	require.True(t, modelDegradationProbeDue(account(ModelDegradationProbeStatusFailed, 15, 60), now), "停检只针对疑似降智")
}

func TestValidateModelDegradationProbeExtra(t *testing.T) {
	extra := map[string]any{
		ModelDegradationProbeIntervalExtraKey:       float64(60),
		ModelDegradationProbePauseThresholdExtraKey: "3",
	}
	require.NoError(t, ValidateModelDegradationProbeExtra(extra))
	require.Equal(t, 60, extra[ModelDegradationProbeIntervalExtraKey])
	require.Equal(t, 3, extra[ModelDegradationProbePauseThresholdExtraKey])

	require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbeIntervalExtraKey: -1}))
	require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbeIntervalExtraKey: 1.5}))
	require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbePauseThresholdExtraKey: 101}))
	require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbePauseMinutesExtraKey: 10081}))
	require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbeCooldownMinutesExtraKey: -1}))
	require.NoError(t, ValidateModelDegradationProbeExtra(nil))
}

func TestUpdateAccountPreservesModelDegradationSnapshotAndRejectsInjection(t *testing.T) {
	accountID := int64(210)
	stored := map[string]any{"status": "degraded", "top_model": "gpt-6-luna", "consecutive_degraded": 2}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {
			ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
			Extra: map[string]any{ModelDegradationProbeExtraKey: stored},
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{
			ModelDegradationProbeIntervalExtraKey:       float64(45),
			ModelDegradationProbePauseThresholdExtraKey: float64(3),
			ModelDegradationProbeExtraKey:               map[string]any{"status": "normal"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, stored, updated.Extra[ModelDegradationProbeExtraKey])
	require.Equal(t, 45, updated.Extra[ModelDegradationProbeIntervalExtraKey])
	require.Equal(t, 3, updated.Extra[ModelDegradationProbePauseThresholdExtraKey])

	_, err = svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{ModelDegradationProbeIntervalExtraKey: float64(-5)},
	})
	require.Error(t, err)
}

func TestCreateAccountDropsInjectedModelDegradationSnapshot(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	created, err := (&adminServiceImpl{accountRepo: repo}).CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "openai",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeOAuth,
		Credentials:          map[string]any{"access_token": "token"},
		SkipDefaultGroupBind: true,
		Extra: map[string]any{
			ModelDegradationProbeIntervalExtraKey: float64(30),
			ModelDegradationProbeExtraKey:         map[string]any{"status": "normal"},
		},
	})
	require.NoError(t, err)
	require.NotContains(t, created.Extra, ModelDegradationProbeExtraKey)
	require.Equal(t, 30, created.Extra[ModelDegradationProbeIntervalExtraKey])
}

func TestModelDegradationProbeBatchRunsInBackground(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{}},
		2: {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Extra: map[string]any{}},
		3: {ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply(texts.astra)}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

	result, err := svc.StartBatchProbe(context.Background(), []int64{1, 2, 3, 99})
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{1, 2}, result.Queued)
	require.ElementsMatch(t, []ModelDegradationProbeBatchSkip{
		{AccountID: 3, Reason: "unsupported"},
		{AccountID: 99, Reason: "not_found"},
	}, result.Skipped)

	var items []ModelDegradationProbeSnapshotItem
	require.Eventually(t, func() bool {
		items, err = svc.GetSnapshots(context.Background(), []int64{1, 2, 3})
		require.NoError(t, err)
		done := 0
		for _, item := range items {
			if item.Snapshot != nil {
				done++
			}
		}
		return done == 2
	}, 5*time.Second, 10*time.Millisecond)
	svc.Stop()
	require.Len(t, items, 3)
	for _, item := range items {
		if item.AccountID == 3 {
			require.Nil(t, item.Snapshot)
			continue
		}
		require.NotNil(t, item.Snapshot)
		require.Equal(t, ModelDegradationProbeStatusNormal, item.Snapshot.Status)
	}

	_, err = svc.StartBatchProbe(context.Background(), []int64{1})
	require.ErrorIs(t, err, ErrModelDegradationProbeUnavailable, "停止后不再接受批量检测")
}

func TestModelDegradationManualProbeAppliesSamePolicyAsScheduled(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		5: {ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbeIntervalExtraKey:        5,
			ModelDegradationProbePauseThresholdExtraKey:  1,
			ModelDegradationProbePauseMinutesExtraKey:    60,
			ModelDegradationProbeCooldownMinutesExtraKey: 50,
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply(texts.other)}}
	svc, pauser := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, ModelDegradationProbeStatusDegraded, snapshot.Status)
	require.Equal(t, []time.Time{now.Add(60 * time.Minute)}, pauser.untils, "手动检测同样按阈值暂停调度")
	require.Equal(t, now.Add(50*time.Minute), *snapshot.ProbeResumeAt, "手动检测同样进入停检")
	require.Equal(t, now.Add(50*time.Minute), snapshot.NextProbeAt)

	// 手动检测之后自动调度也遵守停检：50 分钟内不再自动检测。
	now = now.Add(49 * time.Minute)
	require.NoError(t, svc.RunDue(context.Background()))
	require.Len(t, tester.prompts, 1)

	// 停检期间仍可手动检测；依旧降智时暂停从本次检测起重新计算 60 分钟。
	snapshot, err = svc.ProbeAccount(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, 2, snapshot.ConsecutiveDegraded)
	require.Equal(t, now.Add(60*time.Minute), pauser.untils[1])
}
