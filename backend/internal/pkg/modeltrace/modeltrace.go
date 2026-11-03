// Package modeltrace 移植 ModelTrace（https://github.com/xqy2006/ModelTrace，MIT）
// 的统一全局稳健数字指纹评分器：让模型逐项给出 1..355 的整数，按数字分布与
// 有序块特征对比指纹库中各模型的中心，得到候选模型的闭集概率。
//
// unified_bank.json 原样取自 ModelTrace data/unified_bank.json
// （commit 6cd4a8f，sha256 f267d0513aa06981d024ec5394746fceddf44e4e8a75fa6bf8304255f4b32d6d），
// 评分逻辑与其 static/fingerprint-core.js 保持一致。
package modeltrace

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"unicode"
)

const (
	ValueMin           = 1
	ValueMax           = 355
	Dimension          = ValueMax - ValueMin + 1
	alpha              = 0.5
	minimumValidNumber = 80
)

//go:embed unified_bank.json
var unifiedBankJSON []byte

// ErrNotEnoughNumbers 表示回答中可用的数字不足（拒答、截断或格式不符）。
var ErrNotEnoughNumbers = errors.New("modeltrace: not enough numbers in answer")

type bankModel struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Family      string    `json:"family"`
	FamilyName  string    `json:"family_name"`
	Counts      []float64 `json:"counts"`
}

type hellingerArtifact struct {
	FeatureMean   []float64   `json:"feature_mean"`
	FeatureScale  []float64   `json:"feature_scale"`
	NuisanceBasis [][]float64 `json:"nuisance_basis"`
	Centroids     [][]float64 `json:"centroids"`
}

type orderedBlocksArtifact struct {
	Weight               float64       `json:"weight"`
	FeatureMean          []float64     `json:"feature_mean"`
	FeatureScale         []float64     `json:"feature_scale"`
	NuisanceBasis        [][]float64   `json:"nuisance_basis"`
	Centroids            [][]float64   `json:"centroids"`
	EnvironmentCentroids [][][]float64 `json:"environment_centroids"`
}

type calibration struct {
	Beta       float64 `json:"beta"`
	CVAccuracy float64 `json:"cv_accuracy"`
}

// Bank 是解析后的指纹库。
type Bank struct {
	Models []bankModel `json:"models"`
	Robust struct {
		Hellinger     *hellingerArtifact     `json:"hellinger"`
		OrderedBlocks *orderedBlocksArtifact `json:"ordered_blocks"`
	} `json:"robust"`
	Calibration map[string]calibration `json:"calibration"`
}

var (
	defaultBankOnce sync.Once
	defaultBank     *Bank
	defaultBankErr  error
)

// DefaultBank 返回内嵌的统一指纹库，首次调用时解析。
func DefaultBank() (*Bank, error) {
	defaultBankOnce.Do(func() {
		defaultBank, defaultBankErr = ParseBank(unifiedBankJSON)
	})
	return defaultBank, defaultBankErr
}

// ParseBank 解析并校验指纹库。
func ParseBank(data []byte) (*Bank, error) {
	var bank Bank
	if err := json.Unmarshal(data, &bank); err != nil {
		return nil, fmt.Errorf("modeltrace: parse bank: %w", err)
	}
	if len(bank.Models) == 0 || bank.Robust.Hellinger == nil {
		return nil, errors.New("modeltrace: bank has no models or hellinger artifact")
	}
	h := bank.Robust.Hellinger
	if len(h.FeatureMean) != Dimension || len(h.FeatureScale) != Dimension || len(h.Centroids) != len(bank.Models) {
		return nil, errors.New("modeltrace: hellinger artifact dimension mismatch")
	}
	for _, model := range bank.Models {
		if len(model.Counts) != Dimension {
			return nil, fmt.Errorf("modeltrace: model %s counts dimension mismatch", model.ID)
		}
	}
	if o := bank.Robust.OrderedBlocks; o != nil {
		if len(o.Centroids) != len(bank.Models) {
			return nil, errors.New("modeltrace: ordered block centroids mismatch")
		}
		for _, env := range o.EnvironmentCentroids {
			if len(env) != len(bank.Models) {
				return nil, errors.New("modeltrace: ordered block environment centroids mismatch")
			}
		}
	}
	if _, ok := bank.Calibration["1"]; !ok {
		return nil, errors.New("modeltrace: bank has no single-query calibration")
	}
	return &bank, nil
}

// HasModel 报告指纹库是否收录了指定模型。
func (b *Bank) HasModel(modelID string) bool {
	for _, model := range b.Models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

// Candidate 是单个候选模型的归因结果。
type Candidate struct {
	Model       string  `json:"model"`
	DisplayName string  `json:"display_name"`
	Family      string  `json:"family"`
	Probability float64 `json:"probability"`
	Score       float64 `json:"score"`
}

// Result 是一次归因的结果，Candidates 按概率降序。
type Result struct {
	Candidates    []Candidate `json:"candidates"`
	ParsedNumbers int         `json:"parsed_numbers"`
	UsedOutputs   int         `json:"used_outputs"`
}

// Top 返回第一候选。
func (r *Result) Top() Candidate {
	return r.Candidates[0]
}

// ProbabilityOf 返回指定模型的概率，未收录时为 0。
func (r *Result) ProbabilityOf(modelID string) float64 {
	for _, candidate := range r.Candidates {
		if candidate.Model == modelID {
			return candidate.Probability
		}
	}
	return 0
}

// Output 是一份待评分的模型回答。
type Output struct {
	Text          string
	ExpectedCount int
}

// MinimumNumbers 返回一份回答被接受所需的最少数字个数。
func MinimumNumbers(expectedCount int) int {
	if expectedCount <= 0 {
		return minimumValidNumber
	}
	minimum := int(math.Ceil(float64(expectedCount) * 0.55))
	if minimum < minimumValidNumber {
		return minimumValidNumber
	}
	return minimum
}

var digitRunPattern = regexp.MustCompile(`\d+`)

// ParseNumbers 取回答中最长的一段数字序列：两数之间出现字母即视为新的一段，
// 超出 1..355 的数字被丢弃但不切段。
func ParseNumbers(text string) []int {
	var runs [][]int
	var current []int
	previousEnd := 0
	for _, loc := range digitRunPattern.FindAllStringIndex(text, -1) {
		separator := text[previousEnd:loc[0]]
		if len(current) > 0 && containsLetter(separator) {
			runs = append(runs, current)
			current = nil
		}
		if value, err := strconv.Atoi(text[loc[0]:loc[1]]); err == nil && value >= ValueMin && value <= ValueMax {
			current = append(current, value)
		}
		previousEnd = loc[1]
	}
	if len(current) > 0 {
		runs = append(runs, current)
	}
	var best []int
	for _, run := range runs {
		if len(run) > len(best) {
			best = run
		}
	}
	return best
}

func containsLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// Analyze 对一份或多份回答做闭集归因。数字不足的回答被跳过，全部无效时返回 ErrNotEnoughNumbers。
func (b *Bank) Analyze(outputs []Output) (*Result, error) {
	var scoreSets [][]float64
	parsed := 0
	for _, output := range outputs {
		numbers := ParseNumbers(output.Text)
		if len(numbers) > parsed {
			parsed = len(numbers)
		}
		if len(numbers) < MinimumNumbers(output.ExpectedCount) {
			continue
		}
		scoreSets = append(scoreSets, b.scoreNumbers(numbers))
	}
	if len(scoreSets) == 0 {
		return nil, fmt.Errorf("%w: parsed %d", ErrNotEnoughNumbers, parsed)
	}

	combined := make([]float64, len(b.Models))
	for i := range combined {
		for _, scores := range scoreSets {
			combined[i] += scores[i]
		}
		combined[i] /= float64(len(scoreSets))
	}
	key := strconv.Itoa(min(len(scoreSets), 3))
	cal, ok := b.Calibration[key]
	if !ok {
		cal = b.Calibration["1"]
	}
	scaled := make([]float64, len(combined))
	for i, value := range combined {
		scaled[i] = cal.Beta * value
	}
	probabilities := softmax(scaled)

	candidates := make([]Candidate, len(b.Models))
	for i, model := range b.Models {
		candidates[i] = Candidate{
			Model:       model.ID,
			DisplayName: model.DisplayName,
			Family:      model.Family,
			Probability: probabilities[i],
			Score:       combined[i],
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Probability > candidates[j].Probability
	})
	return &Result{Candidates: candidates, ParsedNumbers: parsed, UsedOutputs: len(scoreSets)}, nil
}

func (b *Bank) scoreNumbers(numbers []int) []float64 {
	marginal := b.hellingerScores(countNumbers(numbers))
	artifact := b.Robust.OrderedBlocks
	if artifact == nil || artifact.Weight == 0 {
		return marginal
	}
	ordered := b.orderedBlockScores(numbers)
	out := make([]float64, len(marginal))
	for i := range marginal {
		out[i] = (1-artifact.Weight)*marginal[i] + artifact.Weight*ordered[i]
	}
	return out
}

func (b *Bank) hellingerScores(counts []float64) []float64 {
	artifact := b.Robust.Hellinger
	feature := hellingerFeature(counts)
	projected := make([]float64, len(feature))
	for i, value := range feature {
		projected[i] = (value - artifact.FeatureMean[i]) / artifact.FeatureScale[i]
	}
	projected = normalized(subtractBasis(projected, artifact.NuisanceBasis))
	scores := make([]float64, len(artifact.Centroids))
	for i, centroid := range artifact.Centroids {
		scores[i] = dot(projected, centroid)
	}
	return standardize(scores)
}

func (b *Bank) orderedBlockScores(numbers []int) []float64 {
	artifact := b.Robust.OrderedBlocks
	feature := orderedBlockFeature(numbers)
	standardized := make([]float64, len(feature))
	for i, value := range feature {
		standardized[i] = (value - artifact.FeatureMean[i]) / artifact.FeatureScale[i]
	}
	unit := normalized(standardized)
	template := make([]float64, len(artifact.Centroids))
	for modelIndex := range artifact.Centroids {
		best := math.Inf(-1)
		for _, env := range artifact.EnvironmentCentroids {
			best = math.Max(best, dot(unit, env[modelIndex]))
		}
		template[modelIndex] = best
	}
	template = standardize(template)
	projected := normalized(subtractBasis(standardized, artifact.NuisanceBasis))
	nuisance := make([]float64, len(artifact.Centroids))
	for i, centroid := range artifact.Centroids {
		nuisance[i] = dot(projected, centroid)
	}
	nuisance = standardize(nuisance)
	mixed := make([]float64, len(template))
	for i := range template {
		mixed[i] = 0.5*template[i] + 0.5*nuisance[i]
	}
	return standardize(mixed)
}

func countNumbers(numbers []int) []float64 {
	counts := make([]float64, Dimension)
	for _, number := range numbers {
		counts[number-ValueMin]++
	}
	return counts
}

func hellingerFeature(counts []float64) []float64 {
	total := alpha * Dimension
	for _, value := range counts {
		total += value
	}
	out := make([]float64, len(counts))
	for i, value := range counts {
		out[i] = math.Sqrt((value + alpha) / total)
	}
	return out
}

func splitIntoFour(values []int) [][]int {
	base := len(values) / 4
	remainder := len(values) % 4
	chunks := make([][]int, 0, 4)
	start := 0
	for i := 0; i < 4; i++ {
		size := base
		if i < remainder {
			size++
		}
		chunks = append(chunks, values[start:start+size])
		start += size
	}
	return chunks
}

func orderedBlockFeature(numbers []int) []float64 {
	pieces := make([]float64, 0, 4*16+10)
	for _, chunk := range splitIntoFour(numbers) {
		bins := make([]float64, 16)
		for i := range bins {
			bins[i] = 0.5
		}
		for _, value := range chunk {
			index := int(math.Floor(float64(value-1) / 355 * 16))
			if index > 15 {
				index = 15
			}
			bins[index]++
		}
		total := sum(bins)
		for _, value := range bins {
			pieces = append(pieces, math.Sqrt(value/total))
		}
	}
	lastDigits := make([]float64, 10)
	for i := range lastDigits {
		lastDigits[i] = 0.5
	}
	for _, value := range numbers {
		lastDigits[value%10]++
	}
	total := sum(lastDigits)
	for _, value := range lastDigits {
		pieces = append(pieces, math.Sqrt(value/total))
	}
	return pieces
}

func sum(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total
}

func mean(values []float64) float64 {
	return sum(values) / float64(len(values))
}

func standardize(values []float64) []float64 {
	center := mean(values)
	variance := 0.0
	for _, value := range values {
		variance += (value - center) * (value - center)
	}
	variance /= float64(len(values))
	scale := math.Max(math.Sqrt(variance), 1e-12)
	out := make([]float64, len(values))
	for i, value := range values {
		out[i] = (value - center) / scale
	}
	return out
}

func dot(left, right []float64) float64 {
	value := 0.0
	for i := range left {
		value += left[i] * right[i]
	}
	return value
}

func normalized(values []float64) []float64 {
	scale := math.Max(math.Sqrt(dot(values, values)), 1e-12)
	out := make([]float64, len(values))
	for i, value := range values {
		out[i] = value / scale
	}
	return out
}

func subtractBasis(values []float64, basis [][]float64) []float64 {
	out := append([]float64(nil), values...)
	for _, vector := range basis {
		projection := dot(out, vector)
		for i := range out {
			out[i] -= projection * vector[i]
		}
	}
	return out
}

func softmax(values []float64) []float64 {
	maximum := math.Inf(-1)
	for _, value := range values {
		maximum = math.Max(maximum, value)
	}
	weights := make([]float64, len(values))
	total := 0.0
	for i, value := range values {
		weights[i] = math.Exp(value - maximum)
		total += weights[i]
	}
	for i := range weights {
		weights[i] /= total
	}
	return weights
}
