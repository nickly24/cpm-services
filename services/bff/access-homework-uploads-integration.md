# Access, организация, Homework settings/PDF и Common Uploads

Состояние **2026-10-09** по source и сохранённым reports. Access/Core organization и Homework используют существующий React UI и public Go owners через BFF. Core U06 privacy, native Attendance U04, protected templates и Core external report приняты. BFF 296 и optimized UI 535 работают; U04 recovery UI 15, U07 canonical result access 30 API / 6 UI и Common Uploads remaining 21 завершены. Native preview/delivery и common UI не означают background apply: Auth-авторизация ещё не реализована. Core dashboard f3f43d… actual READY; 2 read-only native+BFF сценария / 31 checks PASS, без нового React capabilities wiring. Три общих QA остаются PREPARE, общего RELEASE нет.

| Область | Подтверждено | Реальная оставшаяся граница |
| --- | --- | --- |
| Access | Roles CRUD, scoped назначения/отзыв, live права, unknown-response canonical recovery; предыдущие 42 targeted/15 API/15 browser, optimized read-only 5 | Новый Auth import grant не является частью Access role CRUD |
| Organization/divisions/directions/memberships | Новый workspace и native owner contracts;18 own-fixture CRUD +8 read-only; header scope 3 | Shared org 1001 rename не запускался; global org create не требуется |
| Homework settings/audit | Семь настроек, readonly staff, unknown PUT GET recovery, cursor audit;24 unit/92 HTTP/19 browser/15 read-only | Нет нового owner revision/idempotency contract для settings |
| Homework delta | Native PDF worker/S3, accepted job limits/retry, class report/XLSX, honest monitoring;11 joint +6 read-only | Simulated stale/read failure в browser не являются реальной outage проверкой |
| External Tests |15 joint UI/BFF: CRUD/manual results/history/zero/null/privacy/scope/mobile/lost reply | Общий Uploads async apply отдельный и всё ещё pending |
| Common Uploads |17 BFF operations, live native previews, owner 839 HTTP и Rabbit 11 jobs/restart; catalog commit:false | Auth approval/implementation и защищённый UNKNOWN исходной credential-команды; UI acceptance 21 завершён |

Полная карта [ui-cutover-inventory.md](ui-cutover-inventory.md), свежие доказательства [ui-audit-2026-10-09.md](ui-audit-2026-10-09.md), Uploads [dependencies](uploads-contract-gaps.md).

## Access и управление организацией

Entrypoint «Доступ и роли» — `/cabinet/admin/access`. Управление ролями требует live `role=admin && organization_admin=true`; delegated staff не получает его через access permission. Роли общие для организации, roster/counts и обычные assignments относятся к выбранному подразделению. Без выбранного division неизвестный счётчик не подменяется 0.

Новый `CoreOrganizationWorkspace` имеет вкладки организация/подразделения/направления/участия. Admin может переименовать свою организацию, создать/переименовать/архивировать подразделения и направления, выбрать существующий org account и целевое подразделение, добавить participation/изменить роль либо отозвать participation. Staff получает доступные каталоги readonly без org-wide user picker и writes. Создание организаций, новый глобальный администратор и новый личный profile editor не добавлены.

| Действие | Browser BFF → Core owner |
| --- | --- |
| Roles catalog / CRUD | GET/POST `/core/roles`, PUT/DELETE `/core/roles/{role}` → `/v1/core/roles*` |
| Users/profile/account state | `/core/users*`; PUT credentials/active → selected division owner → Auth где нужно |
| Current division membership | PUT/DELETE `/core/memberships/{account}` → `/v1/core/divisions/{session.division_id}/memberships/{account}` |
| Current organization rename | GET/PUT `/core/organization` → `/v1/core/organization` |
| Divisions / directions CRUD | GET/POST `/core/{divisions,directions}`, PUT/DELETE `/{id}` → ownOrg catalog |
| Org existing account / all ownOrg participations | GET `/core/organization/users`; `/core/organization/users/{account}/memberships` |
| Explicit target division users / membership | GET `/core/divisions/{division}/users`; PUT/DELETE `/core/divisions/{division}/memberships/{account}`; ownOrg/live permission fence |

`account_id` и `profile_id` различаются. Credentials GET отсутствует; новый пароль не хранится в roster/recovery journal/report. Профиль обновляется полным свежим DTO с сохранением остальных полей. Membership DTO содержит `can_assign`, global/per-member `can_revoke`, division_name, role_id/name, group_id. Foreign-owned account нельзя заново назначить или менять роль через чужую организацию; fulladmin может native revoke уже существующего собственного-org участия при can_revoke и окончательных owner проверках. Его foreignOrg участия не выдаются.

Delete participation сохраняет аккаунт, другие подразделения и историю. Owner 409 защищает последнее участие, закреплённый рейтинг/attendance history и используемую роль; archive populated division запрещён. Organization/division/direction/role/membership writes не получают выдуманные CAS/receipts. Перед dispatch UI сохраняет несекретный ожидаемый результат, удерживает guard; unknown POST/PUT/DELETE не повторяет автоматически. Canonical GET проверяет существование, точные значения либо отсутствие. Foreign-owned account после последнего currentOrg revoke может стать 404: только exhaustive org catalog absence подтверждает DELETE; PUT 404 не считается успехом.

Source: `lib/bff/core-access.ts`, `core-organization.ts`, `core-admin.ts`; `components/admin/access/admin-access-section.tsx`, `components/admin/organization/core-organization-workspace.tsx`; BFF `internal/bff/core.go`; Core `internal/core/routes.go`, `organization.go`, `users.go`.

## Homework настройки, аудит, PDF и отчёт

Настройки/аудит доступны из Assignments и Monitoring, подписаны selected division. Dirty/unknown/save держат Web Lock, смена org/div сбрасывает старые панели и queries. Readonly просмотр не блокирует switch.

| Owner endpoint | Права |
| --- | --- |
| GET `/bff/homework/settings` | Fullorg admin; staff assignments.view или monitoring.view; native student/proctor API reads не добавляют новый menu |
| PUT `/bff/homework/settings` | Только full organization admin |
| GET `/bff/homework/audit?after=&limit=` | Только full organization admin |

Канонические settings: source/output bytes 1..104857600, pages 1..200, dpi 72..300, jpeg_quality 35..95, presign_ttl 60..900, upload_stale от 2 × TTL до 86400. Полный PUT не имеет revision/idempotency; unknown outcome сверяется GET без auto-save, совпадение не доказывает авторство последней записи. Audit сохраняет owner cursor order; limit 51 для 50 видимых записей, without fake total/has_more.

Workspace отдаёт source/output/pages/dpi/jpeg_quality. BFF UI не подставляет старые 10 MiB/35 pages при отсутствующих limits. Accepted job хранит собственные immutable limits; retry не заимствует новые workspace settings. Предварительный source reject, scanner bounds, output/page errors и accepted-job policy показываются отдельно. PDF идёт напрямую presigned S3, далее File-work/Rabbit; Common Uploads не дублирует upload/complete/submit/retry/cancel.

Класс из Homework report сохраняет null и 0 отдельно, отображается и экспортируется в XLSX. Monitoring различает worker/storage unknown/stale/error, broker/readiness и неподтверждённый последний ответ после read failure. Нельзя показывать старую зелёную карточку как свежую после ошибки. Native recent journal не содержит accepted limits: UI честно использует generic diagnosis, не делает недопустимые student job GET.

Source: `lib/bff/homework.ts`, `homework-settings-audit.ts`, `lib/homework-files/{types,limits,api}.ts`; `components/homework/*`, admin monitoring/infrastructure, supervisor homework report/export.

## Common Uploads: маршруты и фактический flow

Все route prefixes ниже `/bff/uploads/`, owner `/v1/uploads/divisions/{session.division_id}/…`. Файл/session/job/receipt pinned actor/org/div; browser scope/body не выбирает чужую область. Native UUID command и coordinator expected_revision/owner_revision не смешиваются.

| Метод | Paths | Число |
| --- | --- | --- |
| GET | catalog, sessions, sessions/{id}, jobs, jobs/{id}, jobs/{id}/report, files/{id}, files/{id}/download, receipts/{command}, events, templates/{type} |11|
| POST | sessions, sessions/{id}/commit, sessions/{id}/reconcile, jobs/{id}/retry, files/register |5|
| PUT | sessions/{id} |1|

Native preview Core credential rows readonly без паролей; legacy users показывают предметные ошибки/matching; Cards сохраняет полный multiline ordered cards input; Exams собирает полный до 5000 rows snapshot, native edit changes и consistent revision/total; Tests JSON/OnlineTestPad/external rows проверяет protected validator Tests. Partial/errors/expired/пустой snapshot не превращаются в успешный import. Источники credentials не сохраняются/не скачиваются; остальные источники bounded и expire.

UI common center использует catalog/sessions/edit/reconcile/receipt/jobs/template/download. `files/register`, files metadata и events имеют **API-only/backend helper** proof: Homework 57 и Attendance 290 native joint checks. UI не монтирует registry/events flow. Homework.pdf/Attendance images/cards.transform показывают ссылки в Assignments/Zaps/Cards владельцев; это не второй lifecycle или hidden common apply.

Каждый administrative action требует live upload плюс subject (`users/train/tests/test-results/exams`), Cards transform дополнительно Tests. Rabbit сообщения содержат только references, не bearer/source rows/password. Delivery принят, но executor всегда `awaiting_authorization/background_authorization_pending`, progress 0/domain apply 0, report 409, catalog commit:false. Новая Auth delegation [ожидает прямого согласия](../../outputs/microservices/auth-background-import-approval.md) после automatic approval rejection. Это отдельный blocker всего common background apply, а не остановленный owner 8014.

Protected template GET входит в 17 операций. Baseline 839 проверял прежний template набор; новый native Uploads 284 checks / 130 HTTP отдельно подтвердил legacy/Tests templates, account/profileIDs, точные bytes/MIME и download→native preview. Core legacy template 78 native DONE. Native proof не подменяет browser/BFF: common UI acceptance 21 получен отдельно после Directions/U07 fixes на UI 535.

Исходный credential UUID `44083500-b582-4c23-bd1c-67c2e9e367a7` имеет 5 protected recovery PASS (`bff-uploads-original-recovery-http-report.json`): original actor/org/div/session/receipt сохранены, aged staging→outcome_unknown, exact native receipt 404 и reconcile не сняли uncertainty. Business writes/source_resends/new keys 0; это не доказательство failure/no execution/apply. Новый preview не разрешает auto replay.

## Evidence и безопасный повтор

| Уровень | Report / результат |
| --- | --- |
| Access initial joint | bff-core-access-readonly-api-report 15; bff-core-access-crud/optimized-browser-report 10 + 5 |
| Organization new joint | bff-core-organization-crud/optimized-browser-report 18 + 8; bff-core-header-scope-browser-report 3 |
| HW settings/audit initial | bff-homework-settings-audit-http-report 92, browser 19, optimized 15 |
| HW owner handoff | [handoff](../homework/docs/handoff-2026-10-09.md):459 assertions с cleanup; не BFF joint |
| HW new joint | bff-homework-delta-crud/optimized-browser-report 11 + 6; PDF worker/S3, immutable retry, class/XLSX/monitoring |
| Uploads owner | [verification](../uploads/docs/verification.md), uploads-http-report 839, Rabbit 11 jobs/restart; subject_apply_tested=false/browser_or_bff_tested=false |
| Uploads historical unavailable | bff-uploads-unavailable-browser-report 7, live_uploads_pass=false; прежнее stopped окно |
| Uploads protected original recovery | bff-uploads-original-recovery-http-report 5 PASS, outcome_unknown preserved, source_resends/new keys/business writes 0; native HTTP recovery, не browser/apply proof |
| Uploads templates native | uploads-template-http-report 284 checks / 130 HTTP, no import commit/catalog/SQL writes, browser_or_bff_tested=false |
| Uploads remaining joint | bff-uploads-delta-independent-remaining-report 21 = actual 19 checkpoint + readonly tail 2; cumulative 6 preview POST / 2 CAS PUT по explicit amendment, known wrong fixture retained, source/apply 0, pending 0, Auth/browser closed |
| Canonical result access U07 | bff-tests-results-access-http-report 30 API / browser-report 6 UI; 5 isolated Core commands confirmed, no Tests source writes, false rights hide reads/navigation |
| Attendance U04 joint | bff-attendance-recovery-report 15 UI, 27 bounded business attempts including own fixture/setup; unknown UUID/body-free metadata retained, receipt confirmation/reload/privacy/refresh failure, all closed |
| Core external report | native/BFF 67 +fresh fixture 44 (11 confirmed commands/0 pending) +actual UI 10; zero/recorded null/absence и real XLSX |
| Attendance scan | attendance-ui-scan-report 4 PASS, 3 actual XLSX; picker/loading/actual day/fallback, 0 domain writes/QR/camera |
| Attendance U04 native | attendance-command-receipts-http-report 5 actual HTTP сценариев / 925 assertions; runtime 39 ops, 11 Attendance commands / 0 Core writes, cleanup 0. Старый bulk/corruption/late commit/QR expiry/proctor-group — Go/SQL-only |
| Exams fresh CAS | bff-exams-delta-fixture-browser-report 7 PASS; одна новая явно разрешённая own-preview PUT 200 / rev 2→3 после known 403, readonly recovery 5000 rows / 25 pages; import commit/subject writes 0 |
| Optimized Cards/Tests/Ratings | bff-cards-optimized-report 15; bff-tests-optimized-report 7; bff-ratings-delta-browser-report 15, отдельные readonly proofs |
| External Tests joint | bff-external-tests-delta-report 15 PASS,0 JS/remote/legacy |

Все report filenames находятся в игнорируемом `dev/local/`; пароли не копируются. CRUD scripts создают только собственные retained fixtures publicAPI, unknown creates не повторяют. Read-only повтор не создаёт новый набор:

```sh
node services/bff/scripts/core_access_browser.mjs --read-only
node services/bff/scripts/core_organization_browser.mjs --read-only
node services/bff/scripts/homework_settings_audit_browser.mjs --read-only
node services/bff/scripts/homework_delta_browser.mjs --read-only
```

Подтверждённый runtime по `bff-final-runtime-proof.json`: Core `f3f43d…`, Attendance `b8f807f40b73…` / 39 native операций, BFF `853b0eb5…` / 296 публичных операций / 17 Uploads. Optimized UI 535 (`5da08f64…`) READY с 21:37:04 МСК; frontend suite 535 tests / 57 files, TSC/scoped lint/build PASS. BFF full race 2.935 s / vet PASS. U04 native 5 HTTP сценариев / 925 assertions и joint UI 15 приняты отдельно; U07 canonical result permissions — 30 API / 6 UI. Common Uploads remaining acceptance — 21 проверки: actual checkpoint 19 + readonly tail 2; cumulative 6 новых previews / 2 CAS PUT, source/domain apply 0, pending 0, все Auth/context/browser закрыты.

Core dashboard `f3f43d337a2ef4ba00c08cda3d85674f64c1af74cad752ad145fb09e8db9202d` actual READY после Core-only окна 22:11–22:13 МСК с 8 ACK: started `2026-10-09T19:11:19.349574138Z` (22:11:19.349 МСК), healthy/ready/OpenAPI 200. Source: 5-case permission matrix, targeted Go 1.163 s / full race 2.052 s / vet PASS. [core-dashboard-results-http-report.json](../../dev/local/core-dashboard-results-http-report.json) — 2 read-only native+BFF сценария / 31 checks PASS: один actor 548 в A10254 получает `test_results` и report 200, в B10260 — `tests`, canonical result view false и report 403. Все четыре private totals остаются null; native/BFF DTO совпадают, subject/runtime/SQL writes 0, Auth cleanup failures 0. Текущий React dashboard читает totals, но не использует capabilities; новое UI wiring не заявлено. Три общих QA остаются PREPARE; общий RELEASE не объявлен. Auth background grant/full import apply остаются отдельной нерешённой зависимостью.

Core external report 67 API/BFF + 44 fixture + 10 UI, protected templates 78/284, Attendance scan 4/3 XLSX, U06 3/20, U04 native 5/925 и Exams CAS 7 уже DONE. U04 source 9 групп Go/SQL/race/vet дополняет native proof; BFF 296 и U04 UI recovery 15 уже приняты; Core dashboard runtime и native+BFF proof 2/31 уже DONE; React capabilities wiring этим не заявлен. Известный исходный Exams 403 key сохранён; новая подтверждённая PUT не выдаётся за восстановление unknown-команды. Исторические 361/47 и ESLint 68/20 не являются текущим final total. Core 005/school timestamps limitation сохраняется. Нагрузка, CI/CD и production исключены текущим поручением; Auth background approval/full apply — реальные отдельные зависимости.
