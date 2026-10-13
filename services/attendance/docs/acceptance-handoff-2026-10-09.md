# Attendance: handoff после общего аудита, 09.10.2026

## Границы

Attendance владеет calendars/lessons/rooms, отдельными division journals/types/days/marks, QR, reports, zap/attachment/decision/date operations, собственным audit/receipts. Core предоставляет текущие read-only references и permanent history marker; Auth live introspection выполняется на каждом public запросе. BFF маршрутизирует/адаптирует; Uploads может регистрировать существующий файл, но не хранит второй zap/attachment lifecycle. В текущем attendance нет import/background jobs/cache/Rabbit messages; их scope не объявляется проверенным за счёт чужого сервиса. SQL state/audit/receipt изолированы по organization_id, receipt дополнительно по actor_account_id и exact key/body/path. IDs предметных объектов уникальны внутри organization и проверяются по division.

Изменения Core/BFF/UI теперь выполняет только чат BFF. Его окно разрешило пересобрать только attendance8013; Core/BFF/Rabbit/БД/.env и общие Compose не перезапускались.

## Исправления после аудита

- Архивированное подразделение исключается из списка календарей; оно не ломает действующее личное/выбранное расписание. Явно выбранный архивный календарь остаётся404. Отсутствующий internal Core endpoint/plain404 даёт503, а не успешный пустой список. Недоступность Core/Auth остаётся fail-closed.
- Proctor receipt replay для mark/scan/unmark повторно проверяет актуальную группу/назначение и исходную историческую группу через immutable audit snapshot. Отзыв назначения запрещает возврат прежней записи; неизменное назначение сохраняет исходный response/revision. Уже успешно выполненный scan можно повторить после expiry исходного QR по точной команде, но текущие права проверяются снова.
- Mixed timezone events упорядочены по UTC starts_at/ends_at/ID. Период по-прежнему фильтрует локальную дату каждого source calendar; overlap считается по UTC, clusters не содержат скрытые/невыбранные события. DST ambiguous/nonexistent минуты отвергаются.
- Восстановлены прежние правила отгулов: trim/dedup dates; до60 дат, текст/ответ до5000 Unicode символов; пустой answer разрешён; обработанное решение можно исправлять. Одобрение заменяет ручную/QR отметку настроенным ZapDefault с прежним Entry.ID и immutable audit history. Другой zap-mark возвращает409 attendance_already_marked. Обычный manual mark/scan не перезаписывает zap-mark:409 attendance_linked_to_zap.
- Repeated approved сохраняет прежний Type snapshot/UpdatedAt даже после архивации текущего default. Rejected снимает только живые отметки собственного zap, сохраняет tombstone/day history/Core marker. Reapprove связывает даты с текущим активным ZapDefault. Отметка, созданная вручную после unlink, не удаляется последующим reject другого решения.
- Member projection дополнена nullable class/group_name/school_id/school_name/school_short_name. После согласованного обновления Core эти поля проверены на реальных roster/report ответах: 88 новых проверок. Отсутствующие поля честно null, не искусственные значения. UI/XLSX обновляет владелец BFF.

## Точный контракт для BFF

Существующие38 public операций и envelope сохраняются; Native добавил39-ю операцию — scoped metadata-only `GET /command-receipts/{canonical_uuid}` по [U04 контракту](command-receipts-u04-proposal.md). BFF296 runtime и15 actual reload browser сценариев прошли; подробности ниже. Общие пути: owner `/v1/attendance/*`, browser `/bff/attendance/*`; organization_id из проверенной live сессии, division явный URL/filter, active cabinet его не подменяет. Все обычные JSON ответы `{status:true,data:{revision,value}}`. Writes `{expected_revision,data}` + исходный Idempotency-Key, никаких auto retries с новой revision. Ошибки `{status:false,error}`.

`POST /divisions/{d}/zaps`: `{account_id?,text,dates:[YYYY-MM-DD],images:[{mime,data:base64}]}`; student account автоматически собственный, сотрудник с zaps.edit указывает target student. Запрос до3MiB, до3 PNG/JPEG по512KiB. Возвращает Zap без images.

`POST /divisions/{d}/zaps/{z}/process`: `{status:"approved"|"rejected",answer:""|string}`. Прежнее pending-only ограничение и обязательный непустой answer сняты. UI должен давать исправление решения обработанной заявки и прежние лимиты60/5000. Старый UI/HTTP тест expected409 при manual collision не соответствует восстановленному legacy правилу: теперь approve200, zap_default Entry и сохранённая история;409 остаётся для другого linked zap. При исправлении решения UI предупреждение может объяснить изменение живой отметки, но предметное решение выполняет attendance.

`GET /divisions/{d}/students`: Member `{account_id,profile_id,role,full_name,group_id,group_name,class,school_id,school_name,school_short_name}`. Последние6 nullable, legacy group/class/school filters и XLSX колонки следует восстановить через BFF/UI, не через второй источник журналов. API report сохраняет historical Entry/type snapshot и `unmarked_is_absent:false`.

## Точный контракт existing attachments для Uploads

- Registration payload `{type:"attendance.zap-images",target_id:"<zap.id>",owner_reference:"<image-index-1..3>"}` в own organization/division/actor scope.
- Owner metadata: Bearer `GET /v1/attendance/divisions/{d}/zaps/{z}` → `{status:true,data:{revision,value:{zap,image_count}}}`. Нужен специальный unwrap value в Uploads; unwrap только outer data давал Count0 и ложный404.
- Binary: Bearer `GET /v1/attendance/divisions/{d}/zaps/{z}/images/{i}` →200 `image/png` или `image/jpeg`, body<=512KiB. Cache-Control:no-store, no public URL. Student только собственный zap; staff/full admin по актуальному zaps.view. Невидимый/чужой zap/div/index →404, чужой org →404; dependency →503; недействующий token401.
- BFF для прямой выдачи owner адаптирует binary к `{status:true,data:{mime,data:base64}}`, сохраняя обязательные browser context headers. Uploads registry links привязаны к actor+organization+division; другой актор регистрирует собственную ссылку после owner проверки.
- В каталоге Uploads PNG/JPEG, без WebP. Student reconciliation своего file receipt не должен требовать staff upload.view. Общий центр для staff дополнительно требует upload.view/edit, но прямые операции attendance продолжают требовать только предметные права.
- Живая совместная registry→metadata→download/reconcile приёмка прошла после READY владельца Uploads: 290 HTTP/envelope/content assertions. Созданы только три новые ссылки на существующие собственные изображения (D1, D2, org2001). Проверены registration201, exact replay200 с тем же ID, собственный student receipt200, актуальные metadata и PNG bytes/no-store; foreign actor/org/division404, current cabinet mismatch403, readonly/revoked staff и logout новой тестовой сессии. Отчёт: `dev/local/attendance-uploads-attachment-http-report.json`. Core/Attendance writes, SQL, Rabbit jobs и перезапуски этой проверкой не выполнялись.

## Свежие проверки

`dev/local/attendance-scope-http-report.json`:178 проверок реальных auth/core/attendance/MySQL,09.10.2026. Собственные новые DEV scope divisions/accounts/roles/groups в org1001 и2001; существующие общие роли/настройки/участия не менялись. Проверены populated journals/types/zaps/images двух организаций, несколько divisions, full/filtered admin, active readonly cabinet при edit другого разрешённого division, published overlay без journal/file доступа, foreign group/day/type/account/zap/image/QR, UTC/DST/period/localdate boundaries, live role downgrade и proctor unassign с old JWT/exact receipt, historical Type snapshot, decision corrections/manual replacement/unlink/permanent Core guard. Свежий test не заменяет совместную BFF/UI/Uploads приёмку.

Attendance full Go/race/vet с реальным dev MySQL прошли после scoped replay/zap исправлений; дополнительно focused mixed timezone/internal404/zap legacy проверки. SQL тесты используют собственные уникальные синтетические organization keys и очищают только три таблицы attendance своего теста. FullHTTP/main browser прежнего этапа целиком повторно не запускались.

Команды:

```sh
# Реальный MySQL Go/race/vet без вывода DSN
./dev/.venv/bin/python services/attendance/scripts/check.py
# Новые собственные fixtures; затем проверка текущего owner runtime
python3 services/attendance/scripts/scope_http_test.py --prepare --verify
```

`dev/local/attendance-scope-fixtures.json` содержит только ID/login своих fixture accounts и owner image pointer, без passwords/tokens; пароль берётся из private dev accounts.json. History-protected fixtures не удаляются обходом guard. Проверка физической камеры явно deferred пользователем до выкладки и не блокирует dev. Production migration/release остаются отдельным этапом.

Обычная совместная приёмка существующих вложений, независимо от фоновых импортов:

```sh
./dev/.venv/bin/python services/attendance/scripts/uploads_attachment_http_test.py --run --check-staff-read --check-session-revocation
```

Отдельная проверка новых Core metadata прошла после READY владельца Core, после 19:34 МСК: `dev/local/attendance-core-projection-http-report.json`, 88 проверок. Созданы две собственные scoped schools с одинаковым синтетическим названием в org1001 и org2001; обновлены только два собственных synthetic student profiles. Проверены class=10, division-specific group и school ID/name/short_name в Attendance roster и Core report, собственный student roster, реальные report days, foreign school read/update404 и foreign roster/report404. Перезапуски, SQL и изменения общих fixture accounts отсутствуют.

```sh
./dev/.venv/bin/python services/attendance/scripts/core_projection_http_test.py --verify --allow-school-profile-fixtures
```

## Завершённые изменения UI и финальная проверка сканирования

Прежние ограничения в `cpm_front_new/components/attendance/zaps.tsx` (4000/31/2000, обязательный ответ, решение только pending) заменены на 5000/60/5000, пустой ответ и исправление обработанного решения. `lib/bff/attendance.ts` объявляет новые nullable Member fields; `components/attendance/journal.tsx` содержит фильтры и колонки class/group/school в UI/XLSX. Свежая приёмка проверяет approve200 с сохранением Entry.ID, затем approved→rejected→approved с пустым ответом.

Владелец BFF согласовал узкую делегацию одному агенту Attendance: только эти три UI-файла и собственные целевые тесты. Изменения завершены после чтения AGENTS и текущей документации Next. Final source freeze: 15 целевых Vitest тестов, ESLint пяти файлов и scoped TSC прошли. Добавлены `lib/bff/attendance-journal-metadata.test.ts` и `components/attendance/zaps-decision.test.tsx`; они проверяют фильтры по ID, неизвестные metadata исторического ученика, XLSX только видимых строк, заголовки пустой выгрузки, историческое название типа и решения во всех трёх статусах с пустым ответом. Нормальная работа CAS/receipt/context hooks сохранена. Source fingerprints и статус проверок: `dev/local/attendance-ui-source-handoff.json`.

Общие сборка/runtime/browser и BFF acceptance scripts остаются у владельца BFF. Он сообщил о 488 Vitest тестах в 55 файлах и успешной optimized Next сборке. После его BFF/UI READY прошла отдельная read-only браузерная приёмка журнала: 8 сценариев администратора и ученика, пять настоящих XLSX downloads и пять screenshots. Проверены реальные metadata, совместные/null/empty фильтры, только видимые строки в файле, сброс при D1→D2, независимость выбранного журнала от активного кабинета, student self-only, отсутствие JS ошибок/внешних запросов/domain writes. Новые auth sessions завершены через UI, browser contexts закрыты. Отчёт: `dev/local/attendance-ui-metadata-report.json`.

```sh
/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node services/attendance/scripts/ui_metadata_browser_test.mjs --run
```

Владелец BFF завершил свежую совместную приёмку новых правил отгулов: 15 сценариев через фактический BFF/UI. Отчёт `dev/local/bff-attendance-delta-crud-browser-report.json` содержит completed:true, errors/external/legacy пустые. Собственные division10240/backup10243/accounts526–529 созданы через BFF; общие настройки не менялись. Проверены лимиты5000/60 и invalid5001/61, пустой answer, manual138→approved200 с прежним Entry.ID138, approved→rejected→approved, потерянный ответ после reapprove с точным key/body/revision247 и исходным receipt, unlink двух дат и permanent day/membership guard409, readonly staff, foreign organization404, мобильный viewport390px. Физическая камера не запускалась.

Финальная review выявила scan-mode дефект: один day journal загружался в таблицу со всеми колонками периода, что показывало ложные «Не отмечен» для незагруженных дней. Владелец BFF разрешил узкий fix только `journal.tsx` и собственный `journal-scan-view.test.ts`: display/export ограничены фактически загруженным днём; полный список остаётся picker; fallback и QR согласованы с actual day. Обычный периодный journal возвращает прежний объект. Final source freeze: 21 целевой Vitest (15 прежних + 6 новых), scoped TSC и lint PASS. Регрессионные тесты проверяют реальные XLSX headers/rows, средний/пустой день, stale selection, исключение чужих/незагруженных entries и неизменность picker/обычного period journal. Общий build/runtime выполнил владелец BFF; последующая фактическая scan-приёмка описана ниже.

Для неё в согласованном окне выполнены ровно две native Attendance команды: own D1=10150, day141 2036-01-02 и manual entry142 own student391 активным scoped_excuse. Первый day125 2036-01-01 остаётся без живой отметки этого ученика. Manifest: `dev/local/attendance-scan-fixtures.json`; Core/settings/roles/common accounts/SQL/runtime не менялись, новая Auth session закрыта. Последующая browser проверка была read-only.

До final build закрыт также подтверждённый picker race: пока Jan2 загружается, actualDay ещё Jan1, и enabled picker мог терять возврат к прежнему значению. Один shared Day select теперь disabled при loading; scoped lint/TSC PASS, final hash сохранён в source handoff. Целевые21 тест не повторялись ради JSX атрибута. Own browser harness проверил состояние picker/export/camera, временно задержав исходящий реальный GET: данные и ответ не подменяются. Владелец BFF сообщил503 общих Vitest в56 файлах/TSC0 перед последним атрибутом; отдельный последующий optimized build и targeted Attendance-only READY описаны ниже.

Actual Attendance-only UIREADY получен от владельца BFF после optimized build20:53 МСК:509 Vitest в56 файлах, TSC/lint/build PASS, snapshot включает journal SHA256 c3d69c370034ac112e8289a3bed82dafcb3681d9660ef735686adc37c2b0ccbd. В20:56:00 МСК завершена read-only scan-приёмка: `dev/local/attendance-ui-scan-report.json`, status passed, четыре сценария, три настоящих XLSX и три cabinet screenshots. Проверены Jan1-only/unmarked, Jan2-only/manual recorded Type snapshot, fallback Jan1 после сужения периода, disabled picker/export/camera на задержанном реальном GET без подмены ответа. JS/external/attempted business writes0. Held request released, собственная Auth session logout и browser/context close выполнены; script exit0. Это targeted Attendance owner proof, общий project QA RELEASE не объявляется. Позднейшие Core/BFF rebuild относятся к другим контрактам и не выдаются за runtime этой приёмки.

Отдельный новый QA U04 подтвердил потерю исходного UUID после reload и очистку pending при retry401/403. Прежний lost-reply proof работал в том же mounted dialog. Принят владельцем BFF и координатором [ограниченный receipt-read контракт и полный server scope plan](command-receipts-u04-proposal.md): никакого хранения QR/body/credentials в браузере, никакого доказательства rollback по отсутствующей строке. Native source заморожен:9 групп целевых Go/SQL тестов, full race/vet прошли. Dev runtime обновлён21:34:04МСК, image `b8f807f40b7338cae251c9a519be9825fbff0e40134a6396193b080871436b50`, OpenAPI39/health200. Targeted HTTP завершён21:34:23МСК:5 фактических сценариев/925 assertions,11 committed UUID-команд в own fixtures, Core writes0, logout/cleanup без ошибок; report `dev/local/attendance-command-receipts-http-report.json`. Проверены metadata-only DTO, deleted day/archived type, exact public replay без private metadata, actor/org fences, student/current cabinet и bulk noop/delete original scope; GET/replay не меняли revision/public audit. Legacy, corrupt audit/scope, late commit, QR expiry/rotation и current/historical proctor подтверждены Go/SQL, не выдаются за новые actual HTTP. BFF296 обновлён21:44:15МСК image `853b0eb50398b6ac1ed3562feb3f99662f9a4f2c1f926da598f9ea53c2bd2473`, UI535/57/TSC/lint/optimized build готов21:37:04МСК. Final review дополнительно исправил stripping RawQuery/ForceQuery;3 zero-owner-call регрессии и полный BFF race/vet прошли.

U04 actual browser завершён отдельно от scan: `dev/local/bff-attendance-recovery-report.json`,15 сценариев PASS на own divisions10255/10256.27 normal attempts:17Core,3Attendance fixtures,7UI attempts (4 accepted); никакого повторного POST после reload/GET recovery. Проверены потерянный настоящий post-commit response, original UUID/CAS+1/refresh, live grant403 и logout/relogin401 с сохранением UUID, две вкладки/cabinet/actor-org, storage failure, mismatch реального receipt, explicit new-intent latch и known first403 отдельно от unknown retry. Initial harness-only getByLabel остановил первый прогон после9 успешных сценариев; выполнен только оставшийся tail на тех же fixtures без изменения product U04 или replay неизвестной команды.

Ошибки/browser exceptions/remote/legacy/cleanup.errors0. Роли восстановлены, обе Auth-сессии logout200, browser/contexts/heldGET закрыты. Pending fixture/UI отсутствуют; единственный intentional missing404 metadata UUID retained UNKNOWN в private mode0600 manifest, исходного POST для него не было, отсутствие commit не заявляется. Нового actual scan/QR-loss сценария не было: QR/body exclusion — source/unit, expiry/rotation/oldbulk — Go/SQL. Собственный Attendance→BFF→UI этап завершён; общий project QA RELEASE и production cutover остаются отдельными этапами координатора.

## Прежние отчёты

До этого аудита:53 attendance HTTP,53 Core guard HTTP с8 гонками,185 BFF HTTP и33 browser сценария,273 frontend теста. Они относятся к предыдущей реализации/дате запуска и не являются свежей повторной общей приёмкой новых legacy rules. BFF владелец обновляет свои проверки и UI после handoff. Сервис BFF позже имеет собственные более новые общие отчёты.
