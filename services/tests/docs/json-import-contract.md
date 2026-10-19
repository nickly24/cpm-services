# Импорт внутреннего теста из JSON

Tests владеет нормализацией вопросов, проверкой модели и созданием внутреннего теста. Uploads владеет загрузкой файла, preview-сессией, её сроком жизни и подтверждением импорта. Вызовы Tests не создают preview-сессию или черновик. Commit создаёт настоящий тест с начальной неизменяемой версией вопросов и окончательными настройками единственного подключения.

## Методы и права

| Метод | Назначение |
|---|---|
| `POST /v1/tests/divisions/{division}/imports/json/preview` | Предварительная проверка в выбранном подразделении |
| `POST /v1/tests/divisions/{division}/imports/json/commit` | Создание внутреннего теста |
| `POST /internal/tests/imports/json/validate` | Та же предварительная проверка для Uploads |
| `POST /internal/tests/imports/json/commit` | Тот же commit для Uploads |

Публичные методы требуют действующий Bearer, проверенный через живой auth introspect. Внутренние методы дополнительно требуют `X-Service-Key`; один сервисный ключ без Bearer не даёт доступ. Нужны одновременно `tests.view/edit` и `upload.view/edit` либо полный администратор организации. Студент и сотрудник только с просмотром не могут импортировать. Это явное ограничение нового API импорта; в прежнем приложении доступ определялся административным Blueprint.

`organization_id` должен совпадать с организацией токена. `division_id` должен совпадать с подразделением URL и выбранным доступным контекстом сотрудника; полный администратор выбирает подразделение своей организации. Core подтверждает активность подразделения и принадлежность активного направления организации. При недоступности auth или core операция закрывается с `503`. Направление одного теста общее для всех будущих подключений.

## Preview

Обёртка запроса одинакова для публичного preview и внутреннего validate:

```json
{
  "organization_id": 1001,
  "division_id": 1001,
  "direction_id": 1001,
  "payload": {
    "title": "Пример импорта",
    "direction": "Математика",
    "startDate": "2026-10-09T10:00",
    "endDate": "2026-10-09T12:00",
    "timeLimitMinutes": 30,
    "questions": [{
      "questionId": 1,
      "type": "single",
      "text": "Выберите первый вариант",
      "points": 2,
      "answers": [
        {"id": "a", "text": "Первый", "isCorrect": true},
        {"id": "b", "text": "Второй", "isCorrect": false}
      ]
    }]
  }
}
```

`payload` — JSON-объект, не строка с JSON и не multipart-файл. Его размер ограничен 900000 байт. Неизвестные поля внешней обёртки запрещены; неизвестные поля исходного legacy payload не влияют на нормализацию. `direction_id` выбирается явно. Если в файле заполнено `direction`, оно после удаления пробелов по краям должно точно совпасть с текущим названием выбранного направления core. Отсутствующее название дополняется выбранным направлением; другое непустое название даёт ошибку, а не молчаливую замену.

HTTP `200` возвращает стандартную обёртку `{ "status": true, "data": ... }`, в том числе при ошибках содержимого. В `data`:

```json
{
  "valid": true,
  "organization_id": 1001,
  "division_id": 1001,
  "direction_id": 1001,
  "preview_hash": "<64 hex characters>",
  "preview": {
    "title": "Пример импорта",
    "direction_id": 1001,
    "direction_name": "Математика",
    "questions": [{
      "question_id": 1,
      "type": "single",
      "text": "Выберите первый вариант",
      "points": 2,
      "answers": [
        {"id": "a", "text": "Первый", "is_correct": true},
        {"id": "b", "text": "Второй", "is_correct": false}
      ],
      "correct_answers": []
    }],
    "settings": {
      "published": true,
      "start_at": "2026-10-09T07:00:00Z",
      "end_at": "2026-10-09T09:00:00Z",
      "time_limit_minutes": 30,
      "passing_score": null,
      "answers_visible": false
    }
  },
  "summary": {
    "questions_total": 1,
    "total_points": 2,
    "single_count": 1,
    "multiple_count": 0,
    "text_count": 0,
    "errors_count": 0
  },
  "errors": [],
  "source": "json",
  "source_title": "Пример импорта",
  "warnings": []
}
```

Ошибки собираются для всех полей как `{path,message}`; `valid` становится `false`, а `summary.errors_count` равен длине массива. Например, `questions[0].answers[1].isCorrect` указывает на конкретный вариант. При некорректном JSON-типе сохраняются диагностические значения preview; импортировать их нельзя. Ошибка обёртки или прав возвращает обычную ошибку HTTP, без успешного preview.

Native JSON сохраняет прежние правила: даты `YYYY-MM-DDTHH:mm` относятся к московскому времени и нормализуются в UTC; поддерживаются прежние неполные ведущие нули. Конец строго позже начала. `published` по умолчанию `true`, `visible` — `false`, явный `null` использует тот же default. `visible` становится `answers_visible`. Вопросы типов `single`, `multiple`, `text` используют прежние camelCase-ключи; нестроковые и пустые элементы `correctAnswers` отбрасываются. ID, веса и лимит времени должны быть целыми числами JSON, а не строками, boolean или дробными числами. Для вопросов выбора `isCorrect` строго boolean.

Дополнительно применяются ограничения действующей модели Tests: название до 200 символов; максимум 200 вопросов и 400000 байт нормализованного набора; текст вопроса до 4000 символов; 2–50 вариантов до 2000 символов; 1–50 текстовых ключей до 1000 символов; вес 1–100000; уникальные безопасные для JavaScript ID; ID вариантов `A-Za-z0-9_-` длиной 1–128; неповторяющиеся текстовые ключи без учёта регистра. Время 1–1440 минут, даты с 2000 по 2100 год. Исходный файл, который нарушает эти ограничения владельца модели, получает ошибки preview.

## Online Test Pad

Сохраняется формат прежнего JSON import:

```json
{
  "organization_id": 1001,
  "division_id": 1001,
  "direction_id": 1001,
  "payload": {
    "importFormat": "online_test_pad",
    "sourceText": "var test = {\"Name\":\"Тест\",\"Questions\":[...]};",
    "metadata": {
      "title": "Название после импорта",
      "direction": "Математика",
      "startDate": "2026-10-09T10:00",
      "endDate": "2026-10-09T12:00",
      "timeLimitMinutes": 30,
      "published": false,
      "visible": false
    }
  }
}
```

Из `sourceText` извлекается JSON-объект `var test = {...}` с учётом кавычек, экранирования и вложенных скобок. JavaScript не выполняется. `metadata` задаёт реквизиты и настройки; название при отсутствии берётся из `Name`, время — из `Params.timelimitminutes` при `timelimited:true`, иначе 30 минут. Для этого формата отсутствующие `published` и `visible` равны `false`; явный `null` затем проходит прежний native default.

Вопросы и шаблоны ответов стабильно сортируются по `Number`. ID вопросов выдаются последовательно с 1, ID вариантов — `a…z, aa…`. `AnswerType` 10 преобразуется в `single`, 210 в `multiple`, 410 и 420 в `text`; неизвестный тип блокирует commit. HTML очищается, сущности раскрываются, разрывы строк сохраняются. Вес берётся из первого истинного `maxScore`, затем `RightScore`, затем 1; дробь округляется по Python-правилу к ближайшему чётному при равной дистанции и ограничивается снизу единицей.

Если у любого шаблона положительный `Score`, правильность всех шаблонов определяется через `Score > 0`; иначе через `IsRightAnswer:true`. У числового текстового ответа 410 сначала используются `ValueInt` и `ValueFloat`, дробь нормализуется; исходный числовой ноль, включая `-0`/`-0.0`, пропускается, но строковый/HTML fallback `"0"` принимается, как в legacy. Буквальное строковое `ValueInt:"-0"` остаётся текстовым ключом; `ValueFloat:"-0.0"` проходит числовую нормализацию и тоже уступает fallback. Текст 420 и fallback 410 разделяют варианты по `<или>` без учёта регистра и удаляют буквальные повторы. Лимит случайных вопросов не сокращает набор, флаги случайного порядка выдаются предупреждениями; основной Tests перемешивает вопросы при старте попытки и сохраняет исходный порядок вариантов.

Ответ имеет `source: "online_test_pad"`, `source_title` из исходного `Name` и предупреждения legacy-конвертера. Неизвестные типы, отсутствующие правильные ответы, повреждённый `test.js` и нарушения модели входят в общий `errors`.

## Commit и повторы

Commit принимает ту же обёртку с неизменным исходным `payload` и дополнительными полями:

```json
{
  "organization_id": 1001,
  "division_id": 1001,
  "direction_id": 1001,
  "payload": {"title": "...", "questions": []},
  "command_id": "11111111-1111-4111-8111-111111111111",
  "preview_hash": "<hash returned by preview>"
}
```

`command_id` — UUID, одна новая команда на один подтверждённый импорт. `preview_hash` — SHA-256 канонического preview вместе с организацией, подразделением, направлением, текущим названием направления, нормализованными вопросами, настройками, ошибками и предупреждениями. Поле самого хеша при расчёте пустое. Время запроса в него не входит. Изменение названия направления или payload требует нового preview.

До первого создания Tests повторяет живую проверку прав/core, нормализацию, проверку всех ошибок и сравнение хеша. Некорректный preview даёт `400 json_import_validation_failed`, устаревший хеш — `409 json_import_preview_changed`.

Успешный ответ `201` содержит `data: {command_id, test: <TestView>}`. Тест сразу имеет `revision:1`, владельца в выбранном подразделении, одно подключение с финальными импортированными настройками, неизменяемую начальную версию вопросов и историю её ID. Банк выключен, описание пустое, черновик не создаётся. Публикация сохраняется из payload; native default может создать опубликованный тест сразу.

Tests использует существующий долговечный механизм create: стабильный ID из организации, аккаунта и `json-import-<command_id>`, а `operation_hash` связывает канонический payload, preview-хеш, направление и scope с маршрутом commit. Пробелы и порядок ключей JSON не меняют команду. Публичный и внутренний commit имеют одинаковую идентичность команды. Повтор с тем же содержимым возвращает `200`, тот же ID и текущий `TestView`; требуется прежнее право редактирования. Другая нагрузка, направление или подразделение с тем же `command_id` даёт `409 idempotency_conflict` до записи новой версии, включая повтор с теперь некорректным содержимым.

Начальная версия записывается штатным `initialContent`, после неё одной вставкой каталога сохраняются содержимое, ссылки версии и готовые настройки. До этой вставки живого частично импортированного теста нет. При сбое вставки или одновременных одинаковых командах может остаться недоступная неизменяемая версия, как у штатного создания; повтор выбирает единственный документ каталога. Долговечный `operation_hash` находится в документе теста, поэтому перезапуск процесса не создаёт новую копию.

## Проверки

`json_import_test.go` проверяет native DTO/defaults/даты/все ошибки, OTP-HTML/сортировку/приоритет Score/округление/нулевые и дробные текстовые ответы/предупреждения, стабильность preview и команд, наличие финальных настроек и вопросов в первой вставке, повтор без новых записей, конфликт изменённого некорректного payload, права и отсутствие авторизации по одному сервисному ключу.

Режим `json_import_http_test.py --negative-zero-only` создаёт один отдельный тест с исходными JSON-токенами `ValueInt:-0`, `ValueFloat:-0.0` и строкой `ValueInt:"-0"`. Он использует активное направление организации 1001 из `fixtures.directions` отчёта `dev/local/external-tests-integration-summary.json`, без создания, переименования или архивирования этого направления; архивируется только новый собственный тест. Проверяются preview, commit, сохранённые вопросы и content API; отчёт — `dev/local/tests-json-import-negative-zero-http-report.json`. Регрессия сопоставлена с шестью случаями чистых функций старого Python-конвертера, включая запасные текстовые/HTML ответы и отсутствие fallback.

Проверка владельца после добавления контракта: `go test -race ./...` и `go vet ./...` в `services/tests`. После подъёма текущего бинарного образа выполняется `dev/.venv/bin/python services/tests/scripts/json_import_http_test.py`. Сценарий создаёт вымышленные тесты и направление только через owner API, проверяет public/private preview, commit, повторы, параллельное создание, права, смену направления и сохранённые документы/версии через read-only Mongo. В конце он архивирует свои fixtures через API и сохраняет отчёт `dev/local/tests-json-import-http-report.json` без секретов. Прямых записей в БД и обращения к production нет. Unit-прохождение само по себе не подтверждает живую интеграцию Uploads/BFF.

## Excel question tables

The payload `importFormat=question_table` carries raw `rows` (physical `row`,
`title`, `number`, `category`, `question`, `options`, `answer`, `note`) and the
same metadata fields as OnlineTestPad. Uploads extracts bounded workbook cells;
Tests converts and validates them. Each question weighs one point; source IDs
are preserved. Choice markers may be Cyrillic/Latin letters or numbers, separated
by newlines or at least two spaces. Exact unique answer-text matches can repair
wrong labels with a physical-row warning; unmatched keys block confirmation.
Text aliases accept `/` and quoted alternatives after `Допустимо:`. Dates from
Excel date cells remain date strings. Duplicate option texts, inferred types and
changed source titles are visible warnings. Native hashing, current permissions,
Core references, grant binding, atomic creation and idempotency also cover this
source format. Uploaded JavaScript is still parsed as literal JSON and never run.
