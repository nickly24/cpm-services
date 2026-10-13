package exams

import (
	"encoding/json"
	"math/big"
	"sort"
)

// Scores are represented in half points throughout the exam, avoiding binary
// floating point in both votes and rounding.
func maxScore(parts []Part) int {
	n := 0
	for _, p := range parts {
		n += p.Weight * p.Quota
	}
	return n
}
func voteHalf(n json.Number) (int, error) {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return 0, Bad("invalid_vote_value")
	}
	for i := 0; i <= 2; i++ {
		if r.Cmp(big.NewRat(int64(i), 2)) == 0 {
			return i, nil
		}
	}
	return 0, Bad("invalid_vote_value")
}
func halfNumber(n int) json.Number {
	if n%2 == 0 {
		return json.Number(new(big.Int).SetInt64(int64(n / 2)).String())
	}
	return json.Number(new(big.Int).SetInt64(int64(n/2)).String() + ".5")
}
func roundHalf(n int, up bool) int {
	if up {
		return (n + 1) / 2
	}
	return n / 2
}
func validateThresholds(maximum int, rows []Threshold) []string {
	errors := []string{}
	if maximum < 5 {
		errors = append(errors, "max_score_too_low")
	}
	if len(rows) == 0 {
		return append(errors, "grade_thresholds_required")
	}
	if len(rows) != 6 {
		return append(errors, "invalid_threshold_set")
	}
	ordered := append([]Threshold(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Grade < ordered[j].Grade })
	invalid, inc, above := false, false, false
	for i, r := range ordered {
		if r.Grade != i || r.MinScore < 0 || (i == 0 && r.MinScore != 0) {
			invalid = true
		}
		if i > 0 && ordered[i-1].MinScore >= r.MinScore {
			inc = true
		}
		if r.MinScore > maximum {
			above = true
		}
	}
	if invalid {
		errors = append(errors, "invalid_threshold_set")
	}
	if inc {
		errors = append(errors, "threshold_not_increasing")
	}
	if above {
		errors = append(errors, "threshold_above_max")
	}
	return errors
}
func gradeForScore(n int, rows []Threshold) int {
	grade := 0
	for _, r := range rows {
		if r.MinScore <= n && r.Grade > grade {
			grade = r.Grade
		}
	}
	return grade
}
func extraDirection(v int, mode string) (string, error) {
	switch v {
	case 2:
		return "up", nil
	case 0:
		return "down", nil
	case 1:
		switch mode {
		case "repeat":
			return "repeat", nil
		case "round_up":
			return "up", nil
		case "round_down":
			return "down", nil
		}
	}
	return "", Bad("invalid_tie_breaker_mode")
}
