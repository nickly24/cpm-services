# Синтетические шаблоны импорта Tests

Все методы требуют действующего пользовательского Bearer. Организация берётся
из live Auth introspect; доступ к подразделению и его принадлежность организации
проверяются через Core. Студенты не имеют доступа. Никакие реальные вопросы,
ключи существующих тестов, имена или баллы учеников в файлы не экспортируются.
Методы только читают; не создают тесты, импортные сессии, сдачи или результаты.

## JSON / OnlineTestPad

`GET /v1/tests/divisions/{division}/imports/json/template?direction_id=N&format=cpm_json|online_test_pad`

`direction_id` обязателен, положительный int64, активное направление этой
организации. `format` по умолчанию `cpm_json`. Неизвестные и повторяющиеся query
параметры отклоняются. Требуются `upload.view` и `tests.view`, либо полный
администратор организации; право edit для скачивания не нужно.

| format | MIME | Content-Disposition filename |
|---|---|---|
| cpm_json | application/json; charset=utf-8 | primer_importa_testa_cpm.json |
| online_test_pad | text/javascript; charset=utf-8 | primer_importa_testa_online_test_pad.js |

Ответ 200 — чистый файл без `{status,data}`. JSON использует прежние camelCase
поля: title, direction, startDate, endDate, timeLimitMinutes, published, visible,
questions[].questionId/type/text/points/answers[].id/text/isCorrect/correctAnswers.
Три вымышленных вопроса покрывают single/multiple/text. Нативный пример скрыт,
но содержит обязательные демонстрационные даты 2099 года: заменить перед импортом.
Действующий legacy JSON preview требует обе даты даже для скрытого теста.

OnlineTestPad содержит `var test = {Name,Questions:[...]};` с типами 10/210/420.
Это поддерживаемый пример, capability=supported. Файл не исполняется: объект
извлекается и разбирается как JSON. Uploads передаёт в native validate:

```json
{"organization_id":1001,"division_id":1001,"direction_id":1056,"payload":{"importFormat":"online_test_pad","sourceText":"var test = {...};","metadata":{"direction":"Имя выбранного направления","startDate":"2099-01-01T10:00","endDate":"2099-01-01T11:00","published":false}}}
```

Даты и направление для .js задаются в UI/metadata; одного sourceText недостаточно
для успешного preview. JSON файл передаётся объектом payload без обёртки OTP.

`GET /v1/tests/divisions/{division}/imports/json/schema` имеет те же query и права.
Ответ — обычный envelope; data содержит format, capability, organization_id,
division_id, direction_id, filename, mime, file_schema, validate_schema,
commit_schema, notes. Commit дополнительно требует command_id UUID и preview_hash.
Предметные preview/commit по-прежнему требуют edit; схема не расширяет права.

## Внешние результаты XLSX

Основа пути: `/v1/tests/divisions/{division}/external-tests/{24hex}/imports/results`.

| GET suffix | Ответ 200 | Имя файла |
|---|---|---|
| /template | application/vnd.openxmlformats-officedocument.spreadsheetml.sheet | external-test-results-template.xlsx |
| /schema | application/json envelope | без attachment |
| /sample | application/json; charset=utf-8, чистый ExternalImportValidateDTO | external-test-results-sample.json |

Query параметры не допускаются. Требуются `upload.view` и `test-results.view`
(канонический ключ приоритетен над legacy alias), либо полный администратор.
Проверяется живой существующий, неудалённый target именно выбранной org/div;
чужой или удалённый target — 404. Идентификатор target в public Uploads query
преобразуется в native path, а не передаётся как native query.

Один лист «Результаты», заголовки первой строки:

1. ФИО — обязательно, минимум фамилия и имя;
2. Процент правильных ответов (%) — обязательно;
3. Количество правильных ответов;
4. Дата завершения;
5. Потрачено времени;
6. Логин;
7. account_id — optional Auth account;
8. legacy_student_id — optional Core student profile.

Пример содержит только две вымышленные строки с **числовыми** 0 и 87.5.
Процент строго 0..100, null запрещён; 0.9 означает 0.9%, а не 90%.
Uploads сохраняет supplied ID как exact positive int64; большие XLSX ID вводятся
текстом. Неправильный ID не должен теряться или переключать строку на поиск по ФИО.
В native JSON оба поля — optional integer/null. При наличии обоих они должны
указывать на одного ученика текущей org/div. Конфликт на native validate —
HTTP 200, can_commit=false, row.errors code=student_account_mismatch.

Sample содержит текущие organization_id/division_id/test_id/
expected_test_revision и две вымышленные rows; реальные student IDs не подставляются.
Scope/target/revision — внешние поля предметного запроса, не колонки XLSX.
Validate stateless: `/internal/tests/external-results/validate`, service key +
live Bearer. Commit дополнительно требует UUID command_id, preview_hash,
upload_id, upload_session_id, положительные upload_preview_revision и
expected_test_revision. Скачивание/схема не делают commit.

## Транспорт и ошибки

Все ответы `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`.
Файлы имеют Content-Length и `Content-Disposition: attachment; filename="..."`.
Имена фиксированы, пользовательские строки в заголовки не попадают.
Ошибка всегда штатный JSON envelope без Content-Disposition. BFF/Uploads не
должны превращать JSON ошибку в файл; JSON/JS MIME разрешаются только для
зарегистрированного типа tests.json и соответствующего расширения.

Native unit проверки: actual JSON/OTP converter roundtrip, XML/ZIP и нулевой
процент, live авторизация, read-only staff, unknown/duplicate query, чужое
направление/подразделение, deleted/wrong-division target, Mongo read-only,
чистое attachment body. Реальная проверка download→parse→protected preview
выполняется `scripts/import_templates_http_test.py` после Tests READY.

Dev proof 2026-10-09: full Go race/vet PASS; native HTTP 44 запроса,
81 проверка, три Auth сессии закрыты. Report:
`dev/local/tests-import-templates-http-report.json`. Runtime:
`dev/local/tests-template-runtime-report.json`, image
`sha256:27902988ef8fbc74018a2ffeb3af6a5c8f86182ca3c1c4e74670be70d83129c1`.
Хеш запущенного бинарника совпадает с собранным source artifact.
Для Python-проверки требуется openpyxl: использованы готовые bundled pure-Python
пакеты, shared dev venv не изменялся. Legacy XLSX parser проверял старые колонки;
после него в native DTO подставлялись известные IDs собственных вымышленных
fixtures. Проверка чтения новых ID колонок через public Uploads adapter остаётся
отдельной owner acceptance; этот native отчёт не объявляет её выполненной.

Совместная native/public Uploads acceptance затем подтверждена отчётом
`dev/local/uploads-template-http-report.json`: status=passed, 284 проверки,
130 записанных HTTP. Пройдены новые восемь колонок, раздельные account/profile
ID, конфликт без fallback, текстовый ID 9007199254740993 без потери точности,
отказы для небезопасного числового ID, нуля, дроби и повторного заголовка.
JSON/OnlineTestPad/XLSX байты проходят через Uploads без изменения. Import commit,
изменения каталогов/SQL и browser/BFF этим отчётом не проверялись и не выполнялись.
