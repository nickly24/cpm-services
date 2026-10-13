package exams

import (
	"crypto/rand"
	"math/big"
)

type randomPicker func(int) (int, error)

func securePick(n int) (int, error) {
	v, e := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if e != nil {
		return 0, e
	}
	return int(v.Int64()), nil
}

type questionChoice struct {
	question Question
	partID   int64
	cycle    int
}

// Cycles advance only for the selected part. Regular questions never repeat;
// additional questions can reuse earlier questions but cannot repeat the last.
func selectQuestion(a *Attempt, purpose string, onlyPart, exclude int64, pick randomPicker) (questionChoice, error) {
	type pool struct {
		part      Part
		cycle     int
		questions []Question
		weight    int
	}
	pools := []pool{}
	tickets := 0
	last := int64(0)
	if len(a.Presented) > 0 {
		last = a.Presented[len(a.Presented)-1].Question.ID
	}
	for _, part := range a.Parts {
		if onlyPart > 0 && part.ID != onlyPart {
			continue
		}
		if purpose == "regular" && a.Progress[part.ID] >= part.Quota {
			continue
		}
		if purpose == "tie_breaker" && a.Config.TieBreakerSourceMode == "specific_part" && part.ID != a.Config.TieBreakerPartID {
			continue
		}
		cycle := a.Cycles[part.ID]
		if cycle < 1 {
			cycle = 1
		}
		all := []Question{}
		available := []Question{}
		for _, q := range a.Questions {
			u := a.Usage[q.ID]
			if q.PartID != part.ID || q.ID == exclude || u.Excluded {
				continue
			}
			all = append(all, q)
			if purpose == "regular" && !u.Presented || purpose == "tie_breaker" && u.Cycle < cycle {
				available = append(available, q)
			}
		}
		if purpose == "tie_breaker" {
			if len(available) == 0 {
				cycle++
				available = all
			}
			filtered := []Question{}
			for _, q := range available {
				if q.ID != last {
					filtered = append(filtered, q)
				}
			}
			available = filtered
		}
		if len(available) == 0 {
			continue
		}
		weight := 1
		if purpose == "regular" {
			weight = part.Quota - a.Progress[part.ID]
		}
		pools = append(pools, pool{part, cycle, available, weight})
		tickets += weight
	}
	if tickets == 0 {
		return questionChoice{}, unprocessable("question_pool_exhausted")
	}
	ticket, err := pick(tickets)
	if err != nil {
		return questionChoice{}, err
	}
	if ticket < 0 || ticket >= tickets {
		return questionChoice{}, Bad("invalid_random_selection")
	}
	for _, p := range pools {
		if ticket < p.weight {
			index, err := pick(len(p.questions))
			if err != nil {
				return questionChoice{}, err
			}
			if index < 0 || index >= len(p.questions) {
				return questionChoice{}, Bad("invalid_random_selection")
			}
			return questionChoice{p.questions[index], p.part.ID, p.cycle}, nil
		}
		ticket -= p.weight
	}
	return questionChoice{}, Bad("invalid_random_selection")
}
