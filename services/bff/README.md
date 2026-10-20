# Локальный BFF: учебные сервисы, карточки и управление Core в React UI

Этот этап подключает существующий `cpm_front_new` к локальным auth, core, homework, tests, exams, ratings и cards через Go BFF. Дизайн и экраны React сохранены. Рабочие production URL не используются в режиме BFF. Старые production-домашки не перенесены: интерфейс показывает записи нового homework в текущем подразделении. Управление организацией, подразделениями, направлениями, пользователями, участиями и школами, сводки кабинетов, список проктора и отдельные отчёты руководителя используют новый Core. Существующая параллельная интеграция attendance сохранена; её состояние описано в `../attendance/docs/implementation-plan.md`.

## Запуск

Из корня `cpm`, при уже запущенных локальных auth/core/homework/tests/calculate, file-work-service, RabbitMQ и MinIO; для экзаменов также exams на 8010 с общим Core на 8003, для рейтинга ratings на 8012, для карточек cards на 8008. Отдельный Core на 8011 не требуется: `dev/compose.exams.yaml` задаёт `EXAMS_CORE_URL=http://core:8003`. Новый отчёт Core посещаемости читает attendance на 8013; при неподключённом источнике показывает ошибку, не пустой успешный отчёт. Uploads на 8014 снова работает: native preview и Rabbit delivery проверены владельцем. Асинхронное применение импортов пока недоступно из-за отдельной Auth-зависимости; эти команды запускают только BFF/UI:

```sh
# Общий секрет подписанного контекста BFF → Tests (значение не выводится).
./dev/dev gateway-prepare
# Приватный пароль Redis: idempotent, файл mode 0600, значение не выводится.
python3 services/bff/scripts/redis_env.py
# Только BFF; внешняя сеть cpm-dev_default должна существовать.
docker compose -f dev/compose.bff.yaml up -d --build
# UI с отдельным каталогом .next-bff и фиксированными локальными адресами.
./dev/bff ui
```

Адрес: http://127.0.0.1:3001/login. Используйте именно `127.0.0.1`, не `localhost`: Origin проверяется точно. BFF слушает loopback-порт 8001, OpenAPI доступна на http://127.0.0.1:8001/openapi.json. Compose имеет отдельное имя проекта `cpm-bff` и не создаёт и не перезапускает соседние сервисы.

Альтернатива Docker для BFF: сначала `docker compose -f dev/compose.bff.yaml up -d redis`, затем `./dev/bff server` (Go в `$HOME/.local/bin/go`). Redis доступен только на loopback-порту 6379. Не запускайте одновременно два BFF на 8001. Для проверки собранного UI остановите dev UI, выполните `./dev/bff build-ui`, затем `./dev/bff start-ui`.

Сессии BFF хранятся в Redis с TTL 14 дней и отдельным persistent volume `bff-sessions`; Redis использует AOF (`appendfsync everysec`), `noeviction` и `requirepass`. Перезапуск BFF сохраняет cookie, выбранный кабинет и токены. При недоступном Redis BFF возвращает `503 session_store_unavailable`, сохраняет cookie и перестаёт быть ready; скрытого перехода в память нет. `BFF_REDIS_ADDR` по умолчанию `127.0.0.1:6379`, Compose задаёт `redis:6379`; пароль передаётся через `BFF_REDIS_PASSWORD` из игнорируемого `dev/local/bff-redis.env` (0600). Память используется только при создании `bff.New` без Redis в изолированных Go-тестах.

Обычные запросы одной сессии выполняются параллельно. Распределённые reader leases удерживаются до завершения запроса; refresh, смена кабинета и logout получают writer lease и ждут текущие операции. Лимит запроса 25 секунд, lease 45 секунд; сохранение и удаление проверяют владение lease в Redis, поэтому истёкший writer не перезапишет новый контекст. Новый запрос со старой `context_revision` получает `409`. В production Redis должен находиться в приватной сети с TLS/ACL и резервированием; локальный Compose оставляет один узел, AOF everysec допускает потерю последней секунды при аварии Redis/хоста.

Проверка сессий: `cd services/bff && go test -race ./...`. Redis TTL, две независимые BFF, перезапуск BFF, параллельные операции, writer fencing, гонки refresh/context и отказ Redis проверяются через miniredis. Дополнительно `BFF_TEST_REDIS_ADDR=127.0.0.1:6379 go test -race ./...` включает проверку Lua на настоящем Redis; она использует только свои случайные ключи и не очищает общую базу.

Флаг `NEXT_PUBLIC_CPM_BFF_DEV=1` задаётся лаунчером и нужен и при сборке, и при запуске. Он принудительно выбирает локальные API независимо от старых переменных окружения. Локальный режим не подменяет обычную production-конфигурацию. После совместной приёмки внешних тестов launcher `dev/bff` задаёт `NEXT_PUBLIC_CPM_BFF_EXTERNAL_TESTS_DEV=1` по умолчанию для `ui/build-ui/start-ui`; явное `0` сохраняется. В исходниках флаг включается только явным `1`, production legacy остаётся прежним. Публичные флаги фиксируются во время сборки UI. Сборку `.next-bff` нельзя публиковать как production-сайт.

Учётные записи стенда: `dev_admin`, `dev_student_01`, `dev_proctor_1`, `dev_supervisor`, `dev_examiner_1`, `dev_core_staff`. Пароль хранится в игнорируемом `dev/local/accounts.json`; его не следует копировать в исходники или отчёты. У `dev_core_staff` может не быть прав на домашки. Отдельная тестовая учётка сотрудника с чтением основного и редактированием резервного подразделения указана в `dev/local/bff-access-fixtures.json`.

## Что подключено

- Вход, восстановление сессии после обновления, обновление access token внутри BFF и выход.
- Верхнее меню «Кабинет … · Подразделение», группировка подразделений по организации; список доступных контекстов приходит из auth. После переключения UI заново открывает стартовый раздел, сбрасывая фильтры, выбранную работу и старые запросы во всех вкладках.
- Списки всех заданий доступной области с проходом курсорных страниц, поиском, фильтрами и существующей UI-пагинацией. Превышение защитного лимита 100 страниц отображается ошибкой, а не неполным списком.
- Администратор/сотрудник: создание, изменение, публикация, удаление заданий, аналитика и ученики; создание, переименование и архивация типов текущего подразделения. UI учитывает `view/edit`, окончательное разрешение проверяет сервис.
- Ученик: задания и результаты, PDF/сканер, загрузка, обработка, черновик, отправка и исправления. UI читает текущие лимиты source/output/pages из workspace, а принятая обработка и retry сохраняют собственные лимиты job; недостающие BFF limits не заменяются legacy defaults.
- Проктор: свой список, журнал ручных/массовых результатов, файловая очередь, принятие проверки, оценка и возврат на доработку; существующие действия повторной сдачи/редактирования оценки идут через тот же API.
- Архив, мониторинг и отчёт руководителя за период с экспортом существующим механизмом UI. Класс ученика, включая `0`, проходит в отчёт и XLSX. Мониторинг различает здоровое, unknown, stale и неподтверждённое состояние worker/storage после ошибки чтения.
- Настройки и аудит домашних работ открываются из заданий и мониторинга. Полный администратор организации изменяет семь канонических настроек выбранного подразделения и читает аудит с курсорной пагинацией; сотрудник с разрешённым просмотром заданий/мониторинга видит настройки без изменения. Неизвестный исход PUT сохраняется до проверки текущих значений GET, автоматического повторного сохранения нет.
- Тесты администратора/сотрудника: каталог подразделения, банк организации, фильтры, создание и редактирование вопросов, подключение, независимая глубокая копия, архив, редактор черновиков с блокировкой и публикацией.
- Прохождение тестов учеником: закреплённая версия и порядок, сохранение и продолжение попытки, повтор передачи после обрыва, тренировка и разрешённый разбор. Официальное завершение показывает принятие без балла; очередь и готовый результат обновляются отдельно.
- Результаты тестов: попытки «В процессе», сдачи со статусом расчёта, scoped статистика, повтор расчёта, сброс и принудительная сдача по отдельным правам, история баллов. Полный администратор управляет организационной настройкой пересчёта.

- Экзамены: классические устные и внешние, каталог подразделения/банка/организации, общие вопросы и независимые локальные правила, комиссии, назначения, льготы, подготовка, XLSX-импорты и глубокая копия.
- Проведение экзаменатором: готовность всей комиссии, устные вопросы, приватные открытые голоса, разногласия/новые раунды, замена, протокол и автоматическое завершение. Пересдача, апелляция и подтвержденное удаление истории доступны администратору.
- Ученик: только собственные опубликованные результаты, включая нулевые, история пересдачи, протокол без голосов и подготовка по живому банку с проверкой fingerprint.

- Рейтинг: запуск асинхронного расчёта через Rabbit и calculate, журнал состояний/прогресс, отмена и повтор, неизменяемые опубликованные версии и XLSX-отчёт. Во время расчёта остаётся предыдущая полная публикация; место приходит из рейтинга всего подразделения. Администратор/сотрудник учитывает `ratings.view/edit`, руководитель читает без изменения, ученик видит себя, проктор — свою группу.

- Core: управление учениками/персоналом, профиль, новые учётные данные, блокировка/активация без удаления истории, группы и назначения прокторов, школы организации и привязка учеников. Действия учитывают отдельные права текущего подразделения и полный административный доступ организации. Потерянный ответ создания требует проверки результата перед повтором; пароль не сохраняется в журнале восстановления.
- Организация: переименование своей организации, создание/переименование/архивация подразделений и направлений, каталог существующих аккаунтов и добавление участия в целевое подразделение. Полный администратор организации меняет участия и их роли; сотрудник читает доступные справочники. Отзыв участия отдельно учитывает `can_revoke`, владение профилем — `can_assign`. Последнее участие, закреплённый рейтинг и история защищаются owner. Создание организаций не добавлено.
- «Доступ и роли»: полный администратор организации создаёт, изменяет и удаляет роли через native Core API, назначает роли сотрудникам в выбранном подразделении и отзывает только их участие. Роли общие для организации, счётчик сотрудников относится к выбранному подразделению. Удаление используемой роли и последнего участия защищает owner; аккаунт и история сохраняются. Делегированный сотрудник не получает управление доступом.
- Главные страницы администратора/сотрудника и руководителя: реальные доступные счётчики Core и ссылки на подключённые разделы. Отдельный список учеников проктора ограничен его текущей группой.
- Карточки: ручные темы и карточки, каталог/фильтры, обучение и настройки, разрешённые карточки тестов, статус изменённого ответа, преобразование выбранных карточек в один черновик tests с безопасным повтором. Карточки и прогресс принадлежат организации и сохраняются между её подразделениями. Существующая экзаменационная подготовка объединяется с новым деревом cards.
- Отдельные отчёты руководителя по тестам, экзаменам и посещаемости: новый Core читает актуальные результаты владельцев за период, текущий состав учеников, фильтры и Excel. Внешние тесты в Core-отчёте приняты:67 API/BFF проверок,44 own-fixture и 10 реальных UI сценариев различают числовой 0, записанный null и отсутствие результата, включая XLSX. Нулевой результат, ожидающий расчёт и отсутствие результата различаются; классический экзамен учитывает действующую пересдачу/апелляцию, вне системы — оценку, посещаемость — реальные отметки без придуманного отсутствия.

Полные карты контрактов и проверок: [tests-integration.md](tests-integration.md), [exams-integration.md](exams-integration.md), [ratings-integration.md](ratings-integration.md), [core-cards-integration.md](core-cards-integration.md), [access-homework-uploads-integration.md](access-homework-uploads-integration.md). Текущий срез и разделение owner/joint evidence: [ui-audit-2026-10-09.md](ui-audit-2026-10-09.md). Права каталога и результатов независимы. Переключатель показывает доступные контексты auth; общие справочники/карточки остаются внутри своей организации. Внешние тесты (`type=out`) и ручные результаты подключены и прошли 15 совместных UI/BFF проверок. Это не локальное прохождение внешнего URL. Ratings использует обязательные источники экзаменов и внешних тестов; отсутствие источника не подменяется успешным пустым снимком. Узкий read-only Ratings delta прошёл 15 сценариев после отдельного Ratings UI READY; общий QA RELEASE остальных изменений этим не объявлен.

Открытые редакторы, активная экзаменационная сдача, неизвестный исход команды, проверка работы и передача файла удерживают общий Web Lock: сначала закройте редактор или завершите передачу, затем меняйте кабинет/выходите. Это действует и между вкладками. Требуется браузер с Web Locks. Read-only списки переключение не блокируют.

Неподключённые разделы явно показывают «Раздел ещё не подключён в локальном режиме». BFF не проксирует старый API и не делает fallback. Telegram исключён из Dev navigation. Общий Upload center читает live catalog и native previews Core/Cards/Tests/Exams; исполнение доставляется через Rabbit. Каталог честно содержит `commit:false`: executor сохраняет `awaiting_authorization/background_authorization_pending`, предметные записи и progress равны нулю, итоговый report недоступен. Новая Auth delegation не реализована после отклонения автоматической проверкой без прямого согласия пользователя; [конкретное предложение](../../outputs/microservices/auth-background-import-approval.md) остаётся отдельной зависимостью. Native preview/delivery не означает завершённый административный импорт. Исходная credential-команда `44083500-b582-4c23-bd1c-67c2e9e367a7` после 5 protected recovery checks сохраняет outcome_unknown без resend; это не доказательство failure или apply. Подробности и актуальные шаблоны: [uploads-contract-gaps.md](uploads-contract-gaps.md).

## Границы и контракты

| UI / BFF | Владелец |
| --- | --- |
| `/bff/auth/login`, `me`, `refresh`, `context`, `logout` | auth `/v1/auth/*` |
| `/bff/homework/*` | homework `/v1/homework/divisions/{session.division_id}/*` |
| `/bff/core/me` | core `/v1/core/me` |
| `/bff/core/users`, `memberships`, `groups`, `students`, `dashboard` | core в текущем подразделении, разрешённые операции управления |
| `/bff/core/organization`, `directions`, `divisions`, `roles`, `schools`, `organization/test-settings` | Core текущей организации; org writes только полный администратор |
| `/bff/core/organization/users`, `/organization/users/{account}/memberships` | Каталог аккаунтов и участий только своей организации, полный администратор |
| `/bff/core/divisions/{division}/users`, `/divisions/{division}/memberships/{account}` | Явная цель своей организации; owner проверяет полный admin либо текущий scope сотрудника |
| `POST /bff/core/roles`, `PUT/DELETE /bff/core/roles/{id}` | native Core роли текущей организации; только полный администратор организации |
| `/bff/core/reports/tests`, `exams`, `attendance` | core: текущий состав + актуальные результаты правильного владельца |
| `/bff/cards/*` | cards `/v1/cards/*`, scope организации и живых направлений |
| `/bff/tests/*` | tests `/v1/tests/*`, с подразделением из сессии |
| `/bff/exams/*` | exams `/v1/exams/*`, с подразделением из auth-контекста |
| `/bff/exams/capabilities` | актуальные права из auth/me |
| `/bff/ratings/*` | ratings `/v1/ratings/divisions/{session.division_id}/*` |
| `/bff/uploads/*` — 17 операций | Coordinator `/v1/uploads/divisions/{session.division_id}/*`, включая protected templates; async apply pending Auth |
| Presigned upload/download | Локальный MinIO `http://127.0.0.1:19000` |

BFF не подключён к предметным БД и не владеет бизнес-правилами. В браузер возвращаются `user`, `organizations`, `context_revision`; access/refresh остаются в серверном Redis и не передаются браузеру. Непрозрачная cookie `cpm_bff` имеет `HttpOnly`, `SameSite=Strict`, `Path=/bff`. `Secure` отключён только для этого локального HTTP-стенда. Несколько BFF используют одни сессии; перезапуск процесса сохраняет их. Dev Redis остаётся одиночным узлом.

Каждый запрос требует `X-CPM-Request: 1`; изменения — точный Origin. Кроме login/me нужен текущий `X-CPM-Context`. Старый контекст получает 409. У auth также есть монотонная версия контекста в сессии: возврат A→B→A не оживляет старый access token. Перед операцией BFF вызывает auth/me, не кеширует права. Обычные операции параллельны, обновление токенов/кабинета и выход выполняются под распределённой writer lease. Отмена учитывается и при ожидании lease. Повтора бизнес-изменений после ошибок нет.

Разрешены только перечисленные маршруты. Internal API, служебные ключи, произвольные upstream URL и параметры подмены организации/подразделения недоступны. Ответы не кешируются, ошибки не содержат внутренних URL. Таймаут upstream 22 с, операции 25 с. Актуальные лимиты `internal/bff/server.go`:

| Семейство | Тело запроса BFF | Ответ upstream |
| --- | --- | --- |
| По умолчанию, включая роли Core и настройки homework | 1 MiB JSON | 8 MiB по умолчанию; Core — 64 MiB |
| Exams | 18 MiB | 64 MiB |
| Ratings | 1 MiB JSON | 64 MiB |
| Cards | 10 MiB JSON | 64 MiB |
| Attendance | 3 MiB JSON | 8 MiB |
| Uploads | 8 MiB + 64 KiB | 16 MiB JSON; скачивание файла — до 8 MiB |

Multipart пропускается в трёх native Exams XLSX upload-маршрутах и `POST /bff/uploads/sessions`, с обязательным boundary. Native Exams ограничивает файл до 10 MiB; Uploads catalog — до 8 MiB. CAS, Idempotency-Key и X-Exam-Confirmation передаются владельцу там, где контракт их поддерживает, без автоматического повтора. Роли Core и настройки homework не получают выдуманный revision/idempotency contract: их неизвестный исход проверяется чтением. PDF передаётся напрямую локальному S3, не через лимит JSON BFF. CSP запрещает внешние подключения и внешние шрифты в этом режиме.

Адаптация старых UI DTO сосредоточена в `cpm_front_new/lib/bff/homework.ts`, `homework-settings-audit.ts`, `tests-admin.ts`, `tests-student.ts`, `exams-admin.ts`, `exams-conduct.ts`, `exams-student.ts`, `ratings-shared.ts`, `core-admin.ts`, `core-access.ts`, `core-organization.ts`, `core-reports.ts` и `cards.ts`; обычные запросы переключаются на неё только при явном dev-флаге. `uploads.ts` обслуживает common Uploads catalog/preview/journal; готовность apply определяется owner capability. `account_id` используется в homework/core/ratings, старый профильный `id` сохранён отдельно. Нулевой балл сохраняется как результат; «не сдано» больше не принимается за «сдано». Типы и группы берутся из владельцев данных, отсутствующие сведения не придумываются.

## Проверки и состояние приёмки

Проверки используют локальные public API и свои synthetic fixtures. Пароли/токены не копируются в исходники, журналы и отчёты. Скрипты с CRUD и fault/restart требуют согласованного окна; они не являются безопасным read-only smoke. Повторные широкие owner suites без новой причины не требуются.

```sh
# Source checks; не запускают контейнеры.
(cd services/bff && go test -race ./... && go vet ./...)
(cd cpm_front_new && npm test -- --run && npx tsc --noEmit)
# Read-only, с сохранёнными собственными manifests и работающим согласованным UI.
node services/bff/scripts/core_admin_browser.mjs --read-only
node services/bff/scripts/core_groups_browser.mjs --read-only
node services/bff/scripts/core_access_browser.mjs --read-only
node services/bff/scripts/core_organization_browser.mjs --read-only
node services/bff/scripts/homework_delta_browser.mjs --read-only
node services/bff/scripts/attendance_delta_browser.mjs --read-only
# Узкий read-only Ratings delta, retained owner fixtures; jobs/source writes запрещены.
node services/bff/scripts/ratings_delta_browser.mjs
```

Исходный Card/Core/users/dashboard/reports cutover завершён; Access, organization/divisions/directions/memberships, Homework PDF/settings/report и узкий zap delta имеют отдельные совместные доказательства. Uploads owner сообщил 839 native preview/delivery проверок, 11 Rabbit jobs и успешный собственный restart; отдельно templates 284 checks / 130 HTTP после download→native preview; его report явно содержит `subject_apply_tested:false`, `browser_or_bff_tested:false`. Эти числа не являются общим UI import PASS. Старые unavailable-state 7 checks и исходный aggregate `dev/local/bff-ui-cutover-final-report.json` остаются историческими отчётами, не описанием нынешнего живого Uploads.

Предыдущая optimized UI-сборка 509 имеет проверки Cards 15, Tests 7, Ratings 15, Attendance scan 4 (три реальных XLSX) и Core external report 10. Native Core legacy template 78 и Uploads templates 284 checks/130 HTTP приняты отдельно; это не common browser/full apply PASS. Core external report дополнительно проверен 67 API/BFF assertions и 44 проверками подготовки собственного набора: 11 confirmed commands / 0 pending.

Подтверждённый runtime по `bff-final-runtime-proof.json`: Core `f3f43d…`, Attendance `b8f807f40b73…` / 39 native операций, BFF `853b0eb5…` / 296 публичных операций / 17 Uploads. Optimized UI 535 (`5da08f64…`) READY с 21:37:04 МСК; frontend suite 535 tests / 57 files, TSC/scoped lint/build PASS. BFF full race 2.935 s / vet PASS. U04 native 5 HTTP сценариев / 925 assertions и joint UI 15 приняты отдельно; U07 canonical result permissions — 30 API / 6 UI. Common Uploads remaining acceptance — 21 проверки: actual checkpoint 19 + readonly tail 2; cumulative 6 новых previews / 2 CAS PUT, source/domain apply 0, pending 0, все Auth/context/browser закрыты.

Core dashboard `f3f43d337a2ef4ba00c08cda3d85674f64c1af74cad752ad145fb09e8db9202d` actual READY после Core-only окна 22:11–22:13 МСК с 8 ACK: started `2026-10-09T19:11:19.349574138Z` (22:11:19.349 МСК), healthy/ready/OpenAPI 200. Source: 5-case permission matrix, targeted Go 1.163 s / full race 2.052 s / vet PASS. [core-dashboard-results-http-report.json](../../dev/local/core-dashboard-results-http-report.json) — 2 read-only native+BFF сценария / 31 checks PASS: один actor 548 в A10254 получает `test_results` и report 200, в B10260 — `tests`, canonical result view false и report 403. Все четыре private totals остаются null; native/BFF DTO совпадают, subject/runtime/SQL writes 0, Auth cleanup failures 0. Текущий React dashboard читает totals, но не использует capabilities; новое UI wiring не заявлено. Три общих QA остаются PREPARE; общий RELEASE не объявлен. Auth background grant/full import apply остаются отдельной нерешённой зависимостью.

U04 native runtime принят: `attendance-command-receipts-http-report.json` содержит 5 actual HTTP сценариев / 925 assertions. Проверены точные receipt reads, удалённый собственный день, архивированный тип, минимальная запись ученика и границы кабинета/организации. Старый bulk, повреждённый audit, late commit, QR expiry/rotation и исторический перенос группы проктора покрыты только Go/SQL, не native HTTP/UI. Source: 9 новых групп Go/SQL checks, race 3.926 s / vet PASS; предыдущий BFF full race 1.943 s / vet PASS. Новый BFF 296 уже развёрнут; full race 2.935 s / vet PASS и actual UI recovery 15 подтверждены отдельно. Позднейшие totals не подменяют исторические 141/191/217/273/361.

Свежий Exams CAS сценарий завершён: `bff-exams-delta-fixture-browser-report.json` — 7 PASS. После исходного известного 403 выполнена одна новая явно разрешённая PUT своего preview row, 200 / revision 2→3; это не замена unknown-команды. Read-only recovery загрузил revision 3: все 5000 rows через 25 pinned pages. Import commit и запись 5000 предметных вопросов не выполнялись; браузеры/Auth закрыты.

Full ESLint имеет прежний незелёный baseline; target lint/TSC не означают full clean lint. Core 005 limitation в `integrated-verification.json` сохраняется: прежний updated_at четырёх synthetic школ был изменён, сохранность timestamps не доказана, guessed restoration не выполнялся. Нагрузка, CI/CD и production исключены текущим поручением и не относятся к незавершённым пунктам этой задачи.

Common Uploads UI прошёл `bff-uploads-delta-independent-remaining-report.json`: 21 проверки после Directions/U07 fixes на UI 535. Retained run `6078af3e`, editor 536, div 10249/10250; первые 7 прежних сценариев не повторяли. Реальные protected JSON/OnlineTestPad downloads дошли до native preview без JS execution; external XLSX сохранил 8 columns, account IDs, 0/null и CAS; unsafe ID остался readonly; собственный outside exam сохранил 0 points/grade. Template targets/MIME, actor/viewer privacy и 390px приняты. По явному расширению бюджета создано 6 previews вместо исходных 5: ошибочная XLSX-строка с ID в колонке логина имела известный подтверждённый исход и сохранена; исправленный 8-cell preview — отдельный явно разрешённый intent/key/hash. Это не unknown replacement и не apply. Student catalog содержит только own PDF/zap links с false import capabilities; journals 403, foreign actor session и административный UI route 404.

Attendance: optional distinct `BFF_ATTENDANCE_CONTEXT_KEY` shared only with Attendance enables reuse of the current request's live Auth result, including target-division grants/group. No cross-request permission cache; method/URI/body/audience-bound HMAC, 30-second validity. See Attendance `docs/journal-performance.md`.
