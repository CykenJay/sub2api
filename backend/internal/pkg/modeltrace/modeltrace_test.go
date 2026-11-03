package modeltrace

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testdata/parity.json 由 ModelTrace 原版 JS 评分器（fingerprint-core.mjs）对其参考样本评分生成。
type parityFixture struct {
	Single []struct {
		Source        string             `json:"source"`
		Text          string             `json:"text"`
		ExpectedCount int                `json:"expected_count"`
		Top           string             `json:"top"`
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"single"`
	Multi struct {
		Outputs []struct {
			Text          string `json:"text"`
			ExpectedCount int    `json:"expected_count"`
		} `json:"outputs"`
		Top           string             `json:"top"`
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"multi"`
}

func loadParity(t *testing.T) parityFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/parity.json")
	require.NoError(t, err)
	var fixture parityFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Single)
	return fixture
}

func assertProbabilities(t *testing.T, want map[string]float64, got *Result) {
	t.Helper()
	require.Len(t, got.Candidates, len(want))
	for _, candidate := range got.Candidates {
		expected, ok := want[candidate.Model]
		require.True(t, ok, candidate.Model)
		require.LessOrEqual(t, math.Abs(expected-candidate.Probability), 1e-9, candidate.Model)
	}
}

func TestDefaultBankLoads(t *testing.T) {
	bank, err := DefaultBank()
	require.NoError(t, err)
	require.True(t, bank.HasModel("gpt-6-astra"))
	require.False(t, bank.HasModel("gpt-unknown"))
}

func TestAnalyzeMatchesJavaScriptScorer(t *testing.T) {
	bank, err := DefaultBank()
	require.NoError(t, err)
	fixture := loadParity(t)
	for _, tc := range fixture.Single {
		t.Run(tc.Source, func(t *testing.T) {
			result, err := bank.Analyze([]Output{{Text: tc.Text, ExpectedCount: tc.ExpectedCount}})
			require.NoError(t, err)
			require.Equal(t, tc.Top, result.Top().Model)
			assertProbabilities(t, tc.Probabilities, result)
		})
	}

	outputs := make([]Output, 0, len(fixture.Multi.Outputs))
	for _, output := range fixture.Multi.Outputs {
		outputs = append(outputs, Output{Text: output.Text, ExpectedCount: output.ExpectedCount})
	}
	result, err := bank.Analyze(outputs)
	require.NoError(t, err)
	require.Equal(t, 3, result.UsedOutputs)
	require.Equal(t, fixture.Multi.Top, result.Top().Model)
	assertProbabilities(t, fixture.Multi.Probabilities, result)
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
