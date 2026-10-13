# CPM Cards на Go

Карточки работают на `http://127.0.0.1:8008`, контракт — `/openapi.json`. Сервис владеет ручными темами/карточками, прогрессом, настройками изучения, Excel-предпросмотрами и сессиями преобразования. Тесты и их вопросы получает по HTTP из tests. Экзаменационная подготовка остаётся в домене exams.

## Организация, направления и права

Тема имеет `organization_id` и `direction_id`; подразделения у неё нет. Разделы и карточки одинаковы во всех подразделениях одной организации. Ключ прогресса — `(account_id, organization_id, card_ref)`, настройки — `(account_id, organization_id, section_kind, section_ref)`. Auth account ID не заменяется прежним ID профиля. Смена подразделения сохраняет прогресс; другая организация имеет отдельные каталог и настройки.

Каждый бизнес-запрос обращается к живому auth introspect, затем к core за доступными направлениями. Пользователь не задаёт account/organization в теле; неизвестные поля отклоняются. Ученики меняют только собственный прогресс. Полный admin ограничен своей организацией. Staff требует явного `cards`/`training`/`train` view/edit; `train` — действующий ключ текущих auth/core. Одного членства в подразделении недостаточно. Сотрудник использует разрешения выбранного контекста auth, а не суммарные права всех своих подразделений.

Текущий core предоставляет все активные направления организации её участникам: индивидуальной модели назначения направлений в существующем core нет. Cards использует только список, возвращённый core, и не придумывает собственный справочник/гранты. Архивирование направления закрывает чтение и изменение его карточек. Если core получит индивидуальные назначения, ограничение должно применяться в `cards-context`; потребитель уже проверяет каждый direction ID по этому ответу.

## Контракты между сервисами

Добавлен `GET /internal/core/cards-context`: `X-Service-Key` + действующий Bearer. Ответ `{status:true,data:{organization_id,directions:[{id,name}]}}`. Endpoint проверяет текущую сессию и роль, возвращает только активные направления выбранной организации. Ни organisation/account ID, ни division ID извне не принимает.

В tests добавлен файл `internal/tests/cards_contract.go` и регистрация маршрутов:

| Метод | Результат и проверка |
|---|---|
| `GET /internal/tests/cards/catalog` | Метаданные доступных для тренировки тестов, версия; вопросы отсутствуют |
| `GET /internal/tests/cards/catalog?admin=true` | Для разрешённого сотрудника опубликованные тесты, счётчик и visible, включая скрытые ответы; самих ответов нет |
| `GET /internal/tests/cards/tests/{test}` | Test ID, direction ID, title, content version, разрешённые вопросы всех трёх типов |
| `POST /internal/tests/cards/drafts` | Строгий DraftDTO, проверка tests.edit и направления, Idempotency-Key, создание штатного черновика tests |

Все четыре требуют и сервисный ключ, и пользовательскую сессию, проверяемую через auth. Ключ берётся из существующего `AUTH_INTERNAL_KEY`; это отдельная от Bearer сервисная аутентификация, ключ не возвращается клиенту. Tests сам решает доступ к ответам по своему `practiceEligible`: активное направление, неархивный тест, опубликованное подключение с `answers_visible`, принятая официальная сдача пользователя в этом подразделении либо закончившееся окно теста. Клиент не передаёт произвольный division ID. Для ученика проверяется объединение подтверждённых auth подразделений выбранной организации; одного разрешённого подключения достаточно. Staff проверяется только в выбранном контексте его действующих разрешений. Admin также не обходит скрытые ответы через endpoint проекции.

**Явное исправление доступа:** старый `test_allows_training` проверял только published/visible, не сегодняшние подразделения/сдачу/время. Новый endpoint использует действующую политику tests, поэтому не выдаёт ответы до сдачи/окончания окна. Скрытые тесты можно видеть в административном каталоге только как метаданные. Даты и права тестов остаются собственностью tests, не становятся настройками карточек.

Кеширования содержания и копии теста в cards нет. Каждый запрос видит текущую разрешённую версию. Устойчивая ссылка — `test:{test_id}:{question_id}`. Изменения вопроса/представленного ответа меняют fingerprint; изменение веса/метаданных теста само по себе прогресс не сбрасывает. Удалённые вопросы исчезают из выдачи и знаменателя прогресса; их старые отметки остаются невидимыми, как в AS IS. Tests не переиспользует удалённые question ID. Между HTTP-снимком tests и записью MySQL нет распределённой транзакции: если содержимое поменялось в этот момент, сохранённый старый fingerprint на следующем чтении даст `answer_changed`.

## Соответствие AS IS → Go → проверки

Исходники: `cpm-back-main/cpm_back/blueprints/{cards_bp,card_import_bp,card_transform_bp}.py`, `services/cards/{training_cards,training_projection,training_progress,exam_training}.py`, `services/card_import/*`, `services/card_transform/*`. Сверены фронтовые `lib/training/*-api.ts` и тесты многострочного импорта. Префикс новых методов — `/v1/cards`; ответы `{status:true,data:...}` либо `{status:false,error:...}`. Dev React UI подключён через BFF для каталога, CRUD тем и карточек, обучения, прогресса и преобразования в черновик теста. Общий Excel-импорт подключается через uploads с сохранением owner preview/commit; см. `../bff/core-cards-integration.md` и `../bff/uploads-contract-gaps.md`.

| Старый метод | Новый метод | Проверка |
|---|---|---|
| `get-training-tree/{student}` | `GET /tree` | Организация, направления, объединение разрешённых test-подключений, чужие данные |
| `training-sections/{student}/{direction}` | `GET /directions/{direction}/sections` | Search, kind=all/manual/test, progress=all/in_progress/learned, page/limit |
| `section-study/{student}/{kind}/{ref}` | `GET /sections/{kind}/{ref}` | Карточки, stats, batches, settings, версия tests |
| `section-batch/.../{index}` | `GET /sections/{kind}/{ref}/batches/{index}` | Сначала постоянный батч, потом all/unlearned/learned/stale |
| `section-study-settings/...` | `PUT /sections/{kind}/{ref}/settings` | 10/20/30, режим и последний индекс, общий прогресс подразделений |
| `mark-card-learned` | `POST /sections/{kind}/{ref}/learned` | Своя существующая разрешённая карточка, актуальный fingerprint, повтор |
| `DELETE mark-card-learned/...` | `DELETE /sections/{kind}/{ref}/learned` | Собственная отметка, повтор безопасен |
| `get-admin-training-catalog` | `GET /admin/catalog` | Метаданные, количество и видимость, без скрытых ключей |
| `create-training-theme`, `create-theme-with-questions` | `POST /themes` | Уникальное название в направлении, атомарное создание questions |
| `training-theme/{id}` | `PUT/DELETE /themes/{id}` | Организация, направление, expected_revision, удаление дочерних карточек/прогресса |
| `admin-cards-by-theme/{id}` | `GET /themes/{id}/cards` | Порядок sort_order/id, права просмотра |
| `create-card`, `card/{id}` | `POST /themes/{id}/cards`, `PUT/DELETE /themes/{id}/cards/{card}` | Валидация, авто-порядок, конфликт версий и гонка редакторов |
| `card-import/parse` | `POST /imports/parse` | Excel, активный лист, заголовки Вопрос/Ответ, пустые строки, многострочные ячейки |
| `card-import/sessions/{id}` | `GET/PUT /sessions/{id}` | Владелец, организация, 72 часа, исправления и skip, предупреждения о дубликатах |
| `card-import/sessions/{id}/commit` | `POST /sessions/{id}/commit` | Одна SQL-транзакция, ошибки не дают частичного импорта, конкурентные повторы |
| `card-transform/sessions` | `POST /transforms` | Карточки одной темы, заданный порядок, полный ответ одним text alias |
| `card-transform/sessions/{id}/commit` | `POST /sessions/{id}/commit` | Реальный черновик tests, вес 1, сетка 3 колонки, повтор возвращает тот же draft ID |
| Exam-training | Отдельный домен exams | kind=exam отвергается; части, билеты и training_enabled в cards не переносились |

Fingerprint совместим с Python `sha256(json.dumps({q:trim(question),a:trim(answer)},ensure_ascii=False,sort_keys=True))`, включая пробелы JSON и Unicode. Формат ответов: single — один правильный текст; multiple — `; `; text — ` / `. Внутренние переносы строк не уплощаются. `unlearned` включает `answer_changed`; процент учитывает только learned и использует прежнее округление ties-to-even. Батчи индексируются с нуля, границы не сдвигаются от фильтра.

## DTO и ограничения

Создание темы: `{name,direction_id,questions?:[{question,answer,sort_order?}]}`. PUT темы — полное новое name/direction_id плюс `expected_revision`. POST карточки — question/answer и необязательный sort_order; PUT добавляет `expected_revision`. DELETE темы/карточки принимает `{expected_revision}`. Карточка возвращает `revision`. Редакционные PUT заменяют поля явным DTO, это не прежний нестрогий частичный update.

POST/PUT/DELETE тем и карточек, POST transforms, parse, PUT preview и commit поддерживают `Idempotency-Key`: UUID либо ASCII `[A-Za-z0-9:_-]` длиной 8–128. `GET /v1/cards/receipts/{key}` возвращает первоначальный результат команды и request hash после повторной проверки текущего доступа. Клиент сохраняет ключ и исходный DTO до подтверждённого ответа. Запись результата команды и изменение данных выполняются в одной SQL-транзакции: повтор после потери ответа возвращает первоначальный результат, включая ID и ревизию; изменённый DTO или другая операция с тем же ключом дают `409 idempotency_conflict`. Ключ изолирован по account/organization; transform также фиксирует выбранное подразделение. Текущие права и доступность направления проверяются при повторе. Приватные записи команд используют существующую таблицу сессий, не доступны через публичные session routes и не требуют миграции. Поле `revision` ручных тем присутствует в административном каталоге для CAS.

Learned/unlearned: `{card_ref,content_fingerprint}`; для DELETE fingerprint необязателен. Настройки: `{batch_size,study_mode,last_batch_index}`; last_batch_index может быть null. Ссылка ручного раздела канонизируется, ведущие нули не создают дополнительные настройки. Темы без карточек существуют, но не считаются выученными.

Excel: multipart `file,direction_id` и либо `theme_id`, либо `create_new_theme=true,new_theme_name`. XLSX, до 10 MiB запроса, 32 MiB распакованного архива, 1000 строк; отсутствующие ответы остаются исправимыми ошибками preview. Совпадения вопроса/ответа с нормализацией пробелов — warnings, не автоматический skip. При совпадении названия новой темы импорт добавляет карточки в существующую. PUT preview: `{expected_revision,cards:[{row,question,answer,skip}]}` — все исходные строки, без подмены target. Commit: `{expected_revision}`. Ревизия commit остаётся прежней для повторов; повтор возвращает прежний результат, не создаёт новые строки. Состояние `completed` доступно для повторного чтения и после 72 часов; незавершённое preview истекает.

Transform: `{theme_id,card_ids:[...]}`; до 200 уникальных карточек. Сессия фиксирует выбранный снимок и подразделение назначения tests из текущего auth; для чтения, replay/receipt и commit нужен тот же контекст и tests.edit; tests.edit также проверяется при создании preview. Ни тело, ни query не могут сменить подразделение. Черновик не публикуется автоматически. Сбой после успеха tests и до commit MySQL восстанавливается повтором с тем же `cards_{session_id}`. Неизменяемый снимок и ключ гарантируют тот же draft ID. Ответ >1000 символов отклоняется на preview из-за действующего лимита text alias tests; он не обрезается и не разбивается молча.

Ресурсные ограничения: имя темы 200 символов, вопрос 4000, ручной ответ 16000; сортировка 0..1e9; до 1000 тем-вопросов в одном создании, 200 вопросов в преобразовании. Ограничения и строгая валидация устраняют прежние неограниченные/невалидные входы. DELETE learned стал идемпотентным. Произвольный fingerprint или ссылка больше не принимаются. Собственного rate limit нет.

## Хранение, запуск и проверки

Миграция `migrations/001_cards.sql` добавляет пять таблиц `cards_*_v1`. Старые 79 таблиц и Mongo-коллекции не меняются. FK связаны с organization/account и собственными темами; принадлежность направления проверяется через API core, а не чтением его таблиц из cards. Все SQL читаются с organization ID. Миграция идемпотентна; allowlist расширена в `dev/schema/extensions.json`.

```sh
./dev/cards prepare
./dev/cards integration-up
./dev/.venv/bin/python services/cards/scripts/seed.py
./dev/cards test
./dev/cards status
```

`integration-up` адресно пересобирает core/tests/cards с `--no-deps`, чтобы применить добавленные API. Auth/MySQL/Mongo уже должны работать. Скрипт не запускает/останавливает RabbitMQ, calculate, homework или общий контур. Обычный `./dev/cards up` пересобирает только cards. Отдельный профиль и loopback-порт заданы в `dev/compose.cards.yaml`. При недоступном реестре, если образы уже имеются, `dev/.venv/bin/python services/cards/scripts/offline_build.py` перекомпилирует Go под архитектуру контейнера и заменяет бинарники на существующих runtime-образах; затем `./dev/cards start` поднимает готовые образы без `--build`; объединённая команда — `./dev/cards offline-up`. Основной Dockerfile остаётся полноценной многоэтапной сборкой.

Конфигурация: обязательные MYSQL_HOST/PORT/DATABASE/USER/PASSWORD, CARDS_AUTH_URL, CARDS_CORE_URL, CARDS_TESTS_URL, AUTH_INTERNAL_KEY (32+ байта), необязательный CARDS_LISTEN_ADDR. Значений production по умолчанию нет. HTTP-клиенты запрещают redirects, имеют timeout 8 секунд; общий запрос ограничен 30 секундами и отменяет SQL/HTTP. Ошибки источника дают 503, а не пустой успешный каталог. `/health/live` проверяет процесс, `/health/ready` — БД и готовность auth/core/tests. Контейнер non-root/read-only, cap_drop ALL; tmpfs ограничен 64 MiB.

Seed проверяет `cpm_dev`, loopback и фиксированные dev-порты, сохраняет существующие данные. Добавляет отдельные случайные credentials и пользователей cards, использует две существующие синтетические организации и несколько подразделений, создаёт направления через core, темы/карточки/прогресс/preview через cards, три типа вопросов и скрытый тест через tests. Дополнительное участие специального синтетического ученика в другой организации добавляется только dev SQL: публичного cross-organization enrollment в core пока нет. Никакие реальные персональные данные не используются. Секреты только в игнорируемом `dev/local/cards-fixtures.json` с mode 0600.

```sh
cd services/cards
~/.local/bin/go test -race ./...
~/.local/bin/go vet ./...
```

`http_test.py` проверяет реальные четыре сервиса и сохраняет `dev/local/cards-http-report.json` без credentials. Для outage запускается временный **отдельный** контейнер того же cards-образа на loopback 18008 с недоступным tests-портом; общий tests не останавливается. Это реальная проверка отказа HTTP-зависимости, не fixture её успешного контракта. Основные сценарии проекции и черновика используют настоящий dev tests.

Оставшиеся границы: перенос прежних production-данных/прогресса не выполнялся; экзаменационный домен не переносился. Индивидуальные назначения направлений отсутствуют в текущем core. Списки сейчас вычисляют прогресс по разделам с несколькими HTTP/SQL-вызовами; для большого каталога нужна отдельная пакетная проекция tests. У tests-contract защитный предел 1000 кандидатов возвращает явную ошибку, не усекает выдачу. Распределённых транзакций между cards и tests нет; преобразование использует штатную идемпотентность tests.

## Выполненная проверка 7 октября 2026

- Docker `cpm-dev-cards-1`: healthy, `127.0.0.1:8008`; сервисы auth/core/tests использовались реально.
- `http_test.py`: **184 проверки**, результат passed. Включены SQL-репетиция восстановления после удалённого успеха преобразования, истёкший preview, немедленное применение отозванных прав, архивирование направления, конкурентные записи и isolated Docker outage.
- `go test -race ./...` и `go vet ./...` прошли для cards и изменённых core/tests. Проверки бизнес-правил cards включают fingerprint, три типа ответа, округление, границы батчей/режимы, многострочные дубликаты, права, строгие DTO и отказ зависимостей/redirects.
- Повторный seed прошёл без дублирования его аккаунтов/каталогов.
- `dev/scripts/verify.py`: passed; DDL **79 исходных таблиц**, настройки/индексы **9 исходных коллекций** и пароли **20 начальных dev-аккаунтов** сохранены.
- Отчёты: `dev/local/cards-http-report.json` и `dev/local/verification.json`; credentials в них отсутствуют. Docker registry дважды не ответил на TLS handshake, поэтому последняя сборка сделана документированным offline-путём на уже имеющихся runtime-образах.

## Native Uploads contract, 9 октября 2026

Добавлены защищённые `/v1/cards/imports/contract`, `/imports/template`, `/imports/targets`, `/sessions/{session}/source` (metadata, без хранения байтов) и `/receipts/{key}`. XLSX parse, исправление preview и commit восстанавливаются с тем же ключом после потери ответа. Справочник ручных целей импорта не зависит от Tests. Квитанция edit сохраняет результат конкретной команды, даже если после неё прошла другая правка.

Точный контракт и алгоритм восстановления: [docs/uploads-owner-contract-2026-10-09.md](docs/uploads-owner-contract-2026-10-09.md). Общий Uploads дополнительно проверяет `upload`; native Cards сохраняет `train` для совместимости прямого UI. Прямое согласие на Auth worker grant получено в human message `01a12221-7d06-73f3-8f27-8c05f352977a`; реализация идёт. [Текущий Cards enforcement статус](docs/background-import-enforcement.md). Auth/Core adapter реализован по frozen revision 2; source proof/carrier/strict JSON/permission/receipt guards unit/race/vet passed. Cards runtime обновлён в W2 10 октября 00:17 МСК: image `727b467a…`, readiness/OpenAPI 1.2.0 (25 операций) проверены; остальные контейнеры не менялись. Адресный real grant HTTP harness и совместная приёмка очереди ещё предстоят. Готовность native API не означает готовность всей очереди Uploads. Revoke-проверки теперь создают отдельные role/account для каждого запуска, чтобы не менять права shared fixtures.

Проверка 9 октября: основная HTTP-приёмка **187 passed**, новый `uploads_contract_test.py` **227 passed**, Cards `go test -race ./...` и `go vet ./...` passed. Использованы живые dev Auth/Core/Tests/MySQL и изолированный outage-контейнер; остальные сервисы не перезапускались. В рамках этой проверки Uploads8014 не запускался. Разработка Uploads продолжается по более позднему прямому поручению пользователя; прежняя остановка отменена. Согласие на новую фоновую авторизацию получено; её совместная приёмка ожидает завершения exact контракта и реализации, handoff владельцев и согласованного окна запуска.
