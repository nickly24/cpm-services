# React UI: текущая карта перехода на Go/BFF

Срез обновлён **2026-10-09** по исходникам и сохранённым отчётам. Source integration, native owner acceptance и совместная UI/BFF приёмка разделены. Optimized UI 535 и BFF 296 приняты; U04 joint UI 15, U07 API 30 / UI 6, Common Uploads remaining 21 завершены. Предыдущие Cards 15 / Tests 7 / Ratings 15 / Attendance scan 4 / Core external 10 и свежий Exams CAS 7 сохранены с их собственными границами. Core dashboard f3f43d… actual READY; отдельный native+BFF proof 2 сценария / 31 checks PASS, без нового React capabilities wiring. Общий QA RELEASE не объявлен. Исторический call-index в приложениях A–C сохранён с датой **18:02 MSK**: его строки/количество не объявляются текущим scan.

## Текущее состояние

Оригинальный scope Cards/Core/users/schools/dashboard/proctor students/supervisor reports подключён и проверен. Access и Homework settings/audit также завершены. Добавлены организация, подразделения, направления и существующие участия: полный admin меняет свою организацию, scoped staff читает доступные справочники. Telegram исключён. Обычные production-ветки сохранены; в BFF режиме нет silent fallback к ним.

Внешние тесты и ручные результаты прошли **15 joint UI/BFF checks** (`dev/local/bff-external-tests-delta-report.json`). Launcher `dev/bff` теперь задаёт external flag=1 для ui/build/start, явный 0 учитывается; source config требует явного 1 и не включает новый режим production. При flag 0 существующий readiness notice и независимость внутренних каталогов/результатов сохраняются.

Uploads снова работает: native preview/edit и Rabbit delivery приняты его владельцем; common async import **не завершён**. Каталог `commit:false`, executor `awaiting_authorization/background_authorization_pending`, domain writes/progress 0, финальный report 409. Новая Auth delegation ещё не реализована после automatic approval rejection без прямого согласия. Protected templates прошли native Uploads 284 checks / 130 HTTP, Core legacy template 78 DONE; common UI acceptance после Directions/U07 fixes завершён: 21 проверки на UI 535, 6 явно разрешённых previews / 2 CAS PUT, no apply. См. [uploads-contract-gaps.md](uploads-contract-gaps.md).

## Транспорт и границы

| Участок | Текущее поведение / source evidence |
| --- | --- |
| Dev и production | `lib/config.ts`, `dev/bff`: BFF включается явным Dev flag; public env фиксируется в bundle во время build. `API_BASE_URL=/bff/unconnected` не является generic proxy. |
| Старые JSON имена Homework | `lib/api/client.ts` → `lib/bff/homework.ts`: whitelist переводит старые названия функций/paths на native Homework, неизвестный path локально отклоняется. |
| Остальные adapters | BFF early branch в auth/admin/student/exams/training APIs. Legacy transport после branch остаётся production-only. Старый `apiFormRequest` запрещён в BFF до fetch. |
| BFF client | `lib/bff/client.ts`: same-origin cookie, `/bff`, X-CPM-Request/Context; browser Authorization не используется. Account/org/div identity меняется только через Auth context. |
| Files | Homework presigned XHR к локальному MinIO 19000 разрешён владельцем; это не legacy API. PDF lifecycle остаётся Homework/File-work/Rabbit/S3. |
| Owner validation | BFF обновляет auth/me и allowlist; предметные сервисы проверяют scope, права, DTO, revision/receipt. Internal API и служебные ключи в browser не выставлены. |
| Explicit division | Core management target routes разрешены только ownOrg/fulladmin или selected staff scope. Attendance имеет собственные explicit division paths и owner fence; личное расписание объединяет доступные участия текущей организации. |
| Context guards | Dirty editor, передача, активная сдача и unknown command удерживают Web Lock, в том числе между вкладками. Read-only и уже принятый async Ratings job переключение не удерживают. |

## Navigation и права

Entrypoint: `components/sections/section-content.tsx`; menu: `lib/navigation/*`; live permission container: `lib/auth/admin-access.ts`, `components/admin/admin-section-access.tsx`.

| Роль | Разделы / ограничения |
| --- | --- |
| admin | dashboard, users, access, schools, organizations, divisions, directions, upload, assignments/review/archive/monitoring, tests/results, exams, train, ratings, schedule/attendance/scan/zaps. Writes организации требуют реального organization_admin, а не только role label. |
| staff_admin | Admin workspace с live permissions, без access. Users tabs требуют users, groups tab допускает отдельный groups; без users не читаются глобальный roster/profile/unassigned counts. Organization/division/direction catalogs доступны readonly, но не roster/write controls. Начальный раздел — первый разрешённый, включая эти справочники. |
| student | performance (свой рейтинг и external результаты), homework, tests, exams, train, schedule, attendance, zaps. Private чужие результаты/attempts не выдаются. |
| proctor | homework/review/archive, students своей группы, ratings своей группы, schedule/attendance/scan. Нет глобального рейтингового report API. |
| examinator | exams проведения и schedule; не получает admin bank/roles/results другого ученика вне комиссии. |
| supervisor | dashboard, ratings readonly jobs/versions/report, homework/tests/exams/attendance reports, schedule. |

AttendanceWorkspace перехватывает schedule/attendance/scan/zaps **раньше** общей connected allowlist; supervisor attendance является исключением и идёт в Core report. Это правило сохранено. Неподключённый section показывает notice до монтирования legacy component. Новые organizations/divisions/directions отсутствуют в production menu.

## UI → BFF → владелец

| Экран / функции | BFF family и owner | Source evidence |
| --- | --- | --- |
| Login/session/logout/header switch | `/auth/*` → Auth | `lib/auth/api.ts`, `components/layout/cabinet-context.tsx`, `internal/bff/server.go` |
| Homework задания/проверка/архив/мониторинг/settings/audit/report | `/homework/*` → Homework selected division | `lib/bff/homework.ts`, `homework-settings-audit.ts`, `lib/homework-files/api.ts` |
| Users/groups/schools/dashboard/proctor students | `/core/{users,groups,schools,dashboard,students}` → Core | `lib/bff/core-admin.ts`, admin users/schools и proctor-students components |
| Access roles/membership | `/core/roles`, `/core/memberships/{account}` → Core org roles/division participation | `lib/bff/core-access.ts`, admin-access-api, `components/admin/access/*` |
| Organization/directions/divisions | `/core/organization`, `/core/{directions,divisions}` CRUD → Core ownOrg | `lib/bff/core-organization.ts`, `components/admin/organization/*` |
| Existing account participation / role | `/core/organization/users`, `/organization/users/{account}/memberships`; `/core/divisions/{d}/memberships/{account}` → Core | Same adapter/workspace; can_assign/can_revoke distinct, account ID exact |
| Internal Tests/drafts/bank/attempt/calculation/history | `/tests/*` → Tests/Calculate; `/core/organization/test-settings` → Core | `tests-admin.ts`, `tests-student.ts`, admin/student tests APIs |
| External Tests/catalog/manual results/history/delete | `/tests/external-tests/*` → Tests | `external-tests.ts`, shared flag-aware tests adapters, external widgets |
| Classic/outside exams/admin/conduct/student/preparation/import | `/exams/*` → Exams; lookups Core | `lib/exams-v2/api.ts` → `exams-admin/conduct/student.ts`; old exams components not active |
| Cards/manual/test projection/progress/transform | `/cards/*` → Cards org scope; exam training `/exams/*` | `lib/bff/cards.ts`, training APIs, admin/student training components |
| Ratings publication/history/details/job/report | `/ratings/*` → Ratings selected division | `ratings-shared.ts`, admin APIs and shared readonly workspace; actual owner place, zero≠null |
| Supervisor tests/exams/attendance reports | `/core/reports/*` → Core composing live Tests/Exams/Attendance | `core-reports.ts`, supervisor reports wrappers |
| Schedule/attendance/scan/zaps | `/attendance/*` → Attendance | `components/attendance/*`, `internal/bff/attendance.go`; old schedule APIs inactive |
| Common upload center | `/uploads/*` → Uploads coordinator → native owner preview | `lib/bff/uploads.ts`, `components/admin/upload/admin-uploads-bff-*`; commit disabled by live capability |

Подтверждённый runtime по `bff-final-runtime-proof.json`: Core `f3f43d…`, Attendance `b8f807f40b73…` / 39 native операций, BFF `853b0eb5…` / 296 публичных операций / 17 Uploads. Optimized UI 535 (`5da08f64…`) READY с 21:37:04 МСК; frontend suite 535 tests / 57 files, TSC/scoped lint/build PASS. BFF full race 2.935 s / vet PASS. U04 native 5 HTTP сценариев / 925 assertions и joint UI 15 приняты отдельно; U07 canonical result permissions — 30 API / 6 UI. Common Uploads remaining acceptance — 21 проверки: actual checkpoint 19 + readonly tail 2; cumulative 6 новых previews / 2 CAS PUT, source/domain apply 0, pending 0, все Auth/context/browser закрыты.

Core dashboard `f3f43d337a2ef4ba00c08cda3d85674f64c1af74cad752ad145fb09e8db9202d` actual READY после Core-only окна 22:11–22:13 МСК с 8 ACK: started `2026-10-09T19:11:19.349574138Z` (22:11:19.349 МСК), healthy/ready/OpenAPI 200. Source: 5-case permission matrix, targeted Go 1.163 s / full race 2.052 s / vet PASS. [core-dashboard-results-http-report.json](../../dev/local/core-dashboard-results-http-report.json) — 2 read-only native+BFF сценария / 31 checks PASS: один actor 548 в A10254 получает `test_results` и report 200, в B10260 — `tests`, canonical result view false и report 403. Все четыре private totals остаются null; native/BFF DTO совпадают, subject/runtime/SQL writes 0, Auth cleanup failures 0. Текущий React dashboard читает totals, но не использует capabilities; новое UI wiring не заявлено. Три общих QA остаются PREPARE; общий RELEASE не объявлен. Auth background grant/full import apply остаются отдельной нерешённой зависимостью.

Directions 46 target tests и common UI flow, U07 30 API / 6 UI и U04 15 UI уже DONE. Старый Attendance bulk/late commit/QR expiry не заявляются native HTTP/UI proof; recovery UI проверяет собственные реальные receipts, а corruption использует явно обозначенную подмену ответа.

## Текущие остатки кода и приёмки

- Auth background operation authorization ждёт прямого согласия пользователя после automatic rejection. Uploads executor сохраняет awaiting_authorization; domain apply 0 ифинальный report 409. [Предложение](../../outputs/microservices/auth-background-import-approval.md).
- Common Uploads preview/template UI acceptance 21 и native templates 284/130 уже DONE. Credential UUID 44083500-b582-4c23-bd1c-67c2e9e367a7 после recovery 5 PASS остаётся outcome_unknown; source_resends/new keys/business writes 0. Receipt 404 не доказывает отсутствие исполнения, auto-create не разрешён.
- Общие QA 3 — PREPARE, RELEASE нет. Cards 15, Tests 7, Ratings 15, Scan 4/3 XLSX, Core external 67 / fixture 44 / browser 10, U06 native 3/20, U04 native 5/925 / UI 15, U07 30/6, Common Uploads 21 и Exams CAS 7 DONE не числятся незавершёнными.

Core 005/school updated_at 4 limitation остаётся явным ограничением проверки сохранности. Telegram исключён, physical camera отложена пользователем. Нагрузка/CI/CD/production не входят в текущую задачу и не увеличивают список её остатков.

## Доказательства и исторические ветки

Точные свежие отчёты и их границы перечислены в [ui-audit-2026-10-09.md](ui-audit-2026-10-09.md). Исходный Cards/Core cutover — [core-cards-integration.md](core-cards-integration.md); Access/HW/Uploads — [access-homework-uploads-integration.md](access-homework-uploads-integration.md). Новый audit документации не запускал build, browser, owner suite или изменения данных.

Старые `/api/admin-access`, `/api/*-import/*`, users/schools/groups, `/get-all-ratings`, test/exam/training endpoint families после явного BFF branch остаются для обычного production. Старые schedule/attendance/zaps components заменены новым Dev workspace, старые exam components — exams-v2. `fetchMyRating` не является active BFF path. Наличие этих строк в дереве не равно незаконченной migration или fallback.



## Историческое приложение A. Индекс endpoint expressions от 2026-10-09 18:02 MSK

181 call sites в срезе исходников 2026-10-09 18:02 MSK. **Это исторический индекс, не повторный AST scan текущего рабочего дерева.** Номера строк и reachability относятся к указанному времени; новые organization/template/delta изменения в него не добавлены. В строках wrapper с динамическими options метод не восстанавливается из имени функции; он честно помечен как delegated. Это индекс source, а не список 181 недоработок. Все пути ниже являются настоящими выражениями из кода.

| Source path:line | Callee / method | Endpoint expression | Dev reachability |
|---|---|---|---|
| `cpm_front_new/lib/admin/admin-access-api.ts:14` | apiRequest; <code>GET</code> | <code>"/api/admin-access"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-access-api.ts:15` | apiRequest; <code>id ? "PUT" : "POST"</code> | <code>`/api/admin-access/roles${id ? `/${id}` : ""}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-access-api.ts:16` | apiRequest; <code>"DELETE"</code> | <code>`/api/admin-access/roles/${id}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-access-api.ts:17` | apiRequest; <code>id ? "PUT" : "POST"</code> | <code>`/api/admin-access/users${id ? `/${id}` : ""}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-access-api.ts:18` | apiRequest; <code>"DELETE"</code> | <code>`/api/admin-access/users/${id}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-homework-api.ts:24` | apiRequest; <code>GET</code> | <code>`/api/get-homeworks?${qs}`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/admin/admin-homework-api.ts:30` | apiRequest; <code>GET</code> | <code>`/api/homework/${homeworkId}`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/admin/admin-homework-api.ts:36` | apiRequest; <code>GET</code> | <code>`/api/homework/${homeworkId}/overview`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/admin/admin-homework-api.ts:57` | apiRequest; <code>GET</code> | <code>`/api/homework/${homeworkId}/students?${qs}`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/admin/admin-homework-api.ts:65` | apiRequest; <code>"POST"</code> | <code>`/api/create-homework`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/admin/admin-homework-api.ts:85` | apiRequest; <code>"PUT"</code> | <code>`/api/homework/${homeworkId}`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/admin/admin-homework-api.ts:101` | apiRequest; <code>"DELETE"</code> | <code>`/api/homework/${homeworkId}`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/admin/admin-ratings-api.ts:16` | apiRequest; <code>GET</code> | <code>"/get-all-ratings"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-ratings-api.ts:25` | apiRequest; <code>"POST"</code> | <code>"/get-rating-details"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-ratings-api.ts:29` | apiRequest; <code>"POST"</code> | <code>"/calculate-all-ratings"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-ratings-api.ts:38` | apiRequest; <code>GET</code> | <code>`/rating-recalc-jobs?limit=${limit}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-ratings-api.ts:42` | apiRequest; <code>GET</code> | <code>`/rating-recalc-jobs/${jobId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:18` | apiRequest; <code>GET</code> | <code>`/api/get-schools${qs}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:29` | apiRequest; <code>GET</code> | <code>`/api/get-school/${schoolId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:39` | apiRequest; <code>"POST"</code> | <code>"/api/add-school"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:54` | apiRequest; <code>"PUT"</code> | <code>"/api/edit-school"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:67` | apiRequest; <code>"POST"</code> | <code>"/api/student-school-filter"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:81` | apiRequest; <code>GET</code> | <code>"/api/get-unsigned-students-by-school"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:92` | apiRequest; <code>"PUT"</code> | <code>"/api/edit-student"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:102` | apiRequest; <code>"PUT"</code> | <code>"/api/edit-student"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-schools-api.ts:126` | apiRequest; <code>GET</code> | <code>`/api/groups/search?${qs}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-telegram-bot-api.ts:42` | apiRequest; <code>GET</code> | <code>"/api/telegram-bot/settings"</code> | Telegram исключён в BFF |
| `cpm_front_new/lib/admin/admin-telegram-bot-api.ts:52` | apiRequest; <code>"PUT"</code> | <code>"/api/telegram-bot/settings"</code> | Telegram исключён в BFF |
| `cpm_front_new/lib/admin/admin-telegram-bot-api.ts:59` | apiRequest; <code>GET</code> | <code>"/api/telegram-bot/status"</code> | Telegram исключён в BFF |
| `cpm_front_new/lib/admin/admin-telegram-bot-api.ts:63` | apiRequest; <code>"POST"</code> | <code>"/api/telegram-bot/start"</code> | Telegram исключён в BFF |
| `cpm_front_new/lib/admin/admin-telegram-bot-api.ts:69` | apiRequest; <code>"POST"</code> | <code>"/api/telegram-bot/stop"</code> | Telegram исключён в BFF |
| `cpm_front_new/lib/admin/admin-telegram-bot-api.ts:75` | apiRequest; <code>"POST"</code> | <code>"/api/telegram-bot/restart"</code> | Telegram исключён в BFF |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:13` | apiRequest; <code>GET</code> | <code>`/test-drafts?status=${encodeURIComponent(status)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:20` | apiRequest; <code>"POST"</code> | <code>"/test-drafts"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:28` | apiRequest; <code>"POST"</code> | <code>`/test-drafts/from-test/${testId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:35` | apiRequest; <code>GET</code> | <code>`/test-drafts/${draftId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:43` | apiRequest; <code>"PUT"</code> | <code>`/test-drafts/${draftId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:51` | apiRequest; <code>"POST"</code> | <code>`/test-drafts/${draftId}/lock`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:62` | apiRequest; <code>"POST"</code> | <code>`/test-drafts/${draftId}/unlock`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:69` | apiRequest; <code>"POST"</code> | <code>`/test-drafts/${draftId}/publish`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts:76` | apiRequest; <code>"DELETE"</code> | <code>`/test-drafts/${draftId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-results-api.ts:13` | apiRequest; <code>GET</code> | <code>`/test-sessions/student/${studentId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-test-results-api.ts:23` | apiRequest; <code>GET</code> | <code>`/test-session/${encodeURIComponent(sessionId)}/stats`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:21` | apiRequest; <code>GET</code> | <code>"/directions"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:28` | apiRequest; <code>GET</code> | <code>`/tests/${encodeURIComponent(directionName)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:37` | apiRequest; <code>GET</code> | <code>`/test/${testId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:44` | apiRequest; <code>"POST"</code> | <code>"/create-test"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:54` | apiRequest; <code>"POST"</code> | <code>"/external-tests"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:179` | apiRequest; <code>"PUT"</code> | <code>`/test/${testId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:190` | apiRequest; <code>"DELETE"</code> | <code>`/test/${testId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:202` | apiRequest; <code>"PUT"</code> | <code>`/test/${testId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:224` | apiRequest; <code>GET</code> | <code>`/external-tests/${encodeURIComponent(testId)}/delete-preview`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:233` | apiRequest; <code>"DELETE"</code> | <code>`/external-tests/${encodeURIComponent(testId)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-api.ts:255` | apiRequest; <code>GET</code> | <code>`/test/${encodeURIComponent(testId)}/changes${query ? `?${query}` : ""}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts:31` | apiRequest; <code>GET</code> | <code>`/test/${testId}/overview`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts:39` | apiRequest; <code>GET</code> | <code>`/test/${testId}/sessions${buildQuery(params)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts:49` | apiRequest; <code>GET</code> | <code>`/test/${testId}/attempts${buildQuery({ ...params, status: params.status ?? "active" })}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts:58` | apiRequest; <code>GET</code> | <code>`/test-session/${sessionId}/admin`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts:67` | apiRequest; <code>GET</code> | <code>`/test-attempt/${attemptId}/admin`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts:76` | apiRequest; <code>"DELETE"</code> | <code>`/test-session/${sessionId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts:83` | apiRequest; <code>"DELETE"</code> | <code>`/test-attempt/${attemptId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts:90` | apiRequest; <code>"POST"</code> | <code>`/test-attempt/${attemptId}/admin/submit`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-upload-api.ts:22` | apiFormRequest; <code>POST</code> | <code>"/api/user-import/parse"</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:28` | apiRequest; <code>GET</code> | <code>`/api/user-import/sessions/${sessionId}`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:35` | apiRequest; <code>"PUT"</code> | <code>`/api/user-import/sessions/${sessionId}`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:46` | apiRequest; <code>"POST"</code> | <code>`/api/user-import/sessions/${sessionId}/commit`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:58` | apiRequest; <code>GET</code> | <code>`/api/user-import/jobs?limit=${limit}`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:65` | apiRequest; <code>GET</code> | <code>`/api/user-import/jobs/${jobId}`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:72` | apiRequest; <code>GET</code> | <code>`/api/user-import/jobs/${jobId}/report`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:76` | apiRequest; <code>GET</code> | <code>"/external-tests"</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:90` | apiFormRequest; <code>POST</code> | <code>"/api/external-test-results-import/parse"</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:96` | apiRequest; <code>GET</code> | <code>`/api/external-test-results-import/sessions/${sessionId}`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:103` | apiRequest; <code>"PUT"</code> | <code>`/api/external-test-results-import/sessions/${sessionId}`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:114` | apiRequest; <code>"POST"</code> | <code>`/api/external-test-results-import/sessions/${sessionId}/commit`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:142` | apiFormRequest; <code>POST</code> | <code>"/api/card-import/parse"</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:149` | apiRequest; <code>"PUT"</code> | <code>`/api/card-import/sessions/${sessionId}`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:160` | apiRequest; <code>"POST"</code> | <code>`/api/card-import/sessions/${sessionId}/commit`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:174` | apiRequest; <code>"POST"</code> | <code>"/api/card-transform/sessions"</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:185` | apiRequest; <code>"POST"</code> | <code>`/api/card-transform/sessions/${sessionId}/commit`</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:194` | apiRequest; <code>"POST"</code> | <code>"/api/test-import/preview"</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-upload-api.ts:203` | apiRequest; <code>"POST"</code> | <code>"/api/test-import/commit"</code> | production Upload branch; old transform после BFF return |
| `cpm_front_new/lib/admin/admin-users-api.ts:39` | apiRequest; <code>GET</code> | <code>"/api/get-students"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:63` | apiRequest; <code>"POST"</code> | <code>"/api/get-users-by-role"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:75` | apiRequest; <code>"POST"</code> | <code>"/api/add-staff-user"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:95` | apiRequest; <code>"PUT"</code> | <code>"/api/edit-staff-user"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:106` | apiRequest; <code>"POST"</code> | <code>"/api/reset-staff-password"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:114` | apiRequest; <code>GET</code> | <code>"/api/get-groups"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:135` | apiRequest; <code>GET</code> | <code>`/api/groups/overview?${qs}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:142` | apiRequest; <code>GET</code> | <code>`/api/groups/${groupId}/members`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:153` | apiRequest; <code>GET</code> | <code>`/api/groups/search?${qs}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:159` | apiRequest; <code>GET</code> | <code>"/api/get-unsigned-proctors-students"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:166` | apiRequest; <code>"POST"</code> | <code>"/api/add-student"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:186` | apiRequest; <code>"PUT"</code> | <code>"/api/edit-student"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:197` | apiRequest; <code>"POST"</code> | <code>"/api/delete-user"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:208` | apiRequest; <code>"POST"</code> | <code>"/api/change-group-student"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:219` | apiRequest; <code>"POST"</code> | <code>"/api/remove-groupd-id-student"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:230` | apiRequest; <code>"POST"</code> | <code>"/api/change-group-proctor"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:241` | apiRequest; <code>"POST"</code> | <code>"/api/remove-groupd-id-proctor"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:251` | apiRequest; <code>"POST"</code> | <code>"/api/add-group"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:262` | apiRequest; <code>"PUT"</code> | <code>"/api/edit-group"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/admin-users-api.ts:277` | apiRequest; <code>"POST"</code> | <code>"/api/delete-group"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/admin/ratings-report-api.ts:7` | apiRequest; <code>GET</code> | <code>"/ratings-report"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/api/client.ts:45` | fetch; <code>GET</code> | <code>url</code> | transport: legacy branch недостижима в BFF |
| `cpm_front_new/lib/api/client.ts:70` | fetchWithTimeout; <code>GET</code> | <code>`${API_BASE_URL}${path}`</code> | transport: legacy branch недостижима в BFF |
| `cpm_front_new/lib/api/client.ts:112` | fetchWithTimeout; <code>options.method ?? "POST"</code> | <code>`${API_BASE_URL}${path}`</code> | transport: legacy branch недостижима в BFF |
| `cpm_front_new/lib/attendance/attendance-api.ts:20` | apiRequest; <code>GET</code> | <code>"/api/attendance-types"</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:38` | apiRequest; <code>GET</code> | <code>`/api/class-days${buildQuery({ date_from: dateFrom, date_to: dateTo })}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:45` | apiRequest; <code>GET</code> | <code>`/api/class-days/${classDayId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:55` | apiRequest; <code>"POST"</code> | <code>"/api/class-days"</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:65` | apiRequest; <code>"PUT"</code> | <code>`/api/class-days/${classDayId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:74` | apiRequest; <code>"DELETE"</code> | <code>`/api/class-days/${classDayId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:80` | apiRequest; <code>GET</code> | <code>`/api/class-days/${classDayId}/attendance`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:95` | apiRequest; <code>"POST"</code> | <code>`/api/class-days/${classDayId}/attendance`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:105` | apiRequest; <code>"DELETE"</code> | <code>`/api/class-days/${classDayId}/attendance/${attendanceId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:116` | apiRequest; <code>GET</code> | <code>`/api/students/${studentId}/class-day-attendance${buildQuery({ date_from: dateFrom, date_to: dateTo, })}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:131` | apiRequest; <code>"POST"</code> | <code>"/api/get-class-name-by-studID"</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-api.ts:163` | apiRequest; <code>GET</code> | <code>`/api/get-zap/${zapId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-report-api.ts:24` | apiRequest; <code>GET</code> | <code>`/api/attendance-report${buildQuery({ date_from: dateFrom, date_to: dateTo, })}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/attendance/attendance-report-api.ts:42` | apiRequest; <code>"POST"</code> | <code>`/api/zap-dates/${zapDateId}/unlink`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/auth/api.ts:37` | apiRequest; <code>"POST"</code> | <code>"/api/auth"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/auth/api.ts:82` | apiRequest; <code>"POST"</code> | <code>"/api/aun"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/auth/api.ts:97` | apiRequest; <code>"POST"</code> | <code>"/api/logout"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/bff/client.ts:38` | fetch; <code>GET</code> | <code>`/bff${path}`</code> | активный same-origin BFF transport |
| `cpm_front_new/lib/exams/exams-api.ts:27` | apiRequest; <code>GET</code> | <code>`/get-all-exams${buildQuery({ page: params.page, limit: params.limit, search: params.search, sort: params.sort, })}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/exams/exams-api.ts:41` | apiRequest; <code>GET</code> | <code>`/get-exam-sessions/${examId}${buildQuery({ page: params.page, limit: params.limit, search: params.search, sort: params.sort, })}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/exams/exams-api.ts:55` | apiRequest; <code>GET</code> | <code>`/get-student-exam-sessions/${studentId}${buildQuery({ page: params.page, limit: params.limit, grade: params.grade === "all" ? undefined : params.grade, sort: params.sort, })}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/exams/exams-api.ts:68` | apiRequest; <code>GET</code> | <code>`/exams/${examId}/delete-preview`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/exams/exams-api.ts:72` | apiRequest; <code>"DELETE"</code> | <code>`/exams/${examId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/exams-v2/api.ts:88` | apiRequest; <code>GET</code> | <code>`${path}${queryString(params)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/exams-v2/api.ts:112` | apiRequest; <code>command.method</code> | <code>command.path</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/exams-v2/api.ts:142` | apiFormRequest; <code>POST</code> | <code>`${importPrefix(examId, kind)}/parse`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/exams-v2/api.ts:153` | apiRequest; <code>GET</code> | <code>"/directions"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/homework-files/api.ts:70` | fetch; <code>GET</code> | <code>`${HOMEWORK_SERVICE_URL}${path}`</code> | transport: legacy branch недостижима в BFF |
| `cpm_front_new/lib/proctor/proctor-api.ts:29` | apiRequest; <code>GET</code> | <code>`/api/get-homeworks${buildQuery({ type: options?.type, page: options?.page ?? 1, limit: options?.limit ?? 100, })}`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/proctor/proctor-api.ts:43` | apiRequest; <code>"POST"</code> | <code>"/api/student-group-filter"</code> | production branch; BFF → Core |
| `cpm_front_new/lib/proctor/proctor-api.ts:54` | apiRequest; <code>"POST"</code> | <code>"/api/get-homework-sessions"</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/proctor/proctor-api.ts:70` | apiRequest; <code>"POST"</code> | <code>"/api/pass_homework"</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/proctor/proctor-api.ts:88` | apiRequest; <code>"POST"</code> | <code>"/api/pass_homework_bulk"</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/proctor/proctor-api.ts:102` | apiRequest; <code>"POST"</code> | <code>"/api/edit-homework-session"</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/schedule/schedule-api.ts:18` | apiRequest; <code>GET</code> | <code>`/api/schedule?${qs.toString()}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/schedule/schedule-api.ts:24` | apiRequest; <code>"POST"</code> | <code>"/api/schedule"</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/schedule/schedule-api.ts:34` | apiRequest; <code>"PUT"</code> | <code>`/api/schedule/${lessonId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/schedule/schedule-api.ts:43` | apiRequest; <code>"DELETE"</code> | <code>`/api/schedule/${lessonId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/schedule/schedule-api.ts:51` | apiRequest; <code>"POST"</code> | <code>"/api/schedule/bulk"</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/student/homework-api.ts:29` | apiRequest; <code>GET</code> | <code>`/api/homeworks/student-with-sessions?${searchParams.toString()}`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/student/performance-api.ts:5` | apiRequest; <code>GET</code> | <code>"/my-rating"</code> | функция не вызывается current screen |
| `cpm_front_new/lib/student/test-attempt-api.ts:19` | apiRequest; <code>"POST"</code> | <code>"/test-attempt/start"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-attempt-api.ts:45` | apiRequest; <code>"POST"</code> | <code>`/test-attempt/${attemptId}/commits`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-attempt-api.ts:56` | apiRequest; <code>"POST"</code> | <code>`/test-attempt/${attemptId}/finalize`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-attempt-api.ts:70` | apiRequest; <code>GET</code> | <code>`/test-attempt/active?testId=${encodeURIComponent(testId)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-attempt-api.ts:79` | apiRequest; <code>GET</code> | <code>`/test-attempt/${attemptId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-attempt-api.ts:90` | apiRequest; <code>"PATCH"</code> | <code>`/test-attempt/${attemptId}/answer`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-attempt-api.ts:110` | apiRequest; <code>"POST"</code> | <code>`/test-attempt/${attemptId}/practice-answer`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-attempt-api.ts:137` | apiRequest; <code>"POST"</code> | <code>`/test-attempt/${attemptId}/answers`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-attempt-api.ts:147` | apiRequest; <code>"POST"</code> | <code>`/test-attempt/${attemptId}/submit`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/test-review-api.ts:11` | apiRequest; <code>GET</code> | <code>`/test-session/${encodeURIComponent(sessionId)}/review`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/tests-api.ts:16` | apiRequest; <code>GET</code> | <code>"/directions"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/tests-api.ts:29` | apiRequest; <code>GET</code> | <code>`/tests/student/available?${params.toString()}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/student/tests-api.ts:45` | apiRequest; <code>GET</code> | <code>`/tests/${encodeURIComponent(directionName)}/with-sessions?${params.toString()}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/supervisor/supervisor-homework-api.ts:37` | apiRequest; <code>GET</code> | <code>`/api/get-ov-homework-table${BFF_DEV &amp;&amp; period ? `?date_from=${period.dateFrom}&amp;date_to=${period.dateTo}` : ""}`</code> | активное старое имя → Homework mapper |
| `cpm_front_new/lib/training/admin-training-api.ts:15` | apiRequest; <code>GET</code> | <code>"/get-admin-training-catalog"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/admin-training-api.ts:31` | apiRequest; <code>GET</code> | <code>`/admin-cards-by-theme/${themeId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/admin-training-api.ts:45` | apiRequest; <code>"POST"</code> | <code>"/create-training-theme"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/admin-training-api.ts:56` | apiRequest; <code>"PUT"</code> | <code>`/training-theme/${themeId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/admin-training-api.ts:64` | apiRequest; <code>"DELETE"</code> | <code>`/training-theme/${themeId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/admin-training-api.ts:79` | apiRequest; <code>"POST"</code> | <code>"/create-card"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/admin-training-api.ts:96` | apiRequest; <code>"PUT"</code> | <code>`/card/${cardId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/admin-training-api.ts:104` | apiRequest; <code>"DELETE"</code> | <code>`/card/${cardId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/training-api.ts:78` | apiRequest; <code>GET</code> | <code>`/get-training-tree/${studentId}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/training-api.ts:107` | apiRequest; <code>GET</code> | <code>`/training-sections/${studentId}/${directionId}${query.toString() ? `?${query.toString()}` : ""}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/training-api.ts:126` | apiRequest; <code>GET</code> | <code>`/section-study/${studentId}/${sectionKind}/${encodeURIComponent(sectionRefId)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/training-api.ts:145` | apiRequest; <code>GET</code> | <code>`/section-batch/${studentId}/${sectionKind}/${encodeURIComponent(sectionRefId)}/${batchIndex}${query}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/training-api.ts:162` | apiRequest; <code>"PUT"</code> | <code>`/section-study-settings/${studentId}/${sectionKind}/${encodeURIComponent(sectionRefId)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/training-api.ts:176` | apiRequest; <code>"POST"</code> | <code>"/mark-card-learned"</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/training/training-api.ts:189` | apiRequest; <code>"DELETE"</code> | <code>`/mark-card-learned/${studentId}/${encodeURIComponent(cardRef)}`</code> | production branch; ранний BFF return/ternary |
| `cpm_front_new/lib/zaps/zaps-api.ts:78` | apiRequest; <code>"POST"</code> | <code>"/api/create-zap"</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/zaps/zaps-api.ts:87` | apiRequest; <code>"POST"</code> | <code>"/api/get-zaps-student"</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/zaps/zaps-api.ts:99` | apiRequest; <code>GET</code> | <code>`/api/get-zap/${zapId}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/zaps/zaps-api.ts:117` | apiRequest; <code>GET</code> | <code>`/api/get-all-zaps${buildQuery({ status, page: params.page, limit: params.limit, })}`</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/zaps/zaps-api.ts:150` | apiRequest; <code>"POST"</code> | <code>"/api/process-zap"</code> | старый screen заменён / не выбран |
| `cpm_front_new/lib/zaps/zaps-api.ts:157` | apiRequest; <code>"POST"</code> | <code>`/api/zap-dates/${dateId}/retry`</code> | старый screen заменён / не выбран |

## Историческое приложение B. BFF_DEV переключения в срезе 18:02 MSK

Все не-import использования BFF_DEV в рассмотренных TS/TSX, включая if, ternary, JSX, rights и query adaptation. Отсутствие flag в Homework API module безопасно благодаря общему mapper. Test-файлы не включены. Актуально: 383 строк в 92 файлах; AST transport index выше — 181 call sites, добавленных/удалённых legacy вызовов после readiness изменений нет.

| Source file | Строки BFF_DEV |
|---|---|
| `cpm_front_new/app/layout.tsx` | `40` |
| `cpm_front_new/components/admin/access/admin-access-section.tsx` | `19`, `30`, `31`, `34`, `71`, `77`, `97`, `108`, `111`, `119`, `126`, `131` |
| `cpm_front_new/components/admin/dashboard/admin-dashboard-section.tsx` | `69`, `71` |
| `cpm_front_new/components/admin/homework/admin-homework-section.tsx` | `161`, `199`, `206`, `207` |
| `cpm_front_new/components/admin/homework/homework-monitoring-section.tsx` | `40`, `42` |
| `cpm_front_new/components/admin/ratings/admin-rating-jobs-tab.tsx` | `91`, `96` |
| `cpm_front_new/components/admin/ratings/admin-rating-recalc-panel.tsx` | `30` |
| `cpm_front_new/components/admin/ratings/admin-ratings-section.tsx` | `46`, `64`, `65`, `103`, `184`, `203`, `204` |
| `cpm_front_new/components/admin/schools/admin-school-panel.tsx` | `29`, `56`, `57`, `65`, `66` |
| `cpm_front_new/components/admin/schools/admin-schools-section.tsx` | `128`, `139`, `193`, `205` |
| `cpm_front_new/components/admin/test-results/admin-test-results-section.tsx` | `97`, `225`, `245`, `318`, `346` |
| `cpm_front_new/components/admin/tests/admin-create-test-menu.tsx` | `91`, `92` |
| `cpm_front_new/components/admin/tests/admin-test-attempts-panel.tsx` | `136` |
| `cpm_front_new/components/admin/tests/admin-test-draft-editor.tsx` | `483`, `1239`, `1434`, `2667`, `2668`, `2969`, `3364`, `3365`, `3375` |
| `cpm_front_new/components/admin/tests/admin-test-form.tsx` | `84`, `85`, `118`, `206`, `306`, `335`, `347`, `359`, `360`, `368`, `461`, `527`, `566`, `598` |
| `cpm_front_new/components/admin/tests/admin-test-sessions-panel.tsx` | `104`, `109`, `122`, `135`, `161`, `185`, `203`, `219`, `244`, `293`, `294`, `299`, `306` |
| `cpm_front_new/components/admin/tests/admin-test-workspace.tsx` | `47`, `66`, `82`, `90` |
| `cpm_front_new/components/admin/tests/admin-tests-section.tsx` | `127`, `136`, `173`, `191`, `202`, `342`, `407`, `423`, `707`, `708`, `714`, `738`, `762`, `821`, `855`, `864`, `871`, `881`, `889`, `895`, `896`, `897`, `898`, `899`, `914` |
| `cpm_front_new/components/admin/training/admin-cards-to-draft-modal.tsx` | `41`, `104`, `147`, `212`, `217` |
| `cpm_front_new/components/admin/training/admin-training-section.tsx` | `113`, `180`, `204`, `214`, `331`, `786` |
| `cpm_front_new/components/admin/upload/admin-upload-section.tsx` | `53` |
| `cpm_front_new/components/admin/users/admin-group-form-panel.tsx` | `25` |
| `cpm_front_new/components/admin/users/admin-groups-tab.tsx` | `44`, `184`, `332`, `347`, `353`, `381`, `392` |
| `cpm_front_new/components/admin/users/admin-staff-panel.tsx` | `48`, `64`, `81`, `88`, `123`, `131`, `135`, `173`, `185`, `186`, `187` |
| `cpm_front_new/components/admin/users/admin-staff-tab.tsx` | `85`, `89`, `116`, `139`, `172`, `196`, `215`, `256` |
| `cpm_front_new/components/admin/users/admin-student-panel.tsx` | `37`, `69`, `70`, `81`, `96`, `119`, `127`, `131`, `191`, `200`, `227`, `244`, `245` |
| `cpm_front_new/components/admin/users/admin-students-tab.tsx` | `105`, `109`, `236`, `258`, `311` |
| `cpm_front_new/components/admin/users/admin-users-section.tsx` | `34` |
| `cpm_front_new/components/admin/users/core-access.tsx` | `10`, `14`, `19`, `20` |
| `cpm_front_new/components/exams-v2/admin-assignments.tsx` | `349` |
| `cpm_front_new/components/exams-v2/admin-delete.tsx` | `94` |
| `cpm_front_new/components/exams-v2/admin-results.tsx` | `225` |
| `cpm_front_new/components/exams-v2/admin.tsx` | `108`, `147`, `149`, `339`, `345`, `387`, `388`, `452`, `548`, `583`, `686`, `687` |
| `cpm_front_new/components/exams-v2/examiner.tsx` | `116`, `519` |
| `cpm_front_new/components/exams-v2/import.tsx` | `350` |
| `cpm_front_new/components/exams-v2/section.tsx` | `22`, `57` |
| `cpm_front_new/components/exams-v2/shared.tsx` | `371` |
| `cpm_front_new/components/exams-v2/student.tsx` | `138`, `139` |
| `cpm_front_new/components/layout/header.tsx` | `37` |
| `cpm_front_new/components/proctor/proctor-homework-type-toggle.tsx` | `31` |
| `cpm_front_new/components/sections/section-content.tsx` | `54`, `66`, `74` |
| `cpm_front_new/components/student/performance/attendance-qr-card.tsx` | `20`, `60` |
| `cpm_front_new/components/student/performance/student-performance-section.tsx` | `32` |
| `cpm_front_new/components/student/tests/attempt/official-test-attempt-screen.tsx` | `88`, `337` |
| `cpm_front_new/components/student/tests/student-tests-section.tsx` | `154`, `173`, `223`, `233` |
| `cpm_front_new/components/student/training/student-training-section.tsx` | `62` |
| `cpm_front_new/components/student/training/training-flashcards.tsx` | `72`, `134`, `320` |
| `cpm_front_new/components/student/training/training-section-detail.tsx` | `135`, `258`, `479` |
| `cpm_front_new/components/student/training/training-topics-view.tsx` | `185` |
| `cpm_front_new/components/supervisor/reports/supervisor-attendance-section.tsx` | `15` |
| `cpm_front_new/components/supervisor/reports/supervisor-exams-section.tsx` | `15` |
| `cpm_front_new/components/supervisor/reports/supervisor-ratings-section.tsx` | `10` |
| `cpm_front_new/components/supervisor/reports/supervisor-tests-section.tsx` | `15` |
| `cpm_front_new/components/supervisor/supervisor-dashboard-section.tsx` | `52`, `54` |
| `cpm_front_new/components/training/cards-command-recovery.tsx` | `9`, `11`, `13`, `14` |
| `cpm_front_new/contexts/AuthContext.tsx` | `74`, `111` |
| `cpm_front_new/lib/admin/admin-access-api.ts` | `14`, `15`, `16`, `17`, `18` |
| `cpm_front_new/lib/admin/admin-homework-api.ts` | `22` |
| `cpm_front_new/lib/admin/admin-homework-utils.ts` | `17`, `30` |
| `cpm_front_new/lib/admin/admin-ratings-api.ts` | `12`, `19`, `28`, `34`, `41`, `44`, `46`, `50` |
| `cpm_front_new/lib/admin/admin-schools-api.ts` | `16`, `28`, `38`, `53`, `66`, `80`, `91`, `101`, `122` |
| `cpm_front_new/lib/admin/admin-test-drafts-api.ts` | `12`, `19`, `27`, `34`, `42`, `50`, `61`, `68`, `75` |
| `cpm_front_new/lib/admin/admin-test-results-api.ts` | `12`, `22` |
| `cpm_front_new/lib/admin/admin-tests-api.ts` | `20`, `27`, `36`, `43`, `53`, `178`, `189`, `201`, `223`, `232`, `248` |
| `cpm_front_new/lib/admin/admin-tests-monitoring-api.ts` | `30`, `38`, `48`, `57`, `66`, `75`, `82`, `89` |
| `cpm_front_new/lib/admin/admin-users-api.ts` | `38`, `62`, `74`, `94`, `105`, `113`, `123`, `141`, `149`, `157`, `158`, `165`, `185`, `196`, `207`, `218`, `229`, `240`, `250`, `261`, `276` |
| `cpm_front_new/lib/admin/ratings-report-api.ts` | `6` |
| `cpm_front_new/lib/api/client.ts` | `56`, `100` |
| `cpm_front_new/lib/auth/admin-access.ts` | `12` |
| `cpm_front_new/lib/auth/api.ts` | `32`, `71`, `95` |
| `cpm_front_new/lib/auth/roles.ts` | `15` |
| `cpm_front_new/lib/auth/storage.ts` | `5`, `13`, `18` |
| `cpm_front_new/lib/bff/use-context-guard.ts` | `7` |
| `cpm_front_new/lib/bff/use-homework-types.ts` | `6`, `8` |
| `cpm_front_new/lib/config.ts` | `1`, `3`, `8`, `12` |
| `cpm_front_new/lib/exams-v2/api.ts` | `82`, `104`, `126`, `137`, `152`, `166`, `168` |
| `cpm_front_new/lib/exams-v2/hooks.ts` | `92` |
| `cpm_front_new/lib/homework-files/api.ts` | `59`, `145` |
| `cpm_front_new/lib/navigation/examinator.ts` | `12` |
| `cpm_front_new/lib/navigation/index.ts` | `22`, `24`, `28` |
| `cpm_front_new/lib/navigation/proctor.ts` | `15` |
| `cpm_front_new/lib/navigation/supervisor.ts` | `21` |
| `cpm_front_new/lib/proctor/proctor-api.ts` | `42` |
| `cpm_front_new/lib/student/test-attempt-api.ts` | `18`, `44`, `55`, `65`, `78`, `86`, `109`, `132`, `146` |
| `cpm_front_new/lib/student/test-attempt-db.ts` | `4` |
| `cpm_front_new/lib/student/test-attempt-v2-store.ts` | `267`, `333` |
| `cpm_front_new/lib/student/test-review-api.ts` | `10` |
| `cpm_front_new/lib/student/tests-api.ts` | `15`, `24`, `40`, `232` |
| `cpm_front_new/lib/supervisor/supervisor-homework-api.ts` | `37` |
| `cpm_front_new/lib/training/admin-training-api.ts` | `14`, `30`, `44`, `55`, `63`, `75`, `92`, `103` |
| `cpm_front_new/lib/training/training-api.ts` | `77`, `99`, `124`, `125`, `142`, `143`, `160`, `161`, `174`, `175`, `187`, `188` |
| `cpm_front_new/lib/training/training-routes.ts` | `37`, `151` |

## Историческое приложение C. Readiness guard до joint External Tests PASS

Полный индекс не-import использования BFF_EXTERNAL_TESTS_DEV после семи минимальных common integration изменений. Specialized external components/adapters/owner не изменены; их unit test mock проверяет включённый будущий контракт. Новый `cpm_front_new/lib/bff/tests-cutover-readiness.test.ts` содержит три regression для обычных admin catalog/results и student zero при недоступном external owner.

| Source file | Строки readiness guard |
|---|---|
| `cpm_front_new/components/admin/test-results/admin-test-results-section.tsx` | `272` |
| `cpm_front_new/components/admin/tests/admin-create-test-menu.tsx` | `91`, `92` |
| `cpm_front_new/components/sections/section-content.tsx` | `74` |
| `cpm_front_new/components/student/performance/student-performance-section.tsx` | `33` |
| `cpm_front_new/lib/bff/tests-admin.ts` | `76`, `206` |
| `cpm_front_new/lib/bff/tests-student.ts` | `75` |
| `cpm_front_new/lib/config.ts` | `3` |
