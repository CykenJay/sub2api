package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modeltrace"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

// 模型降智检测：按账号配置的间隔，用 ModelTrace 数字指纹挑战请求 gpt-6-astra，
// 指纹第一候选不是 gpt-6-astra 即视为疑似降智。配置与结果都存放在 accounts.extra。
const (
	// ModelDegradationProbeIntervalExtraKey 检测间隔（分钟），0 或缺省表示不检测。
	ModelDegradationProbeIntervalExtraKey = "model_degradation_probe_interval_minutes"
	// ModelDegradationProbePauseThresholdExtraKey 连续疑似降智多少次后暂停调度，0 表示仅展示。
	ModelDegradationProbePauseThresholdExtraKey = "model_degradation_probe_pause_threshold"
	// ModelDegradationProbePauseMinutesExtraKey 达到阈值后临时不可调度的时长（分钟），0 时取默认值。
	ModelDegradationProbePauseMinutesExtraKey = "model_degradation_probe_pause_minutes"
	// ModelDegradationProbeCooldownMinutesExtraKey 疑似降智后停止自动检测的时长（分钟），0 表示按检测间隔。
	ModelDegradationProbeCooldownMinutesExtraKey = "model_degradation_probe_cooldown_minutes"
	// ModelDegradationProbeExtraKey 最近一次检测结果快照，仅由检测服务写入。
	ModelDegradationProbeExtraKey = "model_degradation_probe"

	ModelDegradationProbeExpectedModel = "gpt-6-astra"

	ModelDegradationProbeMaxIntervalMinutes  = 7 * 24 * 60
	ModelDegradationProbeMaxPauseThreshold   = 100
	ModelDegradationProbeMaxPauseMinutes     = 7 * 24 * 60
	ModelDegradationProbeMaxCooldownMinutes  = 7 * 24 * 60
	ModelDegradationProbeDefaultPauseMinutes = 60

	ModelDegradationProbeStatusNormal   = "normal"
	ModelDegradationProbeStatusDegraded = "degraded"
	ModelDegradationProbeStatusFailed   = "failed"

	modelDegradationProbeCycleInterval  = time.Minute
	modelDegradationProbeRequestTimeout = 5 * time.Minute
	modelDegradationProbeMaxPerCycle    = 20
	modelDegradationProbeConcurrency    = 4
	modelDegradationProbeTopCandidates  = 3
	modelDegradationProbeMaxErrorLength = 300
	// 暂停原因里的标记，标明临时不可调度来自降智检测。
	modelDegradationProbePauseMarker   = "model_degradation_probe"
	modelDegradationProbeLeaderLock    = "model:degradation:probe:leader"
	modelDegradationProbeLeaderLockTTL = 10 * time.Minute
)

var (
	ErrModelDegradationProbeUnavailable = infraerrors.ServiceUnavailable(
		"MODEL_DEGRADATION_PROBE_UNAVAILABLE", "model degradation probe is unavailable",
	)
	ErrModelDegradationProbeAccountInvalid = infraerrors.BadRequest(
		"MODEL_DEGRADATION_PROBE_ACCOUNT_INVALID", "model degradation probe only supports OpenAI OAuth / API key accounts",
	)
)

// ModelDegradationProbeCandidate 是快照中保存的候选模型。
type ModelDegradationProbeCandidate struct {
	Model       string  `json:"model"`
	Probability float64 `json:"probability"`
}

// ModelDegradationProbeSnapshot 持久化在 accounts.extra[model_degradation_probe]。
type ModelDegradationProbeSnapshot struct {
	Status              string                           `json:"status"`
	ExpectedModel       string                           `json:"expected_model"`
	TopModel            string                           `json:"top_model,omitempty"`
	TopProbability      float64                          `json:"top_probability,omitempty"`
	ExpectedProbability float64                          `json:"expected_probability,omitempty"`
	Candidates          []ModelDegradationProbeCandidate `json:"candidates,omitempty"`
	ParsedNumbers       int                              `json:"parsed_numbers,omitempty"`
	RequestedNumbers    int                              `json:"requested_numbers,omitempty"`
	ConsecutiveDegraded int                              `json:"consecutive_degraded"`
	// PausedUntil 是本检测最近一次设置的临时不可调度截止时间。
	PausedUntil *time.Time `json:"paused_until,omitempty"`
	// ProbeResumeAt 是疑似降智后停止自动检测的截止时间。
	ProbeResumeAt *time.Time `json:"probe_resume_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LatencyMs     int64      `json:"latency_ms,omitempty"`
	LastAttemptAt time.Time  `json:"last_attempt_at"`
	NextProbeAt   time.Time  `json:"next_probe_at"`
}

// ModelDegradationProbeCandidateRow 是到期筛选所需的最小账号信息。
type ModelDegradationProbeCandidateRow struct {
	ID    int64
	Extra map[string]any
}

type modelDegradationProbeCandidateLister interface {
	ListModelDegradationProbeCandidates(ctx context.Context) ([]ModelDegradationProbeCandidateRow, error)
}

type modelDegradationProbeTester interface {
	RunTestBackgroundWithPrompt(ctx context.Context, accountID int64, modelID, prompt string) (*ScheduledTestResult, error)
}

type modelDegradationProbePauser interface {
	PauseAccountTempUnschedulable(ctx context.Context, account *Account, until time.Time, reason, source string) error
}

// ModelDegradationProbeIntervalMinutes 返回账号配置的检测间隔，非法值视为 0。
func ModelDegradationProbeIntervalMinutes(account *Account) int {
	if account == nil {
		return 0
	}
	return clampModelDegradationProbeSetting(account.getExtraInt(ModelDegradationProbeIntervalExtraKey), ModelDegradationProbeMaxIntervalMinutes)
}

// ModelDegradationProbePauseThreshold 返回连续疑似降智后暂停调度的阈值，0 表示仅展示。
func ModelDegradationProbePauseThreshold(account *Account) int {
	if account == nil {
		return 0
	}
	return clampModelDegradationProbeSetting(account.getExtraInt(ModelDegradationProbePauseThresholdExtraKey), ModelDegradationProbeMaxPauseThreshold)
}

// ModelDegradationProbePauseMinutes 返回达到阈值后临时不可调度的分钟数。
func ModelDegradationProbePauseMinutes(account *Account) int {
	if account == nil {
		return ModelDegradationProbeDefaultPauseMinutes
	}
	minutes := clampModelDegradationProbeSetting(account.getExtraInt(ModelDegradationProbePauseMinutesExtraKey), ModelDegradationProbeMaxPauseMinutes)
	if minutes == 0 {
		return ModelDegradationProbeDefaultPauseMinutes
	}
	return minutes
}

// ModelDegradationProbeCooldownMinutes 返回疑似降智后停止自动检测的分钟数，0 表示按检测间隔。
func ModelDegradationProbeCooldownMinutes(account *Account) int {
	if account == nil {
		return 0
	}
	return clampModelDegradationProbeSetting(account.getExtraInt(ModelDegradationProbeCooldownMinutesExtraKey), ModelDegradationProbeMaxCooldownMinutes)
}

func clampModelDegradationProbeSetting(value, maximum int) int {
	if value < 0 {
		return 0
	}
	if value > maximum {
		return maximum
	}
	return value
}

// IsModelDegradationProbeAccount 报告账号是否支持降智检测（OpenAI OAuth / API Key）。
func IsModelDegradationProbeAccount(account *Account) bool {
	return account != nil && account.Platform == PlatformOpenAI &&
		(account.Type == AccountTypeOAuth || account.Type == AccountTypeAPIKey)
}

// ValidateModelDegradationProbeExtra 校验管理端提交的降智检测配置。
func ValidateModelDegradationProbeExtra(extra map[string]any) error {
	checks := []struct {
		key     string
		maximum int
	}{
		{ModelDegradationProbeIntervalExtraKey, ModelDegradationProbeMaxIntervalMinutes},
		{ModelDegradationProbePauseThresholdExtraKey, ModelDegradationProbeMaxPauseThreshold},
		{ModelDegradationProbePauseMinutesExtraKey, ModelDegradationProbeMaxPauseMinutes},
		{ModelDegradationProbeCooldownMinutesExtraKey, ModelDegradationProbeMaxCooldownMinutes},
	}
	for _, check := range checks {
		raw, ok := extra[check.key]
		if !ok || raw == nil {
			continue
		}
		value, valid := modelDegradationProbeIntValue(raw)
		if !valid || value < 0 || value > check.maximum {
			return fmt.Errorf("%s must be an integer between 0 and %d", check.key, check.maximum)
		}
		extra[check.key] = value
	}
	return nil
}

func modelDegradationProbeIntValue(raw any) (int, bool) {
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case int:
		return v, true
	case int64:
		value = float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		value = f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		value = f
	default:
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false
	}
	return int(value), true
}

// DecodeModelDegradationProbeSnapshot 解析账号 extra 中的检测快照，缺失或非法时返回 nil。
func DecodeModelDegradationProbeSnapshot(extra map[string]any) *ModelDegradationProbeSnapshot {
	if extra == nil {
		return nil
	}
	value, ok := extra[ModelDegradationProbeExtraKey]
	if !ok || value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var snapshot ModelDegradationProbeSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil
	}
	switch snapshot.Status {
	case ModelDegradationProbeStatusNormal, ModelDegradationProbeStatusDegraded, ModelDegradationProbeStatusFailed:
		return &snapshot
	}
	return nil
}

// modelDegradationProbeDue 判断账号是否到期。下次检测时间按上次检测时间与当前配置重新计算，
// 修改间隔或降智后停检时长立即生效：最近一次疑似降智且配置了停检时长时按停检时长，否则按检测间隔。
func modelDegradationProbeDue(account *Account, now time.Time) bool {
	intervalMinutes := ModelDegradationProbeIntervalMinutes(account)
	if intervalMinutes <= 0 {
		return false
	}
	snapshot := DecodeModelDegradationProbeSnapshot(account.Extra)
	if snapshot == nil || snapshot.LastAttemptAt.IsZero() {
		return true
	}
	return !now.Before(modelDegradationProbeNextAt(account, snapshot, intervalMinutes))
}

func modelDegradationProbeNextAt(account *Account, snapshot *ModelDegradationProbeSnapshot, intervalMinutes int) time.Time {
	if snapshot.Status == ModelDegradationProbeStatusDegraded {
		if cooldown := ModelDegradationProbeCooldownMinutes(account); cooldown > 0 {
			return snapshot.LastAttemptAt.Add(time.Duration(cooldown) * time.Minute)
		}
	}
	return snapshot.LastAttemptAt.Add(time.Duration(intervalMinutes) * time.Minute)
}

// ModelDegradationProbeService 周期性检测账号的 gpt-6-astra 是否降智。
type ModelDegradationProbeService struct {
	accountRepo AccountRepository
	tester      modelDegradationProbeTester
	pauser      modelDegradationProbePauser
	bank        func() (*modeltrace.Bank, error)
	challenge   func() modeltrace.Challenge
	now         func() time.Time

	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string

	parentCtx    context.Context
	parentCancel context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	started      bool
	stopped      bool
	cycleMu      sync.Mutex
	probeGroup   singleflight.Group
	probeSlots   chan struct{}
}

func NewModelDegradationProbeService(
	accountRepo AccountRepository,
	accountTestService *AccountTestService,
	rateLimitService *RateLimitService,
) *ModelDegradationProbeService {
	ctx, cancel := context.WithCancel(context.Background())
	svc := &ModelDegradationProbeService{
		accountRepo:  accountRepo,
		bank:         modeltrace.DefaultBank,
		challenge:    modeltrace.NewChallenge,
		now:          time.Now,
		instanceID:   uuid.NewString(),
		parentCtx:    ctx,
		parentCancel: cancel,
		probeSlots:   make(chan struct{}, modelDegradationProbeConcurrency),
	}
	if accountTestService != nil {
		svc.tester = accountTestService
	}
	if rateLimitService != nil {
		svc.pauser = rateLimitService
	}
	return svc
}

// ProvideModelDegradationProbeService 创建并启动进程级检测循环。
func ProvideModelDegradationProbeService(
	accountRepo AccountRepository,
	accountTestService *AccountTestService,
	rateLimitService *RateLimitService,
	lockCache LeaderLockCache,
	db *sql.DB,
) *ModelDegradationProbeService {
	svc := NewModelDegradationProbeService(accountRepo, accountTestService, rateLimitService)
	svc.lockCache = lockCache
	svc.db = db
	svc.Start()
	return svc
}

func (s *ModelDegradationProbeService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.runLoop()
}

func (s *ModelDegradationProbeService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.parentCancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *ModelDegradationProbeService) runLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(modelDegradationProbeCycleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.parentCtx.Done():
			return
		case <-ticker.C:
			if err := s.RunDue(s.parentCtx); err != nil {
				logger.LegacyPrintf("service.model_degradation_probe", "run_due_failed: err=%v", err)
			}
		}
	}
}

// RunDue 检测一批到期账号。多实例部署时只有持有 leader 锁的实例执行。
func (s *ModelDegradationProbeService) RunDue(ctx context.Context) error {
	if s == nil || s.accountRepo == nil || s.tester == nil {
		return nil
	}
	lister, ok := s.accountRepo.(modelDegradationProbeCandidateLister)
	if !ok {
		return nil
	}
	s.cycleMu.Lock()
	defer s.cycleMu.Unlock()

	release, acquired, err := s.tryAcquireLeaderLock(ctx)
	if err != nil {
		return fmt.Errorf("acquire model degradation probe leader lock: %w", err)
	}
	if !acquired {
		return nil
	}
	defer release()

	rows, err := lister.ListModelDegradationProbeCandidates(ctx)
	if err != nil {
		return fmt.Errorf("list model degradation probe candidates: %w", err)
	}
	now := s.currentTime()
	type dueRow struct {
		id   int64
		next time.Time
	}
	due := make([]dueRow, 0, len(rows))
	for _, row := range rows {
		if !modelDegradationProbeDue(&Account{Extra: row.Extra}, now) {
			continue
		}
		var next time.Time
		if snapshot := DecodeModelDegradationProbeSnapshot(row.Extra); snapshot != nil {
			next = snapshot.NextProbeAt
		}
		due = append(due, dueRow{id: row.ID, next: next})
	}
	sort.SliceStable(due, func(i, j int) bool {
		if due[i].next.Equal(due[j].next) {
			return due[i].id < due[j].id
		}
		return due[i].next.Before(due[j].next)
	})
	if len(due) > modelDegradationProbeMaxPerCycle {
		due = due[:modelDegradationProbeMaxPerCycle]
	}

	var group errgroup.Group
	for _, row := range due {
		accountID := row.id
		group.Go(func() error {
			if _, probeErr := s.probe(ctx, accountID, true); probeErr != nil {
				logger.LegacyPrintf("service.model_degradation_probe", "probe_due_failed: account_id=%d err=%v", accountID, probeErr)
			}
			return nil
		})
	}
	return group.Wait()
}

// ProbeAccount 立即检测一个账号，不要求已配置检测间隔。
func (s *ModelDegradationProbeService) ProbeAccount(ctx context.Context, accountID int64) (*ModelDegradationProbeSnapshot, error) {
	if s == nil || s.accountRepo == nil || s.tester == nil {
		return nil, ErrModelDegradationProbeUnavailable
	}
	return s.probe(ctx, accountID, false)
}

// ModelDegradationProbeMaxBatchSize 限制一次手动批量检测提交的账号数。
const ModelDegradationProbeMaxBatchSize = 200

// ModelDegradationProbeBatchSkip 记录批量检测中被跳过的账号。
type ModelDegradationProbeBatchSkip struct {
	AccountID int64  `json:"account_id"`
	Reason    string `json:"reason"`
}

// ModelDegradationProbeBatchResult 是批量检测的提交结果，检测本身在后台执行。
type ModelDegradationProbeBatchResult struct {
	Queued  []int64                          `json:"queued"`
	Skipped []ModelDegradationProbeBatchSkip `json:"skipped"`
}

// ModelDegradationProbeSnapshotItem 是按账号查询的检测快照。
type ModelDegradationProbeSnapshotItem struct {
	AccountID int64                          `json:"account_id"`
	Snapshot  *ModelDegradationProbeSnapshot `json:"snapshot"`
}

// StartBatchProbe 提交一批手动检测并立即返回。检测在后台按全局并发上限执行，
// 同一账号已在检测中时复用进行中的那次；结果写入账号快照，可用 GetSnapshots 轮询。
func (s *ModelDegradationProbeService) StartBatchProbe(ctx context.Context, accountIDs []int64) (*ModelDegradationProbeBatchResult, error) {
	if s == nil || s.accountRepo == nil || s.tester == nil {
		return nil, ErrModelDegradationProbeUnavailable
	}
	accounts, err := s.accountRepo.GetByIDs(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	found := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			found[account.ID] = account
		}
	}
	result := &ModelDegradationProbeBatchResult{Queued: []int64{}, Skipped: []ModelDegradationProbeBatchSkip{}}
	for _, accountID := range accountIDs {
		account, ok := found[accountID]
		switch {
		case !ok:
			result.Skipped = append(result.Skipped, ModelDegradationProbeBatchSkip{AccountID: accountID, Reason: "not_found"})
		case !IsModelDegradationProbeAccount(account):
			result.Skipped = append(result.Skipped, ModelDegradationProbeBatchSkip{AccountID: accountID, Reason: "unsupported"})
		default:
			result.Queued = append(result.Queued, accountID)
		}
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil, ErrModelDegradationProbeUnavailable
	}
	s.wg.Add(len(result.Queued))
	s.mu.Unlock()
	for _, accountID := range result.Queued {
		go func(accountID int64) {
			defer s.wg.Done()
			if _, err := s.probe(s.parentCtx, accountID, false); err != nil {
				logger.LegacyPrintf("service.model_degradation_probe", "batch_probe_failed: account_id=%d err=%v", accountID, err)
			}
		}(accountID)
	}
	return result, nil
}

// GetSnapshots 返回指定账号的最新检测快照，供批量检测轮询进度。
func (s *ModelDegradationProbeService) GetSnapshots(ctx context.Context, accountIDs []int64) ([]ModelDegradationProbeSnapshotItem, error) {
	if s == nil || s.accountRepo == nil {
		return nil, ErrModelDegradationProbeUnavailable
	}
	accounts, err := s.accountRepo.GetByIDs(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	items := make([]ModelDegradationProbeSnapshotItem, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			items = append(items, ModelDegradationProbeSnapshotItem{
				AccountID: account.ID,
				Snapshot:  DecodeModelDegradationProbeSnapshot(account.Extra),
			})
		}
	}
	return items, nil
}

func (s *ModelDegradationProbeService) probe(ctx context.Context, accountID int64, scheduled bool) (*ModelDegradationProbeSnapshot, error) {
	value, err, _ := s.probeGroup.Do(strconv.FormatInt(accountID, 10), func() (any, error) {
		select {
		case s.probeSlots <- struct{}{}:
			defer func() { <-s.probeSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		account, err := s.accountRepo.GetByID(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if !IsModelDegradationProbeAccount(account) {
			return nil, ErrModelDegradationProbeAccountInvalid
		}
		if scheduled && (!account.IsActive() || !modelDegradationProbeDue(account, s.currentTime())) {
			return nil, nil
		}
		return s.probeLoadedAccount(ctx, account)
	})
	if err != nil || value == nil {
		return nil, err
	}
	snapshot, _ := value.(*ModelDegradationProbeSnapshot)
	return snapshot, nil
}

func (s *ModelDegradationProbeService) probeLoadedAccount(ctx context.Context, account *Account) (*ModelDegradationProbeSnapshot, error) {
	previous := DecodeModelDegradationProbeSnapshot(account.Extra)
	startedAt := s.currentTime().UTC()
	snapshot := &ModelDegradationProbeSnapshot{
		ExpectedModel: ModelDegradationProbeExpectedModel,
		LastAttemptAt: startedAt,
	}
	if previous != nil {
		snapshot.ConsecutiveDegraded = previous.ConsecutiveDegraded
		if previous.PausedUntil != nil && previous.PausedUntil.After(startedAt) {
			snapshot.PausedUntil = previous.PausedUntil
		}
	}

	s.runChallenge(ctx, account, snapshot)

	switch snapshot.Status {
	case ModelDegradationProbeStatusNormal:
		snapshot.ConsecutiveDegraded = 0
	case ModelDegradationProbeStatusDegraded:
		snapshot.ConsecutiveDegraded++
		if cooldown := ModelDegradationProbeCooldownMinutes(account); cooldown > 0 {
			resumeAt := startedAt.Add(time.Duration(cooldown) * time.Minute)
			snapshot.ProbeResumeAt = &resumeAt
		}
		s.pauseIfThresholdReached(ctx, account, snapshot)
	}
	if interval := ModelDegradationProbeIntervalMinutes(account); interval > 0 {
		snapshot.NextProbeAt = modelDegradationProbeNextAt(account, snapshot, interval)
	}

	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{ModelDegradationProbeExtraKey: snapshot}); err != nil {
		return nil, fmt.Errorf("save model degradation probe snapshot: %w", err)
	}
	if snapshot.Status == ModelDegradationProbeStatusDegraded {
		slog.Warn("model_degradation_probe_degraded",
			"account_id", account.ID,
			"expected_model", snapshot.ExpectedModel,
			"top_model", snapshot.TopModel,
			"top_probability", snapshot.TopProbability,
			"consecutive", snapshot.ConsecutiveDegraded,
			"paused_until", snapshot.PausedUntil)
	}
	return snapshot, nil
}

// pauseIfThresholdReached 连续疑似降智达到阈值时，把账号设为固定时长的临时不可调度，到期自动恢复。
func (s *ModelDegradationProbeService) pauseIfThresholdReached(ctx context.Context, account *Account, snapshot *ModelDegradationProbeSnapshot) {
	threshold := ModelDegradationProbePauseThreshold(account)
	if threshold <= 0 || snapshot.ConsecutiveDegraded < threshold || s.pauser == nil {
		return
	}
	until := snapshot.LastAttemptAt.Add(time.Duration(ModelDegradationProbePauseMinutes(account)) * time.Minute)
	if err := s.pauser.PauseAccountTempUnschedulable(ctx, account, until, s.pauseReason(snapshot, until), modelDegradationProbePauseMarker); err != nil {
		slog.Warn("model_degradation_probe_pause_failed", "account_id", account.ID, "error", err)
		return
	}
	snapshot.PausedUntil = &until
}

// runChallenge 发送一条指纹挑战并填写快照的检测结果字段。
func (s *ModelDegradationProbeService) runChallenge(ctx context.Context, account *Account, snapshot *ModelDegradationProbeSnapshot) {
	fail := func(message string) {
		snapshot.Status = ModelDegradationProbeStatusFailed
		snapshot.LastError = truncateModelDegradationProbeError(message)
	}
	bank, err := s.bank()
	if err != nil {
		fail(err.Error())
		return
	}
	challenge := s.challenge()
	snapshot.RequestedNumbers = challenge.ExpectedCount

	probeCtx, cancel := context.WithTimeout(ctx, modelDegradationProbeRequestTimeout)
	defer cancel()
	result, err := s.tester.RunTestBackgroundWithPrompt(probeCtx, account.ID, ModelDegradationProbeExpectedModel, challenge.Prompt)
	if err != nil {
		fail(err.Error())
		return
	}
	snapshot.LatencyMs = result.LatencyMs
	if result.Status != "success" {
		fail(result.ErrorMessage)
		return
	}

	analysis, err := bank.Analyze([]modeltrace.Output{{Text: result.ResponseText, ExpectedCount: challenge.ExpectedCount}})
	snapshot.ParsedNumbers = len(modeltrace.ParseNumbers(result.ResponseText))
	if err != nil {
		fail(fmt.Sprintf("valid numbers %d/%d", snapshot.ParsedNumbers, modeltrace.MinimumNumbers(challenge.ExpectedCount)))
		return
	}
	top := analysis.Top()
	snapshot.TopModel = top.Model
	snapshot.TopProbability = top.Probability
	snapshot.ExpectedProbability = analysis.ProbabilityOf(ModelDegradationProbeExpectedModel)
	for i := 0; i < len(analysis.Candidates) && i < modelDegradationProbeTopCandidates; i++ {
		snapshot.Candidates = append(snapshot.Candidates, ModelDegradationProbeCandidate{
			Model:       analysis.Candidates[i].Model,
			Probability: analysis.Candidates[i].Probability,
		})
	}
	if top.Model == ModelDegradationProbeExpectedModel {
		snapshot.Status = ModelDegradationProbeStatusNormal
	} else {
		snapshot.Status = ModelDegradationProbeStatusDegraded
	}
}

func (s *ModelDegradationProbeService) pauseReason(snapshot *ModelDegradationProbeSnapshot, until time.Time) string {
	message := fmt.Sprintf("模型降智检测：期望 %s，连续 %d 次疑似降智", snapshot.ExpectedModel, snapshot.ConsecutiveDegraded)
	if snapshot.TopModel != "" {
		message += fmt.Sprintf("，最近指纹第一候选 %s（%.1f%%）", snapshot.TopModel, snapshot.TopProbability*100)
	}
	state := TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: snapshot.LastAttemptAt.Unix(),
		MatchedKeyword:  modelDegradationProbePauseMarker,
		RuleIndex:       -1,
		ErrorMessage:    message,
		Scope:           "account",
	}
	raw, _ := json.Marshal(state)
	return string(raw)
}

func truncateModelDegradationProbeError(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "probe failed"
	}
	runes := []rune(message)
	if len(runes) > modelDegradationProbeMaxErrorLength {
		return string(runes[:modelDegradationProbeMaxErrorLength]) + "…"
	}
	return message
}

func (s *ModelDegradationProbeService) tryAcquireLeaderLock(ctx context.Context) (func(), bool, error) {
	lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if s.lockCache != nil {
		acquired, err := s.lockCache.TryAcquireLeaderLock(lockCtx, modelDegradationProbeLeaderLock, s.instanceID, modelDegradationProbeLeaderLockTTL)
		if err != nil || !acquired {
			return nil, false, err
		}
		return func() {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer releaseCancel()
			_ = s.lockCache.ReleaseLeaderLock(releaseCtx, modelDegradationProbeLeaderLock, s.instanceID)
		}, true, nil
	}
	if s.db != nil {
		return tryAcquireDBAdvisoryLockWithError(lockCtx, s.db, hashAdvisoryLockID(modelDegradationProbeLeaderLock))
	}
	return func() {}, true, nil
}

func (s *ModelDegradationProbeService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}
