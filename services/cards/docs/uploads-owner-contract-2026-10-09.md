# Cards → Uploads: native owner contract, 9 октября 2026

Base URL в dev: `http://cards:8008`, с host: `http://127.0.0.1:8008`. JSON: `{status:true,data:...}`; ошибка: `{status:false,error:"..."}`. Все бизнес-запросы требуют Bearer и живую проверку Auth + Core. Контракт также доступен в `/openapi.json`.

## Область и права

Cards хранит темы, карточки, настройки и прогресс в области организации. Выбранное подразделение не создаёт отдельный каталог; Core возвращает все активные направления организации. Сессии импорта и квитанции дополнительно принадлежат аккаунту. Чужой аккаунт, даже admin той же организации, не получает чужой preview/source/receipt.

Native Cards требует `train.view` для справочника и шаблона, `train.edit` для parse, edit, commit, session, source и receipt. Сохранены прежние aliases `cards`/`training`. Transform дополнительно требует `tests.edit` при создании, чтении, commit и восстановлении; его подразделение назначения фиксируется из подтверждённого Auth-контекста. Admin ограничен своей организацией.

Uploads дополнительно проверяет **upload** view/edit согласно действию; право `train` не выдаёт право пользоваться Uploads. Эта дополнительная проверка остаётся и на скачивании исходного файла. Native train-поведение сохранено для существующего прямого Cards UI. Во всех общих Uploads job/session остаётся исходное подразделение, даже когда данные Cards общие для организации.

Прямое согласие на новую авторизацию фоновых импортов получено 9 октября: human message `01a12221-7d06-73f3-8f27-8c05f352977a`. Прежнее ожидание согласия снято; нового human stop нет. Реализация `cards.xlsx` идёт по [owner enforcement contract](background-import-enforcement.md): canonical proof и strict request/config guards уже компилируются и проходят unit/race/vet, exact Auth/Core wire handshake согласуется. Runtime/keys/DDL не изменены; native приёмка ниже не является end-to-end проверкой нового worker apply. `cards.transform` исключён из начального фонового allowlist до отдельного двух-owner протокола. Пользовательский Bearer не сохраняется в job, grant не превращается в неограниченную сессию. Развёртывание требует final freeze, согласованного окна и ACK зависимых владельцев; общий QA RELEASE отдельный.

## Справочник, шаблон и исходный файл

| Метод | Результат |
|---|---|
| `GET /v1/cards/imports/contract` | Формат `cards.xlsx`, версия 1, заголовки, лимиты, ссылки на endpoints |
| `GET /v1/cards/imports/targets` | `{organization_id,directions:[{id,name}],themes:[{id,direction_id,name,revision}]}`; только ручные темы и активные направления текущей организации; не вызывает Tests |
| `GET /v1/cards/imports/template` | Бинарный XLSX с одной строкой `Вопрос`, `Ответ`, MIME `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, attachment `cards-template.xlsx` |
| `GET /v1/cards/sessions/{session}/source` | `{session_id,organization_id,direction_id,source_sha256,source_filename,source_sheet,content_type,retains_source_bytes:false}` для собственной доступной import-сессии |

Cards не сохраняет исходные XLSX-байты. Их хранение, TTL и download принадлежат Uploads. Перед download Uploads проверяет свой upload scope и права, затем доступность owner session/source и соответствие сохранённого file hash. Источник старого preview, созданного до этого дополнения, может не иметь hash; это не доказательство соответствия произвольного файла. Metadata не содержит Bearer, credentials или локальных путей. Все ответы `Cache-Control: private, no-store`.

## Команды и восстановление после потери ответа

Optional `Idempotency-Key` поддерживается на CRUD тем/карточек, parse, edit preview, commit и transform. Uploads должен всегда сохранять и передавать один ключ для одной логической команды. Ключ — UUID либо ASCII `[A-Za-z0-9:_-]`, 8–128 символов. Обычные ключи чувствительны к регистру; валидные UUID нормализуются к нижнему регистру для совместимости ранее выданных квитанций. Детерминированный UUID у адаптера подходит.

Ключ изолирован по `(organization_id, account_id)`. Другая операция или другое содержимое под прежним ключом возвращают `409 idempotency_conflict`. Запись данных и результата команды выполняется в одной SQL-транзакции; параллельные повторы сериализуются.

- **Parse:** `POST /v1/cards/imports/parse`, multipart `file,direction_id` и либо `theme_id`, либо `create_new_theme=true,new_theme_name`. Идентичность = SHA256 исходных байтов + значения target. Multipart boundary и filename не влияют на идентичность; в результате сохраняется имя первого запроса. Повтор возвращает тот же session ID и первоначальный DTO preview.
- **Edit:** `PUT /v1/cards/sessions/{session}`, JSON `{expected_revision,cards:[{row,question,answer,skip}]}`. Требуются все исходные строки, target не меняется. Одна команда увеличивает revision один раз; повтор со своим ключом возвращает первоначальный результат даже после более позднего edit.
- **Commit:** `POST /v1/cards/sessions/{session}/commit`, `{expected_revision}`. Повтор возвращает те же card IDs или draft ID. Сама session+revision также сохраняет прежнюю идемпотентность при отсутствии ключа.
- **Transform:** `POST /v1/cards/transforms`, `{theme_id,card_ids:[...]}`. Snapshot закреплён за подразделением назначения. Повтор не создаёт второй preview. Commit использует стабильный downstream key `cards_{session_id}`; потеря локального подтверждения после успеха Tests не создаёт второй draft.

`GET /v1/cards/receipts/{key}` возвращает:

```json
{
  "status": true,
  "data": {
    "state": "completed",
    "operation": "import:parse",
    "request_hash": "<64 lower-case hex characters>",
    "organization_id": 1001,
    "direction_id": 123,
    "session_id": "<32 hex characters>",
    "resource_id": "<same session id>",
    "result": {"session_id": "<same session id>", "revision": 1}
  }
}
```

`result` в примере сокращён: реальный результат полностью совпадает с DTO первоначально выполненной команды. `request_hash` — SHA256 сериализованного native DTO (для parse DTO содержит file SHA256), серверное доказательство неизменности запроса, а не номер ревизии. Операции: `import:parse`, `session:update:{id}`, `session:commit:{id}`, `transform:division:{id}`. Для CRUD session/resource ID отсутствуют.

Алгоритм адаптера: сохранить ключ и исходный запрос → отправить → при неизвестном результате читать receipt → использовать его `result`/session ID → отдельно читать session для текущей ревизии. **404 receipt не доказывает отсутствие выполняющейся команды:** безопасно повторить тот же исходный запрос с тем же ключом. Нельзя создавать новый ключ или считать любой `revision+1` подтверждением своего edit. Ответ 503 сохраняет неопределённость и требует такого же восстановления.

Каждое чтение receipt/replay заново проверяет права, активное направление, принадлежность и доступность session; transform также `tests.edit` и division. Preview истекает через 72 часа; completed session продолжает поддерживать восстановление. Квитанции содержат исходный результат, поэтому не заменяют GET текущей session и не обходят её ограничения.

## Формат и ошибки

XLSX: активный лист; заголовки `Вопрос`, `Ответ`; максимум 1000 строк данных, 10 MiB весь HTTP request, 32 MiB распакованный архив. Вопрос ≤4000 символов, ответ ≤16000. Пустая строка отвергается; пустой ответ становится исправимой row error. Многострочный текст и пустые строки внутри ячейки сохраняются, при commit обрезаются только наружные пробелы. Повторы — warnings; `skip` явный. Transform ≤200 уникальных карточек, ответ ≤1000 символов из-за лимита одного Tests text alias; ответ не дробится.

400: malformed DTO/key/file, row validation; 401: недействующая сессия; 403: нет текущего права/неверный transform division; 404: чужой/отсутствующий/истёкший preview или недоступное направление; 409: revision/idempotency conflict; 415: нужен JSON; 503: зависимость недоступна. Повторяемые ошибки не подменяются пустым успешным результатом.

## Проверка

`go test -race ./...`, `go vet ./...`, обычная Docker build/recreate только Cards. Реальные HTTP Auth/Core/Tests/MySQL, только синтетические dev данные. Скрипты:

```sh
dev/.venv/bin/python services/cards/scripts/http_test.py
dev/.venv/bin/python services/cards/scripts/uploads_contract_test.py
```

Отчёты без credentials: `dev/local/cards-http-report.json`, `dev/local/cards-uploads-contract-http-report.json`. Второй проверяет native owner contract, а не готовность ещё согласуемого Uploads worker grant.

Проверено: **227** новых HTTP/assertion проверок native Uploads контракта и **187** проверок основной приёмки после перевода revoke на отдельные account/role. Новая приёмка охватывает скачанный XLSX с заполнением и обратным parse, конкурентный parse/commit, точные квитанции после более поздних edits, foreign org/account, org-wide import при смене division, pinned transform, живой отзыв train.edit/tests.edit на собственных уникальных staff/role, настоящий Tests draft и SQL-подтверждение отсутствия дублей. Независимый read-only review новых checks доступа к receipts не выявил дефектов.
