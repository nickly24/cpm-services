package tests

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// Fixed synthetic questions only. The selected direction is presentation data,
// never authorization or a replacement for the import's selected target.
func questionTableWorkbook(direction string) ([]byte, error) {
	rows := [][]string{
		{"Отрасль (сокращение)", "Название теста", "Первоисточник", "Номер", "Категория вопроса", "Вопрос", "Варианты ответов", "Правильный ответ", "Примечание к ответу"},
		{direction, "Пример Excel — замените вопросы", "Пример", "1", "Тест с одним вариантом ответа", "Пример: выберите первый вариант", "А. Первый\nБ. Второй", "А. Первый", ""},
		{direction, "Пример Excel — замените вопросы", "Пример", "2", "Тест с несколькими вариантами ответа", "Пример: выберите первый и второй варианты", "А. Первый\nБ. Второй\nВ. Третий", "А. Первый\nБ. Второй", ""},
		{direction, "Пример Excel — замените вопросы", "Пример", "3", "Напишите слово/словосочетание", "Пример: введите слово «пример»", "", "пример / example", "Допустимо: «образец»"},
	}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i, row := range rows {
		fmt.Fprintf(&sheet, `<row r="%d">`, i+1)
		for j, text := range row {
			var escaped bytes.Buffer
			if err := xml.EscapeText(&escaped, []byte(text)); err != nil {
				return nil, err
			}
			fmt.Fprintf(&sheet, `<c r="%c%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, 'A'+j, i+1, escaped.String())
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	return importWorkbook("Вопросы", sheet.String())
}
