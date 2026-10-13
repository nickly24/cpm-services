# Common Uploads: зависимости и подтверждённые границы

Срез **2026-10-09** по сохранённым отчётам. На 8014 работает Rabbit coordinator; прежний stop был отозван. Native preview/edit, доставка и protected templates подтверждены. Полное фоновое применение не завершено: новая Auth operation authorization ещё ожидает прямого согласия пользователя после отклонения автоматической проверкой.

## Подтверждённое состояние

[Owner verification](../uploads/docs/verification.md) разделяет два прогона:

- `dev/local/uploads-http-report.json`: **839 native HTTP checks**, Core credentials/legacy, Cards, Exams и Tests previews, полные pages, current rights/scope/privacy/receipts, **11 Rabbit jobs** и реальный restart только Uploads. `background_mode=pending`, `subject_apply_tested=false`, `browser_or_bff_tested=false`.
- `dev/local/uploads-template-http-report.json`: **284 request/semantic checks / 130 записанных HTTP**. Реальный download→inspect→fill→native preview для Core legacy XLSX, CPM JSON, OnlineTestPad JS, external-results XLSX; точные bytes/MIME, account_id/profile_id, zero/fractional results, несовместимые IDs, безопасные text int64 и durable reload. `import_commit_sent=false`, `subject_apply_tested=false`, `browser_or_bff_tested=false`, catalog/SQL mutations отсутствуют. Baseline 839 этим не переписан.

Core legacy template дополнительно прошёл **78 native checks**: `core-legacy-template-http-report.json`. Восемь типов protected templates и query fences входят в **17 Uploads операций / 296 BFF всего**. BFF `853b0eb5…` и optimized UI 535 (`5da08f64…`) приняты отдельно от owner proof. Common UI remaining acceptance прошёл 21 проверку: actual 19 checkpoint + readonly tail 2. Первые 7 прежних сценариев сохранены без повторных mutations. Реальные JSON/OnlineTestPad/native previews, external 8 columns/0/null/CAS, readonly unsafe ID, own outside-exam zero/CAS, target/MIME selection, private journals и 390px подтверждены без commit/retry/apply.

Исторический unavailable UI 7 относится к остановленному 8014 и не описывает нынешний runtime. Его `live_uploads_pass=false` нельзя ни потерять, ни превратить в новый import PASS.

Три общих QA остаются PREPARE; общего RELEASE нет. Отдельные native проверки не заменяют согласованную совместную browser-приёмку.


Explicit fixture amendment: cumulative **6 новых preview POST / 2 CAS PUT** вместо исходного MAX 5 / 2. Known-confirmed ошибочная XLSX-строка поместила huge text ID в login: session `1d91370261e0b99514e3903818cceeba`, UUID `69a582f3-64c6-4910-99f0-b676a342f224`, source SHA256 `9bbb2cc8…` сохранены без edit/replay. Corrected 8-cell preview — отдельный явно разрешённый intent/session `d34eace0a578ce6cb0c053cf680584a4` с другим key/hash. Все новые исходы confirmed, pending 0, Auth/context/browser закрыты; это не замена неизвестного исхода original `44083500…`. Student API допускает только собственные PDF/zap capability links, административные journals 403, чужой actor session и административный UI route 404.

## Исходная credential-команда: защищённый UNKNOWN

`dev/local/bff-uploads-original-recovery-http-report.json` — **5 passed recovery checks**, business_writes 0/source_resends 0/new_command_keys 0, fresh Auth session closed. Команда `44083500-b582-4c23-bd1c-67c2e9e367a7`, session `e5b5dd865773e7f45538d8f35c185d60` сохранили actor/org/div и исходную receipt binding. Старая staging стала честным `outcome_unknown`; точная native receipt дала 404, reconcile сохранил uncertainty.

Это **не доказательство неудачного импорта, отсутствия исполнения или successful preview/apply**. Исходные credential bytes/password не пересылались; новый command key не создавался. Новый независимый preview не закрывает эту команду. Browser journal verification остаётся отдельным UI шагом; неизвестный результат не разрешает auto-resend.

## Реально оставшиеся пункты

| Пункт | Состояние |
| --- | --- |
| Auth background grant | Не реализован, pending human approval. [Предложение](../../outputs/microservices/auth-background-import-approval.md), [draft](../auth/docs/import-operation-grants.md). Не объявлять готовыми код/миграции/enforcement. |
| Async apply / final reports | Каталог `commit:false`; executor `awaiting_authorization/background_authorization_pending`, progress 0/domain writes 0, report 409. HTTP 202/retry/restart проверяют transport и не обходят Auth dependency. |
| Common browser acceptance | DONE отдельно: UI 535, remaining report 21; Directions fix 46 source checks и real template/preview flow. Native 284 не прибавляется к browser count; general QA RELEASE ещё не объявлен. |
| U07 permission alias и U04 recovery | DONE: canonical results 30 API / 6 UI, Attendance native 39 ops / 5 HTTP scenarios / 925 assertions и joint recovery UI 15. Это обычные live permission/recovery проверки, отдельно от Auth background grant. |
| Core dashboard follow-up | DONE: Core f3f43d… actual READY 22:11:19.349 МСК (8 ACK, healthy/ready/OpenAPI 200); [native+BFF report](../../dev/local/core-dashboard-results-http-report.json) — 2 read-only сценария / 31 checks PASS. Source 5-case matrix / targeted Go 1.163 s / race 2.052 s / vet PASS. subject/runtime/SQL writes 0, Auth cleanup failures 0. React dashboard не использует capabilities; новое UI wiring не заявлено. Это не Uploads async apply blocker. |

## Прежние observations исправлены

Coordinator требует upload плюс canonical subject users/train/tests/test-results/exams; Cards transform требует Tests edit. Native Cards сохраняет только свои train права без обязательного общего upload. Exams preview собирается полностью до 5000 rows, с согласованными revision/state/total/fingerprint, включая после edit; source и native acceptance покрывают большие pages. Core legacy и Tests JSON/OnlineTestPad/external protected validators подключены, не заменены пустыми draft/credential flows.

Protected templates credential CSV, Core legacy XLSX, Cards, Tests JSON/OnlineTestPad/external и три Exams XLSX доступны по актуальному owner proof. Transform/Homework/Attendance имеют template:false и 400/template_not_applicable. Новые external ID columns валидирует Tests; supplied conflicting IDs не разрешают fallback по имени. Source size/row/participant/DTO limits сохраняются после скачивания шаблона.

## Route → фактический UI flow

| Surface | Использование / доказательство |
| --- | --- |
| catalog/sessions/edit/reconcile/receipt/jobs/templates/download | Общий React workspace/adapter. Native preview/template proof есть; common UI acceptance 21 после Directions/U07 fixes DONE. Async apply disabled. |
| files/register, files metadata, receipts/events | API/backend helpers, native joint Homework 57/Attendance 290. Common UI не монтирует registry/events flow; receipt GET в command recovery — отдельное использование. |
| homework.pdf / attendance.zap-images | Links в Assignments/Zaps владельца; нет второго upload/register/attachment lifecycle. |
| cards.transform | Link в native Cards→Tests draft, который работает отдельно от blocked common apply. |

Credential bytes/passwords не сохраняются в общем source/report/journal и не скачиваются. Unknown commands удерживают исходный key/scope. Source metadata/download повторно проверяют actor/org/div и owner access. Internal Auth/service keys не выставлены browser. Нагрузка, CI/CD и production работа исключены текущим поручением и не считаются незавершёнными пунктами этой задачи.

Source evidence: `services/uploads/internal/uploads/{model,http,adapters,templates}.go`, BFF `{uploads,templates}.go`, `cpm_front_new/lib/bff/uploads.ts`, `components/admin/upload/admin-uploads-bff-*`; [contracts](../uploads/docs/contracts.md), [Rabbit delivery](../uploads/docs/rabbit-delivery.md).
