# Core: предметный импорт пользователей старого формата

Срез: 9 октября 2026. Реализован и проверен живым native HTTP отдельный Core контракт `users.legacy`; существующий импорт готовых учётных данных `/imports` сохранён. Новая миграция `006_core_legacy_imports.sql` следует за согласованной `005_core_scope_guards.sql`; обе применены координатором в синтетическом `cpm_dev`, актуальный Core отвечает на 8003. Общий Uploads и его Rabbit workflow этим изменением не объявляются завершёнными.

## Исходный формат

Прочитаны старые `cpm-back-main/cpm_back/services/user_import/{parse_excel,person_name,school_lookup,build_preview,import_runner,sessions}.py` и `blueprints/user_import_bp.py`. Parsed row передаётся в Core **без ID справочников, роли, логина и пароля**:

```json
{
  "row": 2,
  "full_name": "Иванов Иван Иванович",
  "class_raw": "7.0",
  "school_name": "Школа №1",
  "proctor_name": "Петров Пётр Петрович",
  "tg_name": "ivan"
}
```

Workbook: активный лист, значения ячеек, первая строка — заголовки; полностью пустые строки пропускаются. Номер `row` сохраняет исходную строку листа и начинается с 2. Нормализация заголовка: trim, lowercase, `ё → е`, схлопывание пробелов. Первый совпавший столбец используется при повторе заголовка. Обязательны ФИО и Класс.

| Поле parsed row | Старые алиасы заголовка |
|---|---|
| `full_name` | `фио`, `фио ученика`, `ученик`, `студент`, `full_name`, `имя`, `ф.и.о`, `ф.и.о.` |
| `class_raw` | `класс`, `class` |
| `school_name` | `школа`, `school` |
| `proctor_name` | `проктор`, `proctor`, `куратор` |
| `tg_name` | `telegram`, `телеграм`, `tg`, `tg_name`, `ник telegram` |

Отдельной колонки группы в старом формате **нет**. Новая группа получает полное ФИО проктора. При существующем прокторе с закреплённой группой используется эта группа, даже когда её название отличается от ФИО. Core использует текущие `auth_division_access` / `core_proctor_groups`, не меняет старое глобальное поле профиля проктора и не переносит его из существующей группы.

Совпадение человека — фамилия и имя, первые два непустых токена, lowercase; отчество не участвует. Для имён не вводится замена `ё` на `е`. Школа и название группы сопоставляются по полному имени после trim и lowercase; внутренние пробелы не схлопываются, краткое название школы не подменяет полное. Класс сохраняет старую числовую семантику Python `float`: допустимы целые `7`, `7.0`, `7e0`, десятичные Unicode цифры и `_` между цифрами; дробные, пустые и неположительные значения — ошибки.

## Native HTTP контракт для BFF / Uploads

База: `/v1/core/divisions/{division}/legacy-imports`. Параметры `{division}` и `{import}` — положительные числовые ID. Обычная cookie-сессия BFF вызывает native Core с текущим Bearer. Фоновые execution grants и Rabbit обработка в этом контракте пока не подтверждены.

| Метод | Путь | Тело / заголовок | Результат |
|---|---|---|---|
| POST | база | `{rows: LegacyImportRow[]}`; обязательный `Idempotency-Key: <lowercase UUID>` | `201`, полный `LegacyImportSession` |
| GET | `/{import}` | — | `200`, текущий `LegacyImportSession` |
| PUT | `/{import}` | `{expected_revision, rows: LegacyImportRow[]}`; обязательный UUID | `200`, исправленный полный snapshot, revision + 1 |
| POST | `/{import}/commit` | `{expected_revision}`; обязательный UUID | `200`, completed session, revision + 1, безопасный отчёт |

PUT заменяет весь raw snapshot. Это не `{changes}` и не старый `{preview:{students}}`. До новых подтверждений не передавать `group`, `school_id`, `account_id`, `role`, `organization_id`, credentials или произвольные preview поля: неизвестные поля, null и повтор JSON ключа отклоняются. Ограничение JSON native legacy запроса — 2 MiB; `row` должен быть уникальным и ≥ 2.

Core envelope: `{status:true,data:LegacyImportSession}`. Внутри `data`:

- `id`, `organization_id`, `division_id`, `actor_id`, `revision`, `max_rows:100`;
- `status: "preview" | "completed" | "expired"`, `created_at`, `expires_at`;
- `preview: {students, schools, groups, proctors, summary, can_commit}`;
- `report: null | {created:[{account_id,profile_id,role,full_name,row?}],created_students,created_proctors,created_schools,created_groups,skipped_students,credentials_policy:"reset_required"}`.

`students` содержит исходные parsed поля, числовой `class` или null, `person_key`, `school_key`, `proctor_key`, `group_key`, `action:create|skip|error`, `errors:string[]`, `skip_reason?`, `existing_student_id` (profile ID), `existing_account_id` (account ID), `without_group`. Related maps используют строковые ключи сопоставления и `action:create|use_existing`; proctor map различает `existing_id` (profile) и `existing_account_id`.

`summary` сохраняет имена старых счётчиков: `total_rows`, `row_errors`, `schools_total/create/existing`, `groups_total/create/existing`, `proctors_total/create/existing`, `students_create/skip/without_group`. Related maps и счётчики отражают только необходимые для создаваемых учеников сущности. Пропущенные ученики не вызывают создание неиспользуемой школы/группы/проктора.

`preview.can_commit` — обязательный authoritative guard. Ошибки строк допустимы в сохранённом preview, но commit запрещён. Если все ученики уже существуют, `students_create=0`, `can_commit=false`; это отдельный честный результат, а не успешная пустая запись.

### Ошибки и восстановление

| Ситуация | Native результат |
|---|---|
| Нет живого разрешения / не свой division для scoped staff | `403` |
| Не свой actor/org/div session | `404` |
| Неверный/отсутствующий UUID, неоднозначный JSON, scope/secret поля, неверные source row, > 100 строк | `400` |
| Неверный Content-Type | `415 json_required` |
| Один UUID с другим canonical command | `409 idempotency_key_reused` |
| Новая команда с устаревшей revision | `409 stale_revision` |
| Есть ошибки / нет создаваемых учеников | `409 legacy_import_not_ready` |
| Изменилось сопоставление в Core или права между preview и apply | `409 legacy_import_preview_changed` |
| Попытка новой команды для завершённой/просроченной session | `409 legacy_import_completed` / `legacy_import_expired` |

После потерянного ответа повторять **тот же UUID и то же тело**. Receipt проверяется раньше CAS и сопоставления; изменение revision/тела под прежним UUID не является повтором. Временная потеря прав не удаляет ранее принятый receipt: после восстановления доступа оригинальная команда вернёт исходный результат. До подтверждения результата не создавать новую session и не генерировать новый UUID молча.

Если commit вернул `legacy_import_preview_changed`, прочитать session, показать конфликт и обновить snapshot через PUT с текущей revision и новым UUID для новой правки. Повтор не может незаметно присоединить ученика к другому подразделению или переименовать/переназначить существующую группу.

## Область доступа и применение

- Создание/правка/commit: одновременно живые `upload.edit` и `users.edit`; чтение требует view обоих разделов. Session всегда привязана к исходному actor/account, organization и division, включая полного администратора.
- Назначение группы дополнительно требует `groups.edit`. Создание школы и проктора — только organization admin; импорт не повышает привилегии scoped editor.
- Школы читаются в текущей организации, группы — в выбранном подразделении. Люди сопоставляются внутри членства текущей организации. Совпавший человек только в другом подразделении даёт row error; импорт не выдаёт доступ сам.
- Неактивные, неоднозначные совпадения и повторы фамилии/имени внутри файла — row errors. Core не повторяет старое поведение «взять первый match».
- Уже существующие ученики в текущем подразделении получают `skip/already_exists`; их профиль, членство и история не изменяются.
- Начальное хеширование происходит до subject transaction. После него Core ещё раз читает живую identity и проверяет actor/context/права. В транзакции повторно проверяются receipt, revision и актуальный план.
- Школы, группы, новые аккаунты/профили и связи, session completion, receipt и аудит сохраняются **одной MySQL транзакцией**. Любая поздняя ошибка откатывает всё; это не старый Flask apply с компенсирующим удалением частичных данных.
- Пароли не принимаются этим форматом. Случайный bootstrap password существует только в памяти и запросе Auth Hash; в session, отчёте, receipt и журнале его нет. Хеш хранится только в штатной приватной таблице Auth. Перед выдачей доступа полный администратор устанавливает известный пароль через существующее действие credentials.

Core/Auth по-прежнему пишут общую MySQL в согласованном dev этапе (`insertUser`). Эта реализация не утверждает завершённое разделение хранилищ или фоновые capabilities.

## Импорт готовых credentials сохранён

Существующие `/v1/core/divisions/{division}/imports` JSON/CSV/XLSX, `UserDTO` и commit не заменены `users.legacy`. POST preview теперь принимает **необязательный** lowercase UUID `Idempotency-Key`:

- тот же parsed input + UUID возвращает прежний `{id,rows}`, в том числе после завершения account creation;
- другая команда под тем же UUID — `409 idempotency_key_reused`;
- приватный payload с Auth hash остаётся в существующем `core_imports`, receipt содержит только digest и ссылку;
- `GET /v1/core/divisions/{division}/imports/receipts/{receipt}` возвращает только `{session_id,status,created_at}` с привязкой actor/org/div и view обоих разделов;
- POST без UUID сохраняет прежний контракт. Existing GET по session продолжает маскировать password/hash, а commit возвращает сохранённый результат при повторе.

## Проверки и остатки

Новые тесты: `internal/core/legacy_import_test.go`, 14 top-level regression сценариев (дополнительно 4 subtests): старый формат сопоставления/класса, ambiguity/duplicate/inactive/foreign division, отсутствие тихих grants, сохранение закреплённой группы, scoped permissions, zero creates, строгий JSON, command digest/CAS, lost reply → permission rejection → rights restored, поздний SQL rollback, expiry/privacy, credentials receipt после apply.

`go test -race ./...` и `go vet ./...` пройдены после регистрации маршрутов. **Native HTTP: 140 проверок PASS** после подтверждённого deploy/migrations координатором. Evidence — `dev/local/core-legacy-import-http-report.json`, безопасный demo — `dev/local/core-legacy-import-demo.json`. Проверены preview row errors, edit CAS, два одновременных commit одного UUID, durable completed GET и account/profile mapping, proctor/group/school graph, existing skip, ambiguous matches, чужие actor/org/div, изменившийся после preview Core plan, живой отзыв прав и восстановление прежнего UUID. Ответ успешного scoped commit специально закрывается до чтения тела; после временного отзыва и восстановления прав recovery возвращает тот же созданный аккаунт. Отдельный credential preview проверен с UUID и без него, повтор — после завершённого создания аккаунта, receipt GET — без credentials и для запрещённого actor/org.

Повторяемый скрипт `scripts/legacy_import_http_test.py` создаёт **свежие** synthetic сущности через публичные API и оставляет demo; прямого SQL и управления runtime в нём нет. Он пишет отдельные assertions HTTP/status/envelope/privacy/бизнес-поведения в отчёт; число 140 означает проверки, не 140 самостоятельных workflow. `scripts/legacy_import_setup.py` отдельно применяет только additive 006 в фиксированном `cpm_dev`, проверяет prerequisite 005/metadata и при применении читает текущий `extensions.json` под lock, добавляя только свои три названия таблиц. Этот helper выполнял координатор, не HTTP runner.

Ограничения, которые нельзя объявлять полной legacy parity:

1. **100 raw rows на native session**, явно `max_rows:100`. Старый parser не ограничивал файл. Для общего большого импорта нужен согласованный staged hashing ≥ 1000 с private durable hash phase, worker/capability live validation, таймингами и нагрузочной проверкой; тихое разрезание файла на неатомарные части не эквивалентно этому контракту.
2. Core валидирует ФИО ≤ 50, TG ≤ 30, школу ≤ 255, класс 1..99, login ≤ 50. Старая Python проверка класса разрешала любое положительное целое; новое ограничение видно row error.
3. Старый plaintext Excel password report не воспроизводится. Report сообщает `reset_required`; отдельный защищённый credential delivery workflow не реализован.
4. Actor capabilities, Rabbit execution, общий Uploads UI/template/parser и BFF integration принадлежат другим владельцам. Проверка native Core не означает успешный общий import lifecycle.
