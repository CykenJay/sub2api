package modeltrace

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultBankLoads(t *testing.T) {
	bank, err := DefaultBank()
	require.NoError(t, err)
	require.True(t, bank.HasModel("gpt-6-astra"))
	require.True(t, bank.HasModel("gpt-6.2-astra"))
	require.False(t, bank.HasModel("gpt-unknown"))
	require.Contains(t, bank.ModelIDs(), "gpt-6.2-sol")
	require.Len(t, bank.ModelIDs(), len(bank.Models))
}

func TestAnalyzeRejectsShortAnswers(t *testing.T) {
	bank, err := DefaultBank()
	require.NoError(t, err)
	_, err = bank.Analyze([]Output{{Text: "抱歉，我无法完成。12, 34, 56", ExpectedCount: 300}})
	require.True(t, errors.Is(err, ErrNotEnoughNumbers))
}

func TestParseNumbersTakesLongestRunSplitByLetters(t *testing.T) {
	require.Equal(t, []int{7, 8, 9, 10}, ParseNumbers("共 3 个: 1 2 3 here 7, 8, 999, 9, 10"))
	require.Empty(t, ParseNumbers("no numbers"))
}

func TestNewChallenge(t *testing.T) {
	for i := 0; i < 50; i++ {
		challenge := NewChallenge()
		require.GreaterOrEqual(t, challenge.ExpectedCount, 292)
		require.LessOrEqual(t, challenge.ExpectedCount, 332)
		require.True(t, strings.Contains(challenge.Prompt, "1 到 355"))
	}
	require.Equal(t, 166, MinimumNumbers(301))
	require.Equal(t, 80, MinimumNumbers(0))
}
