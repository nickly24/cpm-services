# Draft: ограниченная авторизация фонового импорта

9 октября 2026. **Реализация разрешена человеком; точный контракт ещё согласуется.** Прямое сообщение пользователя `01a12221-7d06-73f3-8f27-8c05f352977a` в главном чате `01a10598-5c52-7dd1-ad4b-95ba33bae44c` разрешило новую авторизацию фоновых импортов после явного подтверждения, в исходной организации/подразделении и с актуальными правами. Прежний отказ автоматической проверки сохранён как история; ожидание этого согласия снято. Код Auth, миграции, новые credentials и включение ещё не выполнены. Draft не подтверждает точность wire-протокола: владельцы согласуют его до подключения. Общий QA RELEASE этим разрешением не объявлен.

## Что доступно сейчас

Uploads сохраняет задания и доставляет через Rabbit; предметный apply заблокирован: awaiting_authorization/background_authorization_pending, каталог commit:false. Preview/edit/read/template проверяют живую пользовательскую сессию. Техническое202 не означает применение строк.

Будущий grant закрепляет исходные account/org/div, job и одну предметную команду; ID не bearer/JWT и не даёт произвольный доступ. Смена кабинета не переносит job. Пользовательский bearer не хранится в БД/outbox/inbox/журнале.

## Выдача и immutable input

Предлагаемый POST /internal/auth/operation-grants требует живого bearer инициатора, отдельного удостоверения Uploads и UUID issuance key. Auth выводит account/org/div из сессии, не из выбора browser. Issuer и каждый owner должны удостоверяться раздельно; существующий общий service key не различает их. Новые ключи сейчас не созданы.

Record закрепляет purpose=uploads.import.apply, upload_id/upload_session_id/job_id (32lowerhex), coordinator command key, type/owner, неизменный native owner command key, **upload_preview_revision отдельно от owner_preview_revision и target_revision**, native session ID, типизированную цель, source digest, owner-defined validation proof и exact registered method/path/query/wire-body digest каждого stage.

Auth строит allowlist из зарегистрированного типа и ID. Переданное описание подтверждает совпадение, не создаёт произвольный маршрут. No host/wildcard/selector headers/query scope override. Строгие DTO отклоняют unknown/duplicate fields и неоднозначные ID. Numeric IDs — положительные canonical decimal strings без ведущих нулей; конкретный wire type согласовывается до реализации.

Пример users.legacy: Uploads preview revision3, Core native revision2/session1, target division_users; POST /v1/core/divisions/{originDivision}/legacy-imports/1/commit, exact body {"expected_revision":2}. Native command UUID создаётся один раз, не меняется при redelivery, reauthorization или lost reply. Matching issuance UUID возвращает тот же record/expiry; другое тело409 idempotency_conflict.

Exact отправляемые bytes сохраняются один раз до первого delivery и используются неизменно. Credentials commit — **{} (два байта), не пустое тело**. Encoder.Encode добавляет LF. Нельзя хешировать одно представление, а отправлять другое.

Не смешивать wire_body_sha256 (actual bytes), native request/OperationHash (канонический предметный контракт), source_sha256 (файл), owner validation/snapshot proof и Uploads wrapper digest. Некоторые native receipt hashes включают method/path/query/scope либо типизированную DTO. Пустой body SHA — стандартный SHA256 нулевых bytes; {} другой digest.

Owner proof не может быть произвольным Uploads hash. Не все владельцы сейчас публикуют/проверяют такой proof. До реализации каждый определяет canonical snapshot scheme/version, null/UTF8/array ordering и сравнение под тем же lock, что revision/commit. Старый preview без доказанного source не разрешает новый apply.

Предлагаемый TTL6часов (конфигурационный предел5минут–24часа) не превышает срок source/editable preview. Restart/retry не продлевает. Receipt recovery имеет отдельный ограниченный режим ниже.

## Native типы и права

Все типы общего Uploads lane требуют актуальное upload.edit плюс предметные права. Native Cards вне этого lane сохраняет train gate без глобального добавления upload. Homework PDF и Attendance attachments не используют grant.

| Тип | Native IDs / revisions / target | Точная команда и права |
| --- | --- | --- |
| users.credentials | numeric Core session; owner revision null; Uploads revision всегда≥1 | POST /v1/core/divisions/{d}/imports/{id}/commit, {}; users.edit, school/proctor creation только fullorgadmin, group assignment groups.edit |
| users.legacy | numeric Core session, positive owner revision | POST /v1/core/divisions/{d}/legacy-imports/{id}/commit, {expected_revision:N}; те же права |
| cards.xlsx | 32lowerhex session, positive int64 revision; org-owned session division nullable, grant origin division обязательна | POST /v1/cards/sessions/{id}/commit, {expected_revision:N}; train.edit, active scoped direction/theme |
| tests.json | Stateless: owner_session_id/owner_preview_revision/target_id=null. Uploads syntheticref/revision отдельно; Tests input_hash/preview_hash отдельно | POST /internal/tests/imports/json/commit, существующая strict DTO; tests.edit, scoped direction/div |
| tests.external-results | Stateless import, target24lowerhex test ID; expected_test_revision отдельно от Uploads preview revision | Registered protected Tests external import commit с exact current DTO; test-results.edit, test в исходном div |
| exams.questions/assignments/outside | native UUID session, positive revision, обязательный positive decimal exam ID | POST /v1/exams/{exam}/imports/{kind}/{UUID}/commit, {expected_revision:N}; exams.edit, questions дополнительно права всех connected divisions |

Cards target типизирован: existing_theme с ID либо create_theme с direction/name и явно согласованной collision policy. Нельзя выдумывать ещё не созданный ID. Native Cards validation_sha256 пока отсутствует: canonical snapshot и проверка под session lock — будущая работа.

Exams source digest соответствует source_hash, validation — **полный** validation_hash, не hash первой страницы. Повторная ValidateImport может изменить hash при прежней revision. Для первого apply fingerprint проверяется непосредственно под commit locks; strict native JSON нельзя молча расширять grant/hash/audit полями. Для replay сравнивается исторический input/receipt, а не вновь применённые строки как дубли.

Tests JSON/OTP остаётся stateless, synthetic Uploads ID не превращается в native session. External отдельно закрепляет input/mapping/preview/owner hash и target revision. Универсальный validation_sha256 не заменяет эти контракты.

## Read stages и carrier

Read — registered route template/query schema, **не один вызов за всё время**. Повторные recovery reads допустимы с новой authorize проверкой каждого вызова. Разрешены только bounded session/receipt operations; не универсальный GET.

- Core credentials: exact own session GET и creation receipt GET /imports/receipts/{UUID}; legacy exact session GET и его существующий matching-command receipt protocol.
- Cards: exact session GET и GET /v1/cards/receipts/{pinnedKey}; не соседний receipt key.
- Exams: exact session GET, limit≤200, bounded offset, fixed expected_revision; до25страниц/5000строк с одинаковыми target/session/revision/hash/state/total. Если read только global metadata первой страницы, это отдельный stage, не полный preview.
- Tests: protected validate **POST**, не native session GET. Exact validate body фиксируется отдельно от commit body; strict DTO не допускает добавления audit fields.

Нужен отдельный внутренний carrier header/reference, удостоверенный issuer и owner; name/middleware/порядок обработки ещё согласовываются. Сейчас такого middleware нет. Запрещено смешивать bearer/grant identities и browser-forwarded grant/service-key. Owner сохраняет actual bytes до JSON decode и до нынешнего Identify; повторное чтение stream не меняет digest.

Bounded dependency reads требуют своего контракта. Cards Identify сейчас вызывает Core cards-context с Bearer+service key. Grant Cards **не** даёт общего Core права; нужен narrow verified reference proof исходного account/org/div/active direction. Без него соответствующий тип нельзя включать.

## Authorize и живые права

Будущий POST /internal/auth/operation-grants/{id}/authorize вызывает **непосредственно owner своим отдельным удостоверением**. Uploads credential не authorizes вместо владельца. Проверяются audience/stage/method/path/query/exact bytes, immutable IDs/scope/target/revisions/proofs/native key.

Положительный ответ обязан вернуть проверенный текущий stage-bound permission context, достаточный для native Actor.Can/scope: account/role/org/div, organization_admin, current scoped permissions и необходимые participations/connected division permissions. Одних account/role/org/div недостаточно. Не присваивать permissions из названия role, не превращать authorized:true в синтетического admin. Альтернативный typed decision требует отдельного owner enforcement контракта.

На каждом read/apply/replay/delivery живьём проверяются account, active org/div, membership и permissions. Предлагаемый account-state revision/epoch инвалидирует pending grants при блокировке/удалении аккаунта, security reset/revoke-all либо существенной identity смене. Такой operation epoch в текущем Auth **не реализован**. Новое право не продлевает старый input/expiry.

Logout одной browser session отдельно от explicit operation revoke. Предлагается: single-session logout не отзывает подтверждённое background задание; account block/revoke-all/security reset и explicit operation revoke отзывают. Этот выбор входит в human-visible confirmation. Ни stale bearer, ни восстановление старой session.

Предлагаемые ошибки:403 operation_access_denied,404 operation_not_found unknown/foreign,409 operation_request_mismatch,410 operation_expired|operation_revoked,503 authorization_unavailable. Недоступность Auth запрещает новую предметную запись. Имена/DTO/TTL/carrier/epoch — draft, не действующий API.

## Replay, отзыв и неизвестный исход

Порядок: live authorization/scope → matching native receipt → только для первого apply current revision/hash/mapping/domain guards. Confirmed replay не валидирует применённые строки как новые дубли. Существующая Tests JSON/external/Core/Cards/Exams receipt семантика сохраняется.

Уже авторизованное и начавшее атомарное применение может закончиться одновременно с отзывом. Распределённая атомарность Auth и owner DB не обещается. Commit+lost reply+expiry/revoke означает **outcome_unknown**, не failed/no_changes. Нельзя выдавать новый owner key либо автоматически делать второй apply. Сначала reconciliation прежней команды.

Обычная expired/revoked delivery запрещена. Completed receipt читает инициатор со свежим доступом к исходному org/div и правом данного импорта; fullorgadmin только если native owner допускает такую audit visibility. Creator-only restrictions сохраняются. Чужой current cabinet не переносит результат. При потере прав — outcome_unknown/access_required, а не обещание, что изменений нет.

Новое явное действие пользователя может разрешить **receipt-recovery-only** прежнего key/target/input, без business apply и без требования живого editable preview. Explicit reauthorization первого apply допустима только если source/preview ещё действуют и прежние exact bytes/key/scope сохранены. Нет auto renewal или JWT workaround. Если native не предоставляет отдельный readonly receipt GET, используют только согласованный matching-command recovery protocol; выдуманного endpoint нет.

Retention не меняется: Cards unfinished72h, completed receipts сейчас отдельно; Exams editable72h/committed7d→410/tombstone→purge404; Tests свой deterministic receipt. Новый grant не оживляет expired preview и не заменяет tombstoned key.

Будущий revoke требует live инициатора либо допустимого fullorgadmin и issuer credential, {} / repeat200. Не откатывает applied graph и не удаляет receipt. Recovery/revoke после account deletion и точная audit visibility согласовываются с владельцами до реализации.

## Cards → Tests: отдельный двухстадийный протокол

**cards.transform исключён из начального background allowlist до отдельного согласования Cards/Tests.** Native Bearer flow сохраняется. Single owner/one apply не покрывает цепочку.

1. Cards audience: POST /v1/cards/sessions/{32hex}/commit, свой stable key, revision и owner-computed transform snapshot. Файла нет: source_sha256=null, отдельный именованный snapshot digest.
2. Tests audience: authenticated caller Cards, Tests самостоятельно authorizes своим ключом; parent grant/job/Cards session/revision/snapshot, исходные account/org/div, destination direction; exact POST /internal/tests/cards/drafts с вычисленным Cards DraftDTO и actual bytes; stable child key **cards_{32hexsession}, не UUID**.

Uploads не выбирает downstream body; Cards не получает общий Tests доступ. Child issuance/registration допустимы только для доказанного parent snapshot и отдельно согласованного Cards derivation. Ещё нет target ID: destination org/div/direction плюс deterministic create, не фиктивная Tests session.

Tests сохраняет deterministic draft/OperationHash, отдельного child receipt GET сейчас нет. Если Tests insert завершён, а Cards SQL/ответ потерян, повтор **обоих прежних keys** возвращает тот же draft и завершает Cards receipt. Обе audience проверки повторяются, stages имеют разные hashes/target/outcome. Lost reply не создаёт второй draft. Это будущая схема, сейчас не реализованная.

## Проверки после прямого согласия

Сначала согласовать native DTO/proofs/carriers/live permission contexts/receipt visibility и recovery policies. Затем только разрешённые dev code/migrations/keys. Согласие концепции не доказывает exact protocol.

Проверить actual Auth/Uploads/worker restart, Rabbit duplicates/redelivery, CAS/hash pinning, lost reply до/после commit, expiry/revoke unknown outcome+receipt-only reconciliation, live membership/permission/account epoch revoke, single-session logout, чужие scope/job/session/target/audience keys, exact method/path/query/bytes и native direct UI. Не хранить bearer/password/private source rows в grants/журналах. Существующие Core/Auth/S3/Rabbit credentials этот draft не ротирует.
