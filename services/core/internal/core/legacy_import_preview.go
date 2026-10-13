package core

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

func legacyPersonKey(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return ""
	}
	return strings.ToLower(parts[0]) + "|" + strings.ToLower(parts[1])
}
func legacyNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

var legacyDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// Python float(), used by the old parser, accepts decimal Unicode digits and
// underscores between digits; it rejects Go hexadecimal floating literals.
func legacyDecimalText(raw string) string {
	runes := []rune(strings.TrimSpace(raw))
	for i, r := range runes {
		if !unicode.IsDigit(r) {
			continue
		}
		for _, table := range unicode.Digit.R16 {
			if r >= rune(table.Lo) && r <= rune(table.Hi) && (r-rune(table.Lo))%rune(table.Stride) == 0 {
				runes[i] = '0' + (r-rune(table.Lo))/rune(table.Stride)%10
			}
		}
		for _, table := range unicode.Digit.R32 {
			if r >= rune(table.Lo) && r <= rune(table.Hi) && (r-rune(table.Lo))%rune(table.Stride) == 0 {
				runes[i] = '0' + (r-rune(table.Lo))/rune(table.Stride)%10
			}
		}
	}
	for i, r := range runes {
		if r == '_' && (i == 0 || i == len(runes)-1 || runes[i-1] < '0' || runes[i-1] > '9' || runes[i+1] < '0' || runes[i+1] > '9') {
			return ""
		}
	}
	return strings.ReplaceAll(string(runes), "_", "")
}

func legacyClass(raw string) (int, string) {
	if strings.TrimSpace(raw) == "" {
		return 0, "Не указан класс"
	}
	text := legacyDecimalText(raw)
	if !legacyDecimal.MatchString(text) {
		return 0, "Класс должен быть положительным целым числом"
	}
	n, e := strconv.ParseFloat(text, 64)
	if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 1 {
		return 0, "Класс должен быть положительным целым числом"
	}
	if n > 99 {
		return 0, "Класс должен быть от 1 до 99"
	}
	return int(n), ""
}
func legacyRows(in []LegacyImportRow) ([]LegacyImportRow, error) {
	if len(in) < 1 || len(in) > 100 {
		return nil, bad("import_requires_1_to_100_rows")
	}
	seen := map[int]bool{}
	out := make([]LegacyImportRow, len(in))
	for i, row := range in {
		if row.Row < 2 || seen[row.Row] {
			return nil, bad("invalid_or_duplicate_source_row")
		}
		seen[row.Row] = true
		for _, text := range []string{row.FullName, row.ClassRaw, row.SchoolName, row.ProctorName, row.TGName} {
			if !validText(text, 5000, false) {
				return nil, bad("invalid_import_cell")
			}
		}
		row.FullName = strings.TrimSpace(row.FullName)
		row.ClassRaw = strings.TrimSpace(row.ClassRaw)
		row.SchoolName = strings.TrimSpace(row.SchoolName)
		row.ProctorName = strings.TrimSpace(row.ProctorName)
		row.TGName = strings.TrimSpace(row.TGName)
		out[i] = row
	}
	return out, nil
}
func legacyBuildPreview(rows []LegacyImportRow, idx legacyImportIndex, a Actor) LegacyImportPreview {
	out := LegacyImportPreview{Students: []LegacyImportStudent{}, Schools: map[string]LegacyImportSchool{}, Groups: map[string]LegacyImportGroup{}, Proctors: map[string]LegacyImportProctor{}}
	counts := map[string]int{}
	for _, row := range rows {
		if key := legacyPersonKey(row.FullName); key != "" {
			counts[key]++
		}
	}
	for _, raw := range rows {
		row := LegacyImportStudent{LegacyImportRow: raw, PersonKey: legacyPersonKey(raw.FullName), SchoolKey: legacyNameKey(raw.SchoolName), ProctorKey: legacyPersonKey(raw.ProctorName), Action: "create", Errors: []string{}, WithoutGroup: raw.ProctorName == ""}
		if row.PersonKey == "" {
			row.Errors = append(row.Errors, "ФИО ученика должно содержать фамилию и имя")
		}
		if !validText(raw.FullName, 50, true) {
			row.Errors = append(row.Errors, "ФИО ученика должно содержать от 1 до 50 символов")
		}
		if !validText(raw.TGName, 30, false) {
			row.Errors = append(row.Errors, "Telegram должен содержать не более 30 символов")
		}
		if class, err := legacyClass(raw.ClassRaw); err != "" {
			row.Errors = append(row.Errors, err)
		} else {
			row.Class = &class
		}
		if counts[row.PersonKey] > 1 {
			row.Errors = append(row.Errors, "Повтор фамилии и имени ученика в файле")
		}
		matches := idx.Students[row.PersonKey]
		if len(matches) > 1 {
			row.Errors = append(row.Errors, "Неоднозначное совпадение ученика: несколько аккаунтов")
		} else if len(matches) == 1 {
			m := matches[0]
			if !m.Active {
				row.Errors = append(row.Errors, "Совпавший аккаунт ученика неактивен")
			} else if !m.InDivision {
				row.Errors = append(row.Errors, "Совпавший ученик не участвует в выбранном подразделении")
			} else {
				row.Action, row.SkipReason = "skip", "already_exists"
				row.ExistingStudentID, row.ExistingAccountID = &m.ProfileID, &m.AccountID
			}
		}
		// Existing students remain unchanged. Validate entered mappings for honest
		// preview, but materialize related entities only for rows being created.
		var school *LegacyImportSchool
		if raw.SchoolName != "" {
			item := LegacyImportSchool{Key: row.SchoolKey, Name: raw.SchoolName, Action: "create"}
			ms := idx.Schools[row.SchoolKey]
			if !validText(raw.SchoolName, 255, true) {
				row.Errors = append(row.Errors, "Название школы должно содержать не более 255 символов")
			}
			if len(ms) > 1 {
				row.Errors = append(row.Errors, "Неоднозначное совпадение школы")
			} else if len(ms) == 1 {
				if !ms[0].Active {
					row.Errors = append(row.Errors, "Совпавшая школа неактивна")
				} else {
					item.Action, item.ExistingID = "use_existing", &ms[0].ID
				}
			} else if row.Action == "create" && !a.OrganizationAdmin {
				row.Errors = append(row.Errors, "Создание школы требует полного администратора организации")
			}
			school = &item
		}
		var group *LegacyImportGroup
		var proctor *LegacyImportProctor
		if raw.ProctorName != "" {
			row.GroupKey = row.ProctorKey
			p := LegacyImportProctor{Key: row.ProctorKey, FullName: raw.ProctorName, GroupKey: row.GroupKey, Action: "create"}
			g := LegacyImportGroup{Key: row.GroupKey, Name: raw.ProctorName, ProctorKey: row.ProctorKey, Action: "create"}
			if row.ProctorKey == "" || !validText(raw.ProctorName, 50, true) {
				row.Errors = append(row.Errors, "ФИО проктора должно содержать фамилию и имя, не более 50 символов")
			}
			parts := strings.Fields(raw.ProctorName)
			if len(parts) > 1 && !validText("pr"+string([]rune(parts[1])[:1])+parts[0], 50, true) {
				row.Errors = append(row.Errors, "Фамилия проктора слишком длинная для создаваемого логина")
			}
			ms := idx.Proctors[row.ProctorKey]
			if len(ms) > 1 {
				row.Errors = append(row.Errors, "Неоднозначное совпадение проктора: несколько аккаунтов")
			} else if len(ms) == 1 {
				m := ms[0]
				p.Action, p.ExistingID, p.ExistingAccountID = "use_existing", &m.ProfileID, &m.AccountID
				if !m.Active || !m.InDivision {
					row.Errors = append(row.Errors, "Совпавший проктор неактивен или не участвует в выбранном подразделении")
				}
				if m.GroupID > 0 {
					p.ExistingGroupID = &m.GroupID
					g.Action, g.ExistingID = "use_existing", &m.GroupID
					found := false
					for _, groups := range idx.Groups {
						for _, candidate := range groups {
							if candidate.ID == m.GroupID && candidate.Active {
								g.Name, found = candidate.Name, true
							}
						}
					}
					if !found {
						row.Errors = append(row.Errors, "Группа проктора неактивна или вне выбранного подразделения")
					}
				}
			} else if row.Action == "create" && !a.OrganizationAdmin {
				row.Errors = append(row.Errors, "Создание проктора требует полного администратора организации")
			}
			if g.ExistingID == nil {
				gs := idx.Groups[legacyNameKey(raw.ProctorName)]
				if len(gs) > 1 {
					row.Errors = append(row.Errors, "Неоднозначное совпадение группы")
				} else if len(gs) == 1 {
					g.Action, g.ExistingID = "use_existing", &gs[0].ID
					if !gs[0].Active {
						row.Errors = append(row.Errors, "Совпавшая группа неактивна")
					}
				}
			}
			if g.ExistingID != nil {
				if owner := idx.GroupOwners[*g.ExistingID]; owner != 0 && (p.ExistingAccountID == nil || owner != *p.ExistingAccountID) {
					row.Errors = append(row.Errors, "Совпавшая группа уже закреплена за другим проктором")
				}
			}
			if row.Action == "create" && !a.Can("groups", true) {
				row.Errors = append(row.Errors, "Назначение группы требует groups.edit")
			}
			// A person key ignores patronymic; inconsistent group names in one
			// workbook would otherwise make the selected group order-dependent.
			if previous, ok := out.Proctors[row.ProctorKey]; ok && legacyNameKey(previous.FullName) != legacyNameKey(p.FullName) {
				row.Errors = append(row.Errors, "Разные ФИО проктора с одинаковыми фамилией и именем в файле")
			}
			group, proctor = &g, &p
		}
		if len(row.Errors) > 0 {
			row.Action, row.SkipReason = "error", ""
			out.Summary.RowErrors++
		} else if row.Action == "skip" {
			out.Summary.StudentsSkip++
		} else {
			out.Summary.StudentsCreate++
			if row.WithoutGroup {
				out.Summary.StudentsWithoutGroup++
			}
			if school != nil {
				out.Schools[school.Key] = *school
			}
			if proctor != nil {
				p := *proctor
				p.StudentCount = out.Proctors[p.Key].StudentCount + 1
				out.Proctors[p.Key] = p
				g := *group
				g.StudentCount = out.Groups[g.Key].StudentCount + 1
				out.Groups[g.Key] = g
			}
		}
		out.Students = append(out.Students, row)
	}
	out.Summary.TotalRows = len(rows)
	out.Summary.SchoolsTotal, out.Summary.GroupsTotal, out.Summary.ProctorsTotal = len(out.Schools), len(out.Groups), len(out.Proctors)
	for _, item := range out.Schools {
		if item.Action == "create" {
			out.Summary.SchoolsCreate++
		} else {
			out.Summary.SchoolsExisting++
		}
	}
	for _, item := range out.Groups {
		if item.Action == "create" {
			out.Summary.GroupsCreate++
		} else {
			out.Summary.GroupsExisting++
		}
	}
	for _, item := range out.Proctors {
		if item.Action == "create" {
			out.Summary.ProctorsCreate++
		} else {
			out.Summary.ProctorsExisting++
		}
	}
	out.CanCommit = out.Summary.RowErrors == 0 && out.Summary.StudentsCreate > 0
	return out
}
