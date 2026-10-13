# Tests: фоновые импорты после подтверждения пользователя

Прямое разрешение проверено в main thread
`01a10598-5c52-7dd1-ad4b-95ba33bae44c`, userMessage
`01a12221-7d06-73f3-8f27-8c05f352977a`: обработчик может сохранять подтверждённый
импорт через очередь только в указанной организации/подразделении, с проверкой
актуальных прав пользователя. Это разрешает реализацию, но не объявляет runtime
или общий QA готовыми. Auth/Core/BFF/UI пишет их владелец; Tests — только этот сервис.

Контракт [Auth Rev2](../../auth/docs/import-operation-grants-v1.md) заморожен,
SHA256 `4aeeccb8497777f1b0fac218bebc52f69dcd162b36683fc3a7756ef5e7bc92bd`.
Реализация ниже относится к исходникам кандидата Tests/OpenAPI 0.5.0.
Наличие исходников и unit PASS не означает, что включены ключи, новый runtime,
Auth schema или пройден полный Uploads/Rabbit/UI сценарий.

## Native операции и неизменные данные

| Тип | Preview | Apply | Receipt-only |
|---|---|---|---|
| tests.json, включая OTP | POST /internal/tests/imports/json/validate | POST /internal/tests/imports/json/commit | POST /internal/tests/imports/json/receipt |
| tests.external-results | POST /internal/tests/external-results/validate | POST /internal/tests/external-results/commit | POST /internal/tests/external-results/receipt |

Query/ForceQuery не допускаются. Grant не добавляет поля в бизнес DTO. Preview
использует существующий Validate DTO. Apply и receipt используют **одинаковые
исходные bytes Commit DTO**, включая прежний command_id UUID в body. При recovery
меняется только разрешённый route/stage; receipt handler никогда не вызывает apply.

JSON: org/div/direction int64, payload raw JSON; commit дополнительно command_id
и preview_hash. Native session, owner preview revision и target ID отсутствуют;
Uploads synthetic session/revision не становятся сессией Tests. External: org/div,
target24hex, expected_test_revision (для фоновой операции обязательно положительная),
rows с раздельными optional account_id/legacy_student_id int64, процентом 0..100;
commit также содержит прежние preview_hash, command_id, upload_id,
upload_session_id, upload_preview_revision. Нет нового endpoint общего CRUD.

## Владелец вычисляет proof

Preview получает additive `owner_proof`:

```json
{"schema_version":1,"scheme":"tests.json.v1","input_sha256":"64hex","validation_sha256":"64hex","mapping_sha256":null}
```

External scheme — `tests.external-results.v1`, mapping_sha256 — 64hex.

- `input_sha256`: SHA256 нативного canonical JSON **Validate DTO**; для external
  ID нормализуется до bare lowerhex, строки — прежним externalImportRequest.
- `validation_sha256`: существующий `preview_hash` без изменения алгоритма.
  Сам `owner_proof` исключён из этого хеша, чтобы не было рекурсии и чтобы
  существующие предметные команды сохранили прежнюю семантику.
- `mapping_sha256`: SHA256 canonical `{organization_id,division_id,test_id,
  revision,rows:[{row_id,account_id,legacy_student_id,student_full_name,candidates}]}`.
  Row order исходный; кандидаты отсортированы по account_id; отсутствующие ID
  сериализуются null, candidates всегда массив. Это отдельный fingerprint
  результата сопоставления в Core, а не source-файл или поле процентов.

Native canonical JSON: ключи объектов сортируются рекурсивно, UTF-8 без HTML
escaping, массивы сохраняют порядок. JSON payload number lexemes сохраняются;
типизированные int64 не проходят через float64. Для DTO optional поля действуют
нативные omitempty, поэтому null/отсутствие не следует произвольно заменять.
Ownerproof не заменяет wire_body_sha256 фактических HTTP bytes (включая LF,
пробелы и escape sequences), source_sha256 или Uploads wrapper/preview hash.

## Enforcement и recovery

Carrier ровно по одному `X-CPM-Operation-Grant` (32hex),
`X-CPM-Operation-Job` (32hex), `X-CPM-Operation-Stage`
(preview/apply/receipt), `X-CPM-Operation-Service-Key` и
`Idempotency-Key` (исходный canonical lowercase UUID на всех этапах).
При grant запросе нет Bearer, legacy X-Service-Key, prepare key или scope-selector
headers. Public Bearer и прочие internal endpoints не принимают grant carrier.
Метод, route без encoded/dot aliases, query/ForceQuery, exact DTO names,
дубли JSON на любой глубине и точные байты проверяются до native записи.

Tests каждый раз POST напрямую в
`/internal/auth/operation-grants/{grant}/authorize` своим
`AUTH_OPERATIONS_TESTS_KEY`, без Bearer. Incoming key — отдельный
`TESTS_OPERATION_DELIVERY_KEY`. Два отсутствующих ключа отключают функцию;
частичная конфигурация, короткие или равные друг другу/legacy ключи запрещают
startup. Sole writer provisioning — Auth/BFF. Overlay
`services/tests/dev-operation-overlay.yaml` содержит только два Tests environment
hooks; его явно подключают после согласованного provisioning/runtime окна.
Общий файл чужих owner credentials в контейнер Tests не монтируется.

Auth response — прямой Rev2 envelope, не status/data wrapper. Обязательные null
поля присутствуют явно; wrong-case, unknown, duplicate, неверные типы и null
для ненулевых metadata отклоняются. Native `actor.id` — реальный профиль,
`account_id` — отдельный реальный аккаунт, они не обязаны совпадать. Actor сохраняет
выданные Auth роль/права; origin membership и origin division_permissions
проверяются. Identity/account epoch и создателя grant проверяет непосредственно
Auth. Tests не заменяет Auth новым синтетическим admin.
В job/grant/log не сохраняется пользовательский bearer, пароль или исходные строки.

Owner каждый раз получает live stage-bound Actor + immutable typed binding,
проверяет exact route/method/noquery/rawbytes, command/job/audience/scope/proofs.
Auth не подменяется shared service key и не создаёт синтетического admin.
Tests требует upload.edit плюс tests.edit либо test-results.edit, live org/div
и участие инициатора. Для первого apply затем выполняются прежние native guards.
JSON вставляет неизменный новый тест по deterministic actor/key ID; external
использует expected target revision CAS, full native preview/mapping comparison.
Распределённая атомарность Auth/Core/Tests не обещается.

После live авторизации сначала проверяется прежняя matching receipt. Ревизия,
сопоставление и editable preview не проверяются заново как новый импорт, если
команда уже подтверждена. Receipt-only требует свежий доступ инициатора к исходной
org/div и оба edit права, но не действующий editable source или активное направление.
Для JSON lookup использует прежние deterministic ID/OperationHash; external —
прежний actor-bound normalized input hash/CommandReceipt даже после удаления target.
Missing receipt означает неизвестный исход, не доказательство отсутствия записи.
Expiry/revoke/Auth failure после возможного commit тоже остаются UNKNOWN.
Ни новый UUID, ни повторный apply по receipt-only не допускаются.

JSON native ID вычисляется по организации, **account_id** и native UUID;
существующий OperationHash зависит от метода/пути/предметного payload и сохранён
без изменения. Его нельзя ошибочно называть хешем actor ID. External native
request hash включает account_id инициатора и полный normalized Commit DTO.
При grant external Commit DTO upload_id/upload_session_id/upload_preview_revision
дополнительно совпадают с immutable binding. Profile ID не подставляется вместо
account_id ни при записи, ни при поиске receipt.

## Readonly prepare до выдачи grant

POST `/internal/tests/imports/json/prepare` и
`/internal/tests/external-results/prepare`: текущий Bearer инициатора,
`X-CPM-Import-Prepare-Key` = incoming Tests delivery credential,
исходный UUID `Idempotency-Key`, неизменный Commit DTO. Нет query, grant,
legacy key или owner mutation. Tests проверяет текущие права, preview, external
target revision/mapping и возвращает status/data с native_command_key,
owner_request_sha256 и native schema1 owner_proof. Uploads копирует хеш владельца,
а не повторяет его алгоритм. Для Auth proof нормализуется явным удалением
schema_version; четыре поля scheme/input/validation/mapping сохраняются.

Prepare не является grant stage. Подтверждение пользователя → сохранение
native key/точных bytes → live prepare → сохранение quote/plan → Auth issue →
durable grant binding → enqueue остаётся ответственностью Uploads.

## План проверки

Точность/стабильность proof и совместимость прежнего preview_hash; mixed credentials,
exactbytes/path/query/ForceQuery/stage/job/audience/target/proof mismatch — без записей;
live revoke/block/foreign/Auth unavailable до apply; matching replay после target
revision/mapping changes; потерянный ответ → отдельное receipt-only без второй записи;
missing/expired/revoked после неизвестного исхода; реальные confirmed UI → Uploads →
Rabbit → worker → owner/result в собственных fixtures. Runtime/новые ключи/Auth/schema
только после source freeze, целевых проверок и нового согласованного окна.

Meaningful native Go cases: import_grants_test.go, import_prepare_test.go,
import_wire_test.go, import_proofs_test.go и import_receipts_test.go. Они проводят
запросы через настоящие native handlers, fake Auth/Core HTTP и Mongo driver
responses; контролируют commands и отсутствие insert/update/delete в отказах,
prepare и receipt. Это unit/HTTP boundary proof, не real-runtime E2E.

`scripts/operation_grants_http_test.py` по умолчанию выполняет только source
preflight: SHA frozen contract, NOTRUN, без чтения credentials/HTTP/DB. `--execute`
предназначен только для согласованного dev окна. Требуются `--login`,
`--foreign-login` (другой разрешённый аккаунт), `--organization`, `--division`,
`--direction`, `--student-account`, `--student-profile`, `--student-name`.
Фиксированы loopback Auth/Tests и cpm_dev Mongo; ключи читаются из приватного
`dev/local/import-operation-credentials.env` mode 0600, токены живут в памяти.
Проверяются JSON, OTP и external: live prepare, выдача grant, preview, logout
инициатора, apply, redelivery, receipt, changed wire/mixed credentials,
revoke, foreign recovery rejection, explicit receipt recovery и запрет нового
apply. Mongo доступ — только чтение своих scoped fixtures; записи идут через API.
Отчёт содержит отдельные profile/account IDs и отмечает Uploads/Rabbit/UI NOTRUN.
Синтетические fixtures и аудит сохраняются, login sessions закрываются.
