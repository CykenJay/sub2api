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

// modelDegradationProbeTesterStub 按顺序返回 replies，用完后重复最后一条；cycle 时循环返回。
// 同一轮的两条样本并发请求，取到回复的先后不固定，测试只依赖同一轮拿到的回复集合。
type modelDegradationProbeTesterStub struct {
	mu      sync.Mutex
	replies []*ScheduledTestResult
	cycle   bool
	next    int
	prompts []string
	models  []string
}

func (s *modelDegradationProbeTesterStub) RunTestBackgroundWithPrompt(_ context.Context, _ int64, modelID, prompt string) (*ScheduledTestResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, prompt)
	s.models = append(s.models, modelID)
	index := s.next
	if s.cycle {
		index %= len(s.replies)
	} else if index >= len(s.replies) {
		index = len(s.replies) - 1
	}
	s.next++
	return s.replies[index], nil
}

type modelDegradationProbePauserStub struct {
	pauses  []string
	untils  []time.Time
	resumes []int64
	repo    *modelDegradationProbeRepo
}

func (p *modelDegradationProbePauserStub) ResumeAccountFromTempUnschedulable(_ context.Context, accountID int64) error {
	p.resumes = append(p.resumes, accountID)
	p.repo.mu.Lock()
	stored := p.repo.accounts[accountID]
	stored.TempUnschedulableUntil = nil
	stored.TempUnschedulableReason = ""
	p.repo.mu.Unlock()
	return nil
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
	// lowConfidenceAstra 是 gpt-6-astra 的真实回答，但指纹第一候选置信度不足 98%。
	lowConfidenceAstra string
}

func loadModelDegradationReferenceTexts(t *testing.T) modelDegradationReferenceTexts {
	t.Helper()
	raw, err := os.ReadFile("testdata/model_degradation_reference.json")
	require.NoError(t, err)
	var fixture struct {
		Astra              string `json:"astra"`
		Other              string `json:"other"`
		LowConfidenceAstra string `json:"low_confidence_astra"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	texts := modelDegradationReferenceTexts{astra: fixture.Astra, other: fixture.Other, lowConfidenceAstra: fixture.LowConfidenceAstra}
	require.NotEmpty(t, texts.astra)
	require.NotEmpty(t, texts.other)
	require.NotEmpty(t, texts.lowConfidenceAstra)
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
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{
		successReply(texts.astra), successReply(texts.astra),
		successReply(texts.other),
	}}
	svc, pauser := newModelDegradationProbeTestService(repo, tester, &now)

	require.NoError(t, svc.RunDue(context.Background()))
	snapshot := repo.snapshot(t, 1)
	require.Equal(t, ModelDegradationProbeStatusNormal, snapshot.Status)
	require.Equal(t, ModelDegradationProbeDefaultModel, snapshot.TopModel)
	require.Equal(t, now.Add(30*time.Minute), snapshot.NextProbeAt)
	require.Equal(t, []string{"probe prompt", "probe prompt"}, tester.prompts, "每次检测并发采集两条样本")
	require.Equal(t, []string{ModelDegradationProbeDefaultModel, ModelDegradationProbeDefaultModel}, tester.models)
	require.Equal(t, ModelDegradationProbeSamples, snapshot.Attempts)

	// 未到期不检测。
	now = now.Add(10 * time.Minute)
	require.NoError(t, svc.RunDue(context.Background()))
	require.Len(t, tester.prompts, 2)

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
		successReply(texts.other), successReply(texts.other),
		successReply(texts.other), successReply(texts.other),
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
	require.Len(t, tester.prompts, 2, "停检期间不自动检测")

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
	require.Equal(t, ModelDegradationProbeSamples+ModelDegradationProbeMaxRetries, snapshot.Attempts)
	require.Len(t, tester.prompts, ModelDegradationProbeSamples+ModelDegradationProbeMaxRetries, "两条样本共用 3 次重试")
}

func TestModelDegradationProbeRetriesFailureInPlace(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{}},
	}}
	// 截断成只剩前 60 个数字，模拟上游返回不完整。
	numbers := strings.SplitN(strings.Trim(texts.astra, "[]"), ",", 61)
	truncated := "[" + strings.Join(numbers[:60], ",")
	for name, failure := range map[string]*ScheduledTestResult{
		"request error":       {Status: "failed", ErrorMessage: "API returned 502"},
		"incomplete response": successReply(truncated),
	} {
		t.Run(name, func(t *testing.T) {
			tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{
				failure, successReply(texts.astra), successReply(texts.astra),
			}}
			svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

			snapshot, err := svc.ProbeAccount(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, ModelDegradationProbeStatusNormal, snapshot.Status)
			require.Equal(t, 3, snapshot.Attempts, "只重试失败的那条，已有效的样本保留")
			require.Len(t, tester.prompts, 3)
			require.Empty(t, snapshot.LastError, "成功的结论不保留之前的失败原因")
			require.Equal(t, ModelDegradationProbeDefaultModel, snapshot.TopModel)
		})
	}
}

func TestModelDegradationProbeLowConfidenceIsFailure(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{}},
	}}
	// 每轮一条 astra、一条 luna：模拟上游时好时坏，合并后置信度不足，整轮重来直到预算耗尽。
	tester := &modelDegradationProbeTesterStub{cycle: true, replies: []*ScheduledTestResult{
		successReply(texts.lowConfidenceAstra), successReply(texts.other),
	}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ModelDegradationProbeStatusFailed, snapshot.Status)
	require.Less(t, snapshot.TopProbability, float64(ModelDegradationProbeDefaultConfidencePercent)/100)
	require.Contains(t, snapshot.LastError, "low confidence")
	require.Zero(t, snapshot.ConsecutiveDegraded, "置信度不足不计入疑似降智")
	require.Equal(t, (ModelDegradationProbeMaxRetries+1)*ModelDegradationProbeSamples, snapshot.Attempts)
}

func TestModelDegradationProbeCombinesSamplesForConfidence(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	bank, err := modeltrace.DefaultBank()
	require.NoError(t, err)
	single, err := bank.Analyze([]modeltrace.Output{{Text: texts.lowConfidenceAstra, ExpectedCount: 218}})
	require.NoError(t, err)
	require.Less(t, single.Top().Probability, float64(ModelDegradationProbeDefaultConfidencePercent)/100, "单条达不到门槛")

	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{
		successReply(texts.lowConfidenceAstra), successReply(texts.astra),
	}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ModelDegradationProbeStatusNormal, snapshot.Status, "两条合并后达到门槛")
	require.GreaterOrEqual(t, snapshot.TopProbability, float64(ModelDegradationProbeDefaultConfidencePercent)/100)
	require.Equal(t, ModelDegradationProbeSamples, snapshot.Attempts)
}

func TestModelDegradationProbeUsesSelectedModel(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbeModelExtraKey: "gpt-6-luna",
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply(texts.other)}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "gpt-6-luna", snapshot.ExpectedModel)
	require.Equal(t, ModelDegradationProbeStatusNormal, snapshot.Status, "第一候选与所选模型一致即正常")
	require.Equal(t, []string{"gpt-6-luna", "gpt-6-luna"}, tester.models, "请求所选模型")
	require.Greater(t, snapshot.ExpectedProbability, float64(ModelDegradationProbeDefaultConfidencePercent)/100)
}

func TestModelDegradationProbeChangingConfidence(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Now()
	extra := map[string]any{
		ModelDegradationProbeConfidenceExtraKey: 95,
		ModelDegradationProbeExtraKey:           map[string]any{"status": "degraded", "consecutive_degraded": 2},
	}
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: extra},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply(texts.other)}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)
	for _, wantCount := range []int{1, 2} {
		snapshot, err := svc.ProbeAccount(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, 95, snapshot.MinConfidencePercent)
		require.Equal(t, wantCount, snapshot.ConsecutiveDegraded)
	}
	extra[ModelDegradationProbeConfidenceExtraKey] = 100
	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ModelDegradationProbeStatusFailed, snapshot.Status)
	require.Contains(t, snapshot.LastError, "< 100%")
	require.Zero(t, snapshot.ConsecutiveDegraded)
}

func TestModelDegradationProbeSwitchingModelResetsConsecutiveCount(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbeModelExtraKey: "gpt-6-luna",
			ModelDegradationProbeExtraKey: map[string]any{
				"status": "degraded", "expected_model": ModelDegradationProbeDefaultModel, "consecutive_degraded": 2,
				"last_attempt_at": now.Add(-time.Hour), "next_probe_at": now.Add(-time.Minute),
			},
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply(texts.astra)}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ModelDegradationProbeStatusDegraded, snapshot.Status)
	require.Equal(t, 1, snapshot.ConsecutiveDegraded, "换模型后不沿用旧模型的连续次数")
}

func TestModelDegradationProbeUnknownModelFailsWithoutRequest(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbeModelExtraKey: "gpt-unknown",
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply("")}}
	svc, _ := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ModelDegradationProbeStatusFailed, snapshot.Status)
	require.Contains(t, snapshot.LastError, "gpt-unknown")
	require.Empty(t, tester.prompts, "库里没有的模型不发请求")
}

func TestModelDegradationProbeVerdictRequiresConfidence(t *testing.T) {
	require.Equal(t, ModelDegradationProbeStatusNormal, modelDegradationProbeVerdict(ModelDegradationProbeDefaultModel, ModelDegradationProbeDefaultModel, 0.98, 98))
	require.Equal(t, ModelDegradationProbeStatusDegraded, modelDegradationProbeVerdict(ModelDegradationProbeDefaultModel, "gpt-6-luna", 0.999, 98))
	require.Equal(t, ModelDegradationProbeStatusFailed, modelDegradationProbeVerdict(ModelDegradationProbeDefaultModel, ModelDegradationProbeDefaultModel, 0.979, 98))
	require.Equal(t, ModelDegradationProbeStatusFailed, modelDegradationProbeVerdict(ModelDegradationProbeDefaultModel, "gpt-6-luna", 0.6, 98))
}

func TestModelDegradationProbePauseEnabledSwitch(t *testing.T) {
	account := func(extra map[string]any) *Account { return &Account{Extra: roundTripExtra(extra)} }

	// 未设置开关沿用旧语义：阈值大于 0 即暂停。
	require.Equal(t, 2, ModelDegradationProbePauseThreshold(account(map[string]any{ModelDegradationProbePauseThresholdExtraKey: 2})))
	require.Zero(t, ModelDegradationProbePauseThreshold(account(map[string]any{})))
	// 关闭开关时保留阈值但不暂停。
	require.Zero(t, ModelDegradationProbePauseThreshold(account(map[string]any{
		ModelDegradationProbePauseEnabledExtraKey:   false,
		ModelDegradationProbePauseThresholdExtraKey: 2,
	})))
	// 开启开关但阈值为 0 时按 1 次处理。
	require.Equal(t, 1, ModelDegradationProbePauseThreshold(account(map[string]any{
		ModelDegradationProbePauseEnabledExtraKey:   true,
		ModelDegradationProbePauseThresholdExtraKey: 0,
	})))
}

func TestModelDegradationProbeSetPauseEnabled(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbePauseThresholdExtraKey: 3,
		}},
		2: {ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive},
	}}
	svc, pauser := newModelDegradationProbeTestService(repo, &modelDegradationProbeTesterStub{}, &now)

	resumed, err := svc.SetPauseEnabled(context.Background(), 1, false)
	require.NoError(t, err)
	require.False(t, resumed, "未处于暂停时无需恢复")
	require.Empty(t, pauser.resumes)
	account, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, ModelDegradationProbePauseEnabled(account))
	require.Equal(t, 3, modelDegradationProbeRawPauseThreshold(account), "只改开关，保留阈值")

	_, err = svc.SetPauseEnabled(context.Background(), 1, true)
	require.NoError(t, err)
	account, err = repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 3, ModelDegradationProbePauseThreshold(account))

	_, err = svc.SetPauseEnabled(context.Background(), 2, true)
	require.ErrorIs(t, err, ErrModelDegradationProbeAccountInvalid)
}

func TestModelDegradationProbeTurningPauseOffResumesProbePause(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbePauseEnabledExtraKey:   true,
			ModelDegradationProbePauseThresholdExtraKey: 1,
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply(texts.other)}}
	svc, pauser := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, snapshot.PausedUntil, "疑似降智后被暂停")

	resumed, err := svc.SetPauseEnabled(context.Background(), 1, false)
	require.NoError(t, err)
	require.True(t, resumed)
	require.Equal(t, []int64{1}, pauser.resumes)
	account, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, account.TempUnschedulableUntil, "账号恢复调度")
	stored := DecodeModelDegradationProbeSnapshot(account.Extra)
	require.Nil(t, stored.PausedUntil, "快照不再展示暂停")
	require.Equal(t, 1, stored.ConsecutiveDegraded, "检测结果保留")
	require.False(t, ModelDegradationProbePauseEnabled(account))
}

func TestModelDegradationProbeTurningPauseOffKeepsOtherTempUnschedulable(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	until := now.Add(30 * time.Minute)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
			TempUnschedulableUntil:  &until,
			TempUnschedulableReason: `{"matched_keyword":"rate limit","rule_index":0,"status_code":429}`,
			Extra:                   map[string]any{ModelDegradationProbePauseEnabledExtraKey: true},
		},
	}}
	svc, pauser := newModelDegradationProbeTestService(repo, &modelDegradationProbeTesterStub{}, &now)

	resumed, err := svc.SetPauseEnabled(context.Background(), 1, false)
	require.NoError(t, err)
	require.False(t, resumed, "429 规则造成的暂停不归降智检测管")
	require.Empty(t, pauser.resumes)
	account, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, until, *account.TempUnschedulableUntil)
}

func TestModelDegradationProbeSwitchOffSkipsPause(t *testing.T) {
	texts := loadModelDegradationReferenceTexts(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	repo := &modelDegradationProbeRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{
			ModelDegradationProbePauseEnabledExtraKey:   false,
			ModelDegradationProbePauseThresholdExtraKey: 1,
		}},
	}}
	tester := &modelDegradationProbeTesterStub{replies: []*ScheduledTestResult{successReply(texts.other)}}
	svc, pauser := newModelDegradationProbeTestService(repo, tester, &now)

	snapshot, err := svc.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ModelDegradationProbeStatusDegraded, snapshot.Status)
	require.Equal(t, 1, snapshot.ConsecutiveDegraded)
	require.Nil(t, snapshot.PausedUntil)
	require.Empty(t, pauser.pauses)
	require.Equal(t, ModelDegradationProbeSamples, snapshot.Attempts, "有效结论不重试")
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
	require.NoError(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbePauseEnabledExtraKey: true}))
	require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbePauseEnabledExtraKey: "true"}))

	for _, invalid := range []any{0, 101, 95.5} {
		require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbeConfidenceExtraKey: invalid}))
	}
	modelExtra := map[string]any{ModelDegradationProbeModelExtraKey: " gpt-6.2-astra "}
	require.NoError(t, ValidateModelDegradationProbeExtra(modelExtra))
	require.Equal(t, "gpt-6.2-astra", modelExtra[ModelDegradationProbeModelExtraKey])
	require.NoError(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbeModelExtraKey: ""}), "空值表示默认模型")
	require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbeModelExtraKey: "gpt-unknown"}))
	require.Error(t, ValidateModelDegradationProbeExtra(map[string]any{ModelDegradationProbeModelExtraKey: 123}))
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
	require.Len(t, tester.prompts, ModelDegradationProbeSamples)

	// 停检期间仍可手动检测；依旧降智时暂停从本次检测起重新计算 60 分钟。
	snapshot, err = svc.ProbeAccount(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, 2, snapshot.ConsecutiveDegraded)
	require.Equal(t, now.Add(60*time.Minute), pauser.untils[1])
}
