# CPM: карта файлов и владельцев загрузок

Проверено по исходникам dev 9 октября 2026 года. Core legacy и Tests JSON/OnlineTestPad/external-results уже подключены к созданию, чтению и редактированию preview в Uploads. Core/Cards/Exams хранят собственные native sessions; для Tests Uploads хранит `validated_input` и снимок preview, полученный только через защищённую предметную валидацию Tests.

Новая реализация фонового применения есть в исходниках и использует согласованный Auth-контракт revision 2 после прямого разрешения пользователя. Она включается отдельно флагом `UPLOADS_OPERATIONS_ENABLED=1`: явное подтверждение, сохранение неизменной команды, разрешение Auth, Rabbit, применение предметным владельцем и проверка его квитанции. По умолчанию режим выключен; прежний runtime и исторические строки таблицы ниже ещё описывают ожидание авторизации. Это ограничение развёртывания, а не ожидающее согласие пользователя. Полный контракт и хронология — [background-public-api.md](background-public-api.md); результаты исходников и живых проверок ведутся отдельно в [verification.md](verification.md). Homework/Attendance остаются владельцами своих файлов; cards.transform исключён из первых восьми фоновых типов.

## Полный перечень потоков

| Тип / экран | Файл и существующий процесс | Предметный владелец | Область target | Граница общего центра |
| --- | --- | --- | --- | --- |
| users.legacy / «Пользовательские данные» | XLSX: parse → editable preview учеников/школ/групп/прокторов. Legacy /api/user-import/* имеет отдельный commit/job/report. | Core — native legacy preview/read/edit/commit, сопоставления, ученики, группы, школы. Auth — аккаунты и права. | Ученики, группы, участия — division; школы — справочник organization. | Uploads create/read/edit подключены к одной Core legacy session, max100rows. Сырые колонки разбирает Uploads, предметные правила — Core. Core report без паролей использует credentials_policy=reset_required. Async apply ожидает авторизации. |
| users.credentials | Core JSON/CSV/XLSX с явными full_name/login/password; preview → commit → история. Это иной формат. | Core + Auth, /v1/core/divisions/{division}/imports. | Проверенные organization/division; роль и предметные права проверяет Core. | Не выдавать за замену legacy XLSX. Файлы с паролями не сохраняются/не скачиваются через общий центр; preview/report не раскрывают password/hash. Fixtures содержат runtime-маркер. |
| cards.xlsx / «Карточки» | XLSX в manual-тему: направление и существующая/новая тема → parse → preview/edit/exclude → commit → отчёт. | Cards: native sessions/revision/commit/receipts/targets/contract/template. | Карточки, manual-темы, прогресс — organization. Направления — Core catalog той же организации. | Одна native Cards session. Uploads файл/job/журнал pinned к исходному division; копия темы на каждый кабинет не создаётся. |
| cards.transform / «Карточки → драфт» | Выбор существующих карточек; JSON create preview → commit. XLSX повторно не загружается. | Cards → Tests. Cards подготавливает содержание; Tests владеет созданным черновиком. | Карточки — organization; целевой draft — проверенное division. | Не дублировать создание draft/правила вопросов. Нужны train.edit и tests.edit исходного actor. |
| tests.json / «Тесты» | Один внутренний .json; legacy /api/test-import/preview → /commit; настройки редактируются. | Tests: содержание, настройки, drafts, версии и создание теста; protected imports/json/validate и /commit. | Direction — organization; тест имеет owner division и разрешённые подключения внутри organization. | Uploads create/read/edit подключены в режиме validated_input: исходный файл сохраняется, DTO канонизируется, Tests валидирует вопросы/ответы/настройки. Каждое редактирование создаёт новую revision снимка, чтение проверяет актуальность у Tests. Subject writes отсутствуют; async apply ожидает авторизации. |
| tests.json / OnlineTestPad test.js | UI принимает .js наряду с .json. JSON после var test =; AnswerType10/210/410/420 и warnings преобразует Tests. | Tests, тот же content import. | Как для внутреннего теста. | Подключён тот же validated_input create/read/edit. Uploads проверяет синтаксис объекта без выполнения JS и передаёт exact sourceText + legacy metadata. Conversion/correct answers/settings — Tests. Async apply ожидает авторизации. |
| tests.external-results / «Результаты тестов» | Внешний test target + XLSX → header search первых10строк → preview/matching; legacy имеет commit/job/report. | Tests internal external-results/validate + /commit; Core даёт scoped участия и distinct account/profile IDs. | Organization + division + external test. | Uploads create/read/edit подключены в режиме validated_input: извлечение raw rows, pinned target/revision и protected validation Tests. Сопоставления и atomic batch apply принадлежат Tests; текущий Uploads adapter вызывает только validate. Async apply ожидает авторизации. |
| exams.questions / банк вопросов | XLSX: часть/вопрос/эталонный ответ → native preview/edit/exclude → commit. | Exams, /v1/exams/{exam}/imports/questions. | Объект экзамена и его банк; допустимое division/access определяет Exams. | Одна native owner session; Uploads собирает все страницы preview. Вопросы/части/commit — Exams. |
| exams.assignments / назначения | XLSX: ученик +1–6экзаменаторов + optional replacement limit → native preview/edit → commit. | Exams, /imports/assignments. | Назначения, комиссия, привилегия — target division экзамена. | Login/account/profile resolution и версия привилегии — owner. Чужой ID/кабинет не разрешается совпадением логина. |
| exams.outside / «Результаты экзаменов» | XLSX: ученик/баллы/оценка/имя экзаменатора → preview/edit → commit; общий admin-tab и экран Exams. | Exams, /imports/outside. | Target division экзамена вне LMS. | Не overwrites existing result. External Tests и external Exams — разные типы/команды. |
| homework.pdf / рабочее место ученика | Homework presigned S3 → браузер binary PDF прямо S3 → complete → Rabbit → File-work → owner draft → submit/review/grade. | Homework — lifecycle/metadata; File-work-service — проверка/сжатие PDF; S3 — bytes. | Organization + division + собственная работа/submission по правилам Homework. | НЕ переносится. Общий каталог даёт owner link/разрешённые metadata. Второй worker/state machine/обязательный byte proxy отсутствуют. |
| attendance.zap-images / приложения отгула | Zap create с JSON images mime/data; защищённая binary выдача отдельных attachments. | Attendance, /v1/attendance/divisions/{division}/zaps/.... | Organization + division + zap, ученик только свой. | Только ссылка/чтение existing attachment через owner. Zap lifecycle/metadata — Attendance, не старый Core zaps. |
| Шаблоны и отчёты | Legacy frontend создаёт users/cards XLSX templates, test JSON sample и XLSX reports. Общий журнал users/cards/cards_to_draft/external_test_results. | Owner определяет формат; Uploads хранит scoped общий job/report. | Template требует current upload+subject rights; Core legacy требует edit. Report сохраняет исходный job scope. | Catalog.template: credential CSV header, Core legacy XLSX, Cards XLSX, Tests JSON/OnlineTestPad (direction_id/format), external-results XLSX (target_id), три Exams XLSX (target_id). Native template deployments и фактические проверки — verification.md. Transform/Homework/Attendance template:false и 400 template_not_applicable. Общий report требует apply; пока 409 report_not_ready. |

## Источники и маршруты для проверки

| Факт | Точный путь и строка |
| --- | --- |
| Пять old admin upload tabs | cpm_front_new/lib/admin/admin-upload-types.ts:12; Exams tab cpm_front_new/components/exams-v2/upload.tsx:109 |
| Legacy parse/get/edit/commit/jobs/report и transform/tests calls | cpm_front_new/lib/admin/admin-upload-api.ts:17; blueprints cpm-back-main/cpm_back/blueprints/user_import_bp.py:34, card_import_bp.py:27, card_transform_bp.py:26, test_import_bp.py:12, external_test_results_import_bp.py:31, exam_imports_bp.py:31 |
| Dev переключается на BFF workspace; legacy UI остаётся старому режиму | cpm_front_new/components/admin/upload/admin-upload-section.tsx:53 |
| Legacy journal четыре import_type и rolling_back | cpm_front_new/components/admin/upload/admin-upload-jobs-tab.tsx:12 |
| Legacy user headers + active sheet | cpm-back-main/cpm_back/services/user_import/parse_excel.py:11, :59 |
| ФИО фамилия+имя lowercase, отчество игнорируется | cpm-back-main/cpm_back/services/user_import/person_name.py:7 |
| Legacy user first existing match; группы от проктора; class integer | cpm-back-main/cpm_back/services/user_import/build_preview.py:19, :36, :114 |
| Legacy users несколько commits + компенсация, не одна транзакция | cpm-back-main/cpm_back/services/user_import/import_runner.py:160, :197, :316 |
| Legacy dispatch daemon thread | cpm-back-main/cpm_back/services/user_import/import_jobs.py:347, :388 |
| Native credential strict columns, 1–100rows | services/core/internal/core/imports.go:27; services/core/internal/core/routes.go:88 |
| Core native legacy raw DTO/matching/max100 и маршруты | services/core/internal/core/legacy_import_model.go:7; services/core/internal/core/legacy_import_preview.go:66; services/core/internal/core/legacy_import_http.go:110; services/core/internal/core/routes.go:94 |
| Uploads users.legacy native create/read/edit и предел100 | services/uploads/internal/uploads/model.go:52; services/uploads/internal/uploads/adapters.go:67, :84; services/uploads/internal/uploads/sessions.go:254 |
| Core legacy report без паролей, reset_required | services/core/internal/core/legacy_import_apply.go:112 |
| Cards Russian header, nonempty rows, multiline | cpm-back-main/cpm_back/services/card_import/parse_excel.py:15, :38, :85; services/cards/internal/cards/sessions.go:191 |
| Cards duplicates are warnings; included warning row creates | cpm-back-main/cpm_back/services/card_import/build_preview.py:91; cpm-back-main/cpm_back/services/card_import/import_runner.py:228; services/cards/internal/cards/sessions.go:84 |
| Cards owner session/transform/receipt/targets/template | services/cards/internal/cards/http.go:93; services/cards/internal/cards/import_contract.go:10 |
| Cards → Tests draft | cpm-back-main/cpm_back/services/card_transform/to_draft_canvas.py:27; services/cards/internal/cards/sessions.go:419 |
| UI .js accept и sourceText wrapper | cpm_front_new/lib/admin/admin-upload-types.ts:87; cpm_front_new/components/admin/upload/admin-test-upload-panel.tsx:181 |
| OnlineTestPad JSON extraction/AnswerType conversion | cpm-back-main/cpm_back/services/test_import/json_import.py:101, :163, :336 |
| Native JSON question/settings schema | cpm-back-main/cpm_back/services/test_import/json_import.py:435, :504 |
| External header aliases/search10, percent comma/% parsing | cpm-back-main/cpm_back/services/external_test_results_import/parse_excel.py:11, :64, :92 |
| External duplicate/ambiguity/unknown/existing blocks all | cpm-back-main/cpm_back/services/external_test_results_import/preview.py:44, :148; cpm-back-main/cpm_back/services/external_test_results_import/import_runner.py:153 |
| Tests raw DTO/preview/commit, different account/profile IDs | services/tests/internal/tests/external_import.go:16, :32, :39; services/tests/internal/tests/external_http.go:710 |
| Tests JSON/OnlineTestPad protected validator | services/tests/internal/tests/json_import.go:721, :808; services/tests/internal/tests/external_http.go:712 |
| Uploads validated_input source/preview snapshots, create/read/edit | services/uploads/internal/uploads/model.go:55; services/uploads/internal/uploads/staged_previews.go:17, :187, :247, :295, :338 |
| Public catalog commit:false; все jobs ждут авторизации | services/uploads/internal/uploads/http.go:100; services/uploads/internal/uploads/jobs.go:169, :188 |
| Protected templates и applicability; report_not_ready | services/uploads/internal/uploads/templates.go; services/core/internal/core/legacy_import_template.go; services/tests/internal/tests/import_templates.go; services/uploads/internal/uploads/jobs.go:31 |
| Все три native Exams kinds/API | services/exams/internal/exams/imports.go:32; services/exams/internal/exams/http.go:63 |
| Exams headers/10MiB/64MiB/5000rows/no formulas | services/exams/internal/exams/imports.go:17, :35, :128, :205, :418 |
| Exams assignment/commission/privilege and outside rules | services/exams/internal/exams/imports.go:688 |
| Homework full lifecycle/direct S3/scoped Rabbit command | services/homework/internal/homework/http.go:146; services/homework/internal/homework/storage.go:35; services/homework/internal/homework/workflow.go:306 |
| File-work existing queue topology/worker | services/file-work-service/internal/filework/model.go:16; services/file-work-service/internal/filework/worker.go:104 |
| Attendance actual PNG/JPEG byte/type bounds | services/attendance/internal/attendance/http.go:45; services/attendance/internal/attendance/zaps.go:24 |
| Legacy zap JPEG/HEIC/PDF5MiB differs from current PNG/JPEG512KiB | cpm_front_new/lib/zaps/zap-date-utils.ts:4; components/attendance/zaps.tsx:131 |
| Legacy user export includes plaintext password | cpm_front_new/lib/admin/admin-upload-export.ts:102 |

## Точные форматы и границы разбора

| Тип | Синтаксис источника | Поведение и предел |
| --- | --- | --- |
| Legacy users | XLSX active sheet, first row mandatory ФИО/Класс; optional Школа/Проктор/Telegram; aliases preserved. | Blank rows skipped, physical row preserved; DTO rows[row/full_name/class_raw/school_name/proctor_name/tg_name]. Native Core и каталог Uploads: max100rows. Owner validates class/name/matching/rights. |
| Credentials | CSV UTF-8 или XLSX literal full_name/login/password, optional role/class/tg_name/school_id/group_id/role_id. | Core1–100. Password marker must be replaced in memory; do not import/persist the marker as a password. |
| Cards | XLSX active sheet first row Вопрос/Ответ; direction_id + existing theme_id или create_new_theme/new_theme_name. | Max1000; owner10MiB, expanded32MiB, Uploads common source8MiB. Question4000/answer16000characters. Multiline blank answer lines/separators preserved. Empty workbook row rejects. |
| External results | Headers in physical rows1–10; mandatory ФИО/percent; optional correct count/date/time/login/account_id/legacy_student_id. Legacy export also had source number/platform user/IP. | Max5000 raw rows. Uploads only necessary owner fields, no IP/platform in Rabbit. Null percentage means blank/unparseable; zero remains0. String87,5%→87.5; numeric0.875 remains0.875 regardless of display format. Stable UUIDv8 from sourcebytes+physicalrow. Owner DTO/participant limits separately apply. |
| External explicit IDs | Только account_id и legacy_student_id, casefold; positive decimal int64, blank omitted. Generic id — source_number. | Text сохраняет int64 без округления; numeric≤2^53−1, большие IDs требуют text. Invalid/duplicate headers —422 row/field. Account и profile различны; current scoped lookup/consistency/mismatch preview принадлежат Tests. |
| External typed cells | String/number normal scalars; date allowed completed_at. | Boolean/error/date score or fractional correct_count gives honest source syntax error row/field. Formulas blocked before owner. Tests validates range0..100 and duplicate/existing/matching rules. |
| Test JSON | title/direction/startDate/endDate/timeLimitMinutes/published/visible/questions; questionId/type/text/points/answers or correctAnswers. | Native Tests max200questions; startDate/endDate обязательны даже для hidden. Синтаксический parser возвращает исходные JSON bytes; uploaded source сохраняется. Protected input/preview канонизируются для snapshot/hash, поэтому DTO не обещает побайтовое совпадение с файлом. Tests owns subject shape, correctness and settings. Общий owner DTO ≤1MiB. |
| OnlineTestPad JS | BOM accepted, JSON after var test =; braces/escaped quotes in strings considered. | No eval/execute. Wrapper importFormat=online_test_pad, exact sourceText, metadata. Optional scalar fields title/direction/startDate/endDate/timeLimitMinutes/published/visible. Owner converts types10/210/410/420, score correctness and warnings. |
| Exams questions | Часть/Вопрос/Эталонный ответ or part_code/question_text/answer_text. | First row, strict unknown/duplicate headers, single-sheet, max5000rows. |
| Exams assignments | student_id or student_login; examinator_1_id/login...6; optional replacement_limit. | Exams requires1–6different examiner participations and current privilege revision. Account/profile mapping is owner responsibility. |
| Exams outside | student_id or student_login, numeric points, integer grade, text examinator. | Exams validates grade0..5/points and rejects existing result replacement. |
| Homework PDF | Genuine binary PDF directly S3. | Source/output/pages limits from Homework settings; Homework/File-work enforce security/generation/compression. Fixture is owner-chain sample. |
| Attendance image | Zap command JSON mime=image/png or image/jpeg, data=base64. | ≤3attachments, each decoded≤512KiB and actual type/dimensions checked by Attendance. |

Common bounded XLSX gate forbids formulas/VBA/embedded content/external relationships/XML entities. Compressed/expanded bytes, entry/column/row limits apply. External URLs are never opened. Legacy read-only cached formula values do not authorize formulas in the new service.

## Scope, commit и очередь

1. File, administrative session, preview revision, job, report, event, receipt pin verified organization_id + original division_id + actor_account_id + type/owner. Эти scope IDs не берутся из spreadsheet/body; явные предметные account/profile IDs проверяет владелец в этой области.
2. Cabinet switch does not move open session/job. Cards/themes/progress and schools/directions are organizational targets, while their upload journal still records division. Groups/participations/scores are division-scoped.
3. Administrative import requires upload and subject permission. Cards canonical subject is train. upload.edit does not grant admin role or all pupils. Student personal PDF/zap operations remain owner-only.
4. Current create/read/edit используют текущего actor и protected owner validation. Для будущего async apply owner должен повторно проверить membership/role/permissions/target/revision через согласованный Auth-контракт; сейчас apply отсутствует. Service key does not replace original actor. Account and profile IDs are distinct; same names in another scope never authorize updates there.
5. Execution queue is RabbitMQ; SQL is durable journal/state/outbox/inbox/receipt/recovery, not primary import polling. Messages contain scoped IDs and protected-input references, never bearer/password/full source rows.
6. Core/Cards/Exams используют одну native owner session. Tests использует administrative validated_input snapshot в Uploads и stateless validation владельца; subject session не создаётся. Edits create new revision. Подтверждение технического enqueue фиксирует revision/input hashes; native owner commit пока не вызывается.
7. Технический enqueue может вернуть 202, но public catalog всегда commit:false. Rabbit executor durably фиксирует awaiting_authorization/background_authorization_pending для всех импортов до ACK; domain apply и progress равны нулю, report возвращает409. Retry не обходит эту зависимость и не использует сохранённый bearer. Owner transaction/aggregate commit после подключения не будет означать distributed atomicity Core/Auth/Tests; legacy compensation также не atomic.
8. Source/report/template/download/expiry/retry/guessed ID access checks current scope and owner permission. Password/hash never in general preview/journal/report/source. Uploads cleanup never deletes domain S3 or Attendance objects.

## Синтетические файлы и повтор

services/uploads/scripts/generate_fixtures.py reads no env/secrets, contacts no DB/HTTP/Rabbit/S3, seeds no accounts, runs no migration. generate_workbooks.mjs authors XLSX through bundled artifact runtime. PDF/PNG contain only synthetic content.

    python3 services/uploads/scripts/generate_fixtures.py
    cd services/uploads
    ~/.local/bin/go test ./internal/uploads -run OwnerInput -count=1

Options: --runtime (root returned by load_workspace_dependencies), --output, --previews. Previews stay temporary. fixtures/manifest.json records size/SHA256; expectations.json records source/owner assertions; scenarios.json defines symbolic org/div fixtures. Harness binds runtime IDs/credentials in memory.

| Directory | Coverage |
| --- | --- |
| fixtures/users | Legacy success/duplicate names/owner ambiguity/invalid names+classes/missing header; credential CSV/XLSX markers/duplicate login/101rows. |
| fixtures/cards | Success/duplicates warning/multiline blank line+separators/empty row/missing answer+header/formula; transform JSON runtime IDs. |
| fixtures/tests | Native single/multiple/text success; invalid question IDs/answers/JSON; OnlineTestPad JS + request wrapper + unsupported AnswerType. |
| fixtures/external-results | Fraction/string score success; header10/11; duplicate patronymic-insensitive names; ambiguity/unknown; Boolean/non-number/out-of-range; existing result; zero+0.875; 5001rows. |
| fixtures/exams | Questions success/duplicate/formula/205rows/5001rows; assignments success/duplicate examiner/foreign division; outside success/invalid/foreign org. |
| fixtures/owner-references | Genuine one-page homework PDF, PNG + Attendance JSON; no lifecycle transfer. |
| fixtures/security | Disguised not-XLSX/not-PDF;65MiB expanded entry in small ZIP. |

HTTP harness must bind two organizations and A1/A2/B1 divisions, same names in different scopes, two ambiguous students inside A1, existing external result, filtered staff and permissions. Cases: foreign guessed IDs/source/report, revoke access after preview, cabinet switch, upload edit without subject edit, stale revision, loss-of-response receipt/retry, worker restart/Rabbit redelivery. Fixture existence does not prove those HTTP checks passed.

## Готовые preview и оставшиеся зависимости

- Core legacy native preview/read/edit/commit доступны; Uploads create/read/edit подключены с пределом100rows. Core применяет policy reset_required и не экспортирует plaintext credentials. Общий async apply ещё не готов.
- Tests native JSON/OnlineTestPad и external-results validated_input create/read/edit подключены. Все subject checks, conversion, correctness/settings/warnings и matching — protected validators Tests; Uploads сохраняет bounded input/preview snapshots с revision/hash и проверяет их актуальность при чтении/подтверждении.
- Общая зависимость для ВСЕХ background imports — отдельно согласованный Auth-контракт. Публичный каталог commit:false; принятый202 проверяет transport, а не завершение импорта. Domain apply=0 и report_not_ready до её снятия.
- Tests owner DTO ограничен1MiB; external raw rows ограничены5000. Эти пределы не отменяют subject/participant limits Tests. Reports и recovery результата apply пока ожидают его подключения.
- Exams all preview pages require consistent id/revision/state/total. 205row specimen catches100/200row truncation. Browser/BFF acceptance is separate from direct owner HTTP.
- Legacy zap HEIC/PDF5MiB versus current Attendance PNG/JPEG512KiB is an explicit migration difference; Uploads cannot silently accept unsupported data.
- Protected templates users.legacy, Tests JSON/OnlineTestPad и external-results развёрнуты и прошли отдельный download→new parser→native preview прогон:284проверки/130записанныхHTTPзапросов, включая точные account/profile IDs и durable reload. Credential CSV header, Cards XLSX и три Exams XLSX также подключены. Transform/Homework/Attendance имеют template:false и400template_not_applicable. Актуальные runtime/proof — docs/verification.md; browser/BFF приёмка остаётся отдельной.
- BFF/UI changes belong to BFF chat. Homework/Attendance use owner links, no duplicate lifecycle wrappers. Telegram integration excluded; tg_name remains profile field.
