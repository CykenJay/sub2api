//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestListModelDegradationProbeCandidates(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	suffix := time.Now().UnixNano()

	create := func(name, platform, accountType, status string, extra map[string]any) *service.Account {
		return mustCreateAccount(t, client, &service.Account{
			Name:        fmt.Sprintf("%s-%d", name, suffix),
			Platform:    platform,
			Type:        accountType,
			Status:      status,
			Credentials: map[string]any{"api_key": "secret"},
			Extra:       extra,
		})
	}
	withInterval := map[string]any{service.ModelDegradationProbeIntervalExtraKey: 30}
	oauth := create("mdp-oauth", service.PlatformOpenAI, service.AccountTypeOAuth, service.StatusActive, withInterval)
	apikey := create("mdp-apikey", service.PlatformOpenAI, service.AccountTypeAPIKey, service.StatusActive, withInterval)
	unset := create("mdp-unset", service.PlatformOpenAI, service.AccountTypeOAuth, service.StatusActive, map[string]any{})
	disabled := create("mdp-disabled", service.PlatformOpenAI, service.AccountTypeOAuth, service.StatusDisabled, withInterval)
	other := create("mdp-other", service.PlatformAnthropic, service.AccountTypeOAuth, service.StatusActive, withInterval)

	require.NoError(t, repo.UpdateExtra(ctx, oauth.ID, map[string]any{
		service.ModelDegradationProbeExtraKey: service.ModelDegradationProbeSnapshot{
			Status:        service.ModelDegradationProbeStatusDegraded,
			ExpectedModel: service.ModelDegradationProbeExpectedModel,
			TopModel:      "gpt-6-luna",
			LastAttemptAt: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC),
			NextProbeAt:   time.Date(2026, 11, 3, 10, 30, 0, 0, time.UTC),
		},
	}))

	rows, err := repo.ListModelDegradationProbeCandidates(ctx)
	require.NoError(t, err)
	byID := map[int64]map[string]any{}
	for _, row := range rows {
		byID[row.ID] = row.Extra
	}
	require.Contains(t, byID, oauth.ID)
	require.Contains(t, byID, apikey.ID)
	require.NotContains(t, byID, unset.ID)
	require.NotContains(t, byID, disabled.ID)
	require.NotContains(t, byID, other.ID)

	snapshot := service.DecodeModelDegradationProbeSnapshot(byID[oauth.ID])
	require.NotNil(t, snapshot)
	require.Equal(t, "gpt-6-luna", snapshot.TopModel)
	require.Equal(t, 30, service.ModelDegradationProbeIntervalMinutes(&service.Account{Extra: byID[oauth.ID]}))
}
