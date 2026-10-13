package exams

import "time"

// The original exam domain uses a fixed UTC+03 calendar, including historical
// dates. Using a timezone database here would change that legacy rule.
var examMoscow = time.FixedZone("Europe/Moscow", 3*3600)

type ExamDateFilter struct {
	From string
	To   string
}

func validExamDate(value string) bool {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	for i, c := range value {
		if i != 4 && i != 7 && (c < '0' || c > '9') {
			return false
		}
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Year() > 0
}

// ParseExamDateFilter validates the whole period before data is filtered or
// paginated. Public catalogue/results filters allow either end to be omitted.
// The older from_date/to_date spellings are equivalent aliases, but conflicting
// values are rejected rather than silently taking one of them.
func ParseExamDateFilter(query map[string]string) (ExamDateFilter, error) {
	var out ExamDateFilter
	for _, field := range []struct {
		canonical, alias string
		target           *string
	}{
		{"date_from", "from_date", &out.From},
		{"date_to", "to_date", &out.To},
	} {
		value, alias := query[field.canonical], query[field.alias]
		if value != "" && alias != "" && value != alias {
			return out, Bad("conflicting_" + field.canonical)
		}
		if value == "" {
			value = alias
		}
		if value != "" && !validExamDate(value) {
			return out, Bad("invalid_" + field.canonical)
		}
		*field.target = value
	}
	if out.From != "" && out.To != "" && out.From > out.To {
		return out, Bad("invalid_date_to")
	}
	return out, nil
}

func (filter ExamDateFilter) Active() bool { return filter.From != "" || filter.To != "" }
func (filter ExamDateFilter) Contains(date string) bool {
	return validExamDate(date) && (filter.From == "" || date >= filter.From) && (filter.To == "" || date <= filter.To)
}

// ExamDate returns the current operational date in the selected division.
// Completed attempt windows are immutable historical details and do not replace
// the current catalogue date. A bank-only view never invents a connection date.
func ExamDate(e *Exam, divisionID int64) (string, bool) {
	if e == nil {
		return "", false
	}
	d := e.Divisions[divisionID]
	if d == nil {
		return "", false
	}
	switch e.Kind {
	case "classic":
		if d.Config.StartAt == nil {
			return "", false
		}
		return d.Config.StartAt.In(examMoscow).Format("2006-01-02"), true
	case "outside_lms":
		if validExamDate(d.Date) {
			return d.Date, true
		}
	}
	return "", false
}

func (filter ExamDateFilter) Match(e *Exam, divisionID int64) bool {
	if !filter.Active() {
		return true
	}
	date, ok := ExamDate(e, divisionID)
	return ok && filter.Contains(date)
}

func examDateValue(e *Exam, divisionID int64) any {
	if date, ok := ExamDate(e, divisionID); ok {
		return date
	}
	return nil
}

// ExamCatalogDateLess mirrors legacy DATE(Moscow start) ordering with exam ID
// as the stable tie-breaker. Empty/draft dates behave like SQL NULL values.
func ExamCatalogDateLess(a, b *Exam, divisionID int64, ascending bool) bool {
	da, _ := ExamDate(a, divisionID)
	db, _ := ExamDate(b, divisionID)
	if da != db {
		if ascending {
			return da < db
		}
		return da > db
	}
	if a == nil {
		return b != nil && ascending
	}
	if b == nil {
		return !ascending
	}
	if ascending {
		return a.ID < b.ID
	}
	return a.ID > b.ID
}
