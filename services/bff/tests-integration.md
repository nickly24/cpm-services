# Интеграция тестов через BFF

Карта требований составлена по действующему коду `services/tests`, `services/calculate`, HTTP-контракту tests и существующим экранам `cpm_front_new`. Это перечень поведения, которое должна сохранять интеграция, и проверок для локальных Dev Mongo, Dev RabbitMQ и Dev UI. Само наличие строки в матрице не подтверждает выполнение её браузерной проверки: фактические прогоны записываются отдельно в конце документа и в `dev/local`.

## Модель и область данных

Тест — один общий документ организации. У него одно подразделение-владелец, одно направление из core, общее содержимое и неизменяемые версии. Подключение к другому подразделению сохраняет ID теста; настройки подключения самостоятельные. Независимая копия создаёт новый тест, владельца и версию вопросов. Организацию меняют через auth-контекст; подключить или скопировать документ между организациями нельзя.

Попытка закрепляет `organization_id`, `division_id`, `account_id`, `test_id`, версию вопросов, порядок, настройки и сроки при старте. `account_id` означает аккаунт auth; старый `user.id`/профиль core не заменяет его. Сдачи, статистика, попытки и списки текущего подразделения фильтруются до подсчёта `total` и пагинации.

| Пользователь | Каталог/банк | Общее содержимое | Настройки подключения | Чужие сдачи |
|---|---|---|---|---|
| Полный admin организации | Все тесты своей организации | Редактирование любого владельца | Доступные подразделения организации | Чтение, сброс, принудительная сдача, повтор расчёта |
| staff_admin `tests.view` | Текущие подключения и открытый банк | Чтение доступного теста | Чтение текущего подразделения | Требуется отдельно `test_results.view` или `test-results.view` |
| staff_admin `tests.edit` | Создание, подключение, независимая копия | Только владелец выбранного подразделения | Редактирование выбранного подразделения | Изменения требуют отдельно `test_results.edit`/`test-results.edit` |
| student | Опубликованные тесты выбранного подразделения | Вопросы через собственную попытку без ключей | Только доступность | Собственные сдачи; разбор при разрешении |
| proctor/examiner/supervisor | Отдельного общего допуска tests нет | Нет | Нет | Нет; роли не превращаются в администратора |

У роли с правом результатов без права каталога допустим список сдач, но недопустим обязательный запрос полного содержимого или карточки каталога для каждой строки результатов. Метаданные имени теста должны приходить из разрешённой проекции либо отображаться как ID. Права проверяются сервисом на каждом вызове через живой auth introspect; кнопки `actions` удобны для UI, но не заменяют проверку API.

## Публичные маршруты владельца

Все пути таблицы начинаются с `/v1/tests`. BFF публикует только разрешённые публичные методы и фиксирует контекст выбранного кабинета. `/internal/tests` и `/internal/calculate` браузеру не доступны. Ошибки владельца сохраняют машинный код; `409` не запускает автоматический повтор изменяющей операции.

| Метод / путь | Назначение и обязательный контракт |
|---|---|
| `GET /bank` | Банк своей организации; контекст действий — выбранное подразделение |
| `GET /divisions/{d}/tests` | Каталог подключения; студент видит опубликованные |
| `POST /divisions/{d}/tests` | Создать скрытый тест; `Idempotency-Key`, `title`, `description`, `direction_id`, `questions` |
| `GET /{test}` | Реквизиты, `revision`, `content_version_id`, подключения, происхождение и разрешённые действия |
| `PUT /{test}` | Общие реквизиты; `expected_revision`; отсутствие `questions` сохраняет прежние вопросы |
| `GET /{test}/content` | Полный редактор, `max_question_id`, `used_option_ids` и ключи; только доступ каталога |
| `PUT /{test}/content` | Полный снимок редактора; одна версия и один пакет истории; `expected_revision` |
| `GET /{test}/history` | Только подтверждённая история версий; `limit`, `offset` |
| `PUT /{test}/bank` | `{expected_revision, available}`; общая видимость в банке |
| `PUT /{test}/archive` | `{expected_revision, is_archived}`; архив/восстановление общего теста |
| `POST /divisions/{d}/tests/{test}/attach` | Общий ID и новые скрытые настройки; `{expected_revision}` |
| `POST /divisions/{d}/tests/{test}/copy` | Независимая глубокая копия; `{expected_revision,title}`, `Idempotency-Key` |
| `PUT /divisions/{d}/tests/{test}/settings` | Полная замена настроек текущего подключения; `expected_revision` |
| `DELETE /divisions/{d}/tests/{test}` | Снять подключение; `{expected_revision}`; владельца снять нельзя |
| `GET /divisions/{d}/tests/{test}/access` | Доступность официального прохождения/тренировки/разбора и ID текущей попытки/сдачи |
| `GET /divisions/{d}/drafts` | Список черновиков; `state`, `limit`, `offset` |
| `POST /divisions/{d}/drafts` | Новый черновик; `Idempotency-Key`; незавершённые вопросы допускаются |
| `GET /divisions/{d}/drafts/{draft}` | Данные, координаты и состояние блокировки |
| `PUT /divisions/{d}/drafts/{draft}` | Полный снимок и `expected_revision`; собственная живая блокировка |
| `DELETE /divisions/{d}/drafts/{draft}` | Архив черновика; `expected_revision` |
| `POST /divisions/{d}/tests/{test}/draft` | Новый независимый черновик из теста; `{}`, `Idempotency-Key` |
| `POST /divisions/{d}/drafts/{draft}/lock` | Получить/продлить блокировку 90 секунд; `expected_revision`; новая ревизия |
| `DELETE /divisions/{d}/drafts/{draft}/lock` | Снять собственную блокировку; `expected_revision` |
| `POST /divisions/{d}/drafts/{draft}/publish` | Новый самостоятельный скрытый тест; повтор по исходной ревизии возвращает тот же тест |
| `POST /divisions/{d}/tests/{test}/attempts` | Старт/продолжение; `{practice:false}` или `{practice:true}` |
| `GET /divisions/{d}/attempts` | Попытки; `test_id`, административный `account_id`, `practice`, пагинация |
| `GET /attempts/{attempt}` | Закреплённые вопросы без ключей, ответы, сроки и `server_time` |
| `PATCH /attempts/{attempt}/answer` | Один фиксируемый `{question_id,value}` |
| `PATCH /attempts/{attempt}/answers` | Пакет фиксируемых ответов |
| `POST /attempts/{attempt}/commits` | До 25 офлайн-коммитов; подтверждённые ID и конфликт повтора |
| `POST /attempts/{attempt}/finalize` | Финальный авторитетный снимок; `202 accepted`, постановка в очередь |
| `POST /attempts/{attempt}/submit` | Сдача из серверных ответов; официальная `202`, тренировка `200` |
| `POST /attempts/{attempt}/force-submit` | Административная сдача сохранённых ответов; `test_results.edit` |
| `POST /attempts/{attempt}/practice-answer` | Немедленная обратная связь через calculate; только тренировка |
| `DELETE /attempts/{attempt}` | Административный сброс; `expected_revision`; история не перезаписывается |
| `GET /divisions/{d}/submissions` | Сдачи; `test_id`, административный `account_id`, `calculation_status`, пагинация |
| `GET /divisions/{d}/submissions/stats` | Те же фильтры, что у списка; принятые, готовые, ожидающие, ошибки и средний балл |
| `GET /submissions/{submission}` | Текущий статус расчёта; `result:null` до готовности |
| `GET /submissions/{submission}/review` | Готовый разбор; до готовности `409 calculation_pending`, без допуска `403` |
| `GET /submissions/{submission}/calculations` | Подтверждённая история баллов и ревизий; `test_results.view` |
| `POST /submissions/{submission}/retry-calculation` | Новая ревизия незавершённого расчёта; `expected_revision`, `test_results.edit` |

Направления и организационная настройка принадлежат core: `/v1/core/directions` и `GET/PUT /v1/core/organization/test-settings`. Настройку `{expected_revision,recalculate_on_test_change}` меняет полный администратор организации. Банк вопросов отдельным владельцем не является: вопросы берутся из доступного каталога/содержимого тестов, без создания нового параллельного хранилища.

Каталог поддерживает пересекающиеся `q`, `direction_id`, `owner_division_id`, `is_archived`, `relation=all|owned|attached|copy`. `limit` — 1–100, `offset` — 0–100000. Поиск применяют перед пагинацией. В старом интерфейсе `page` переводится в offset; клиентские фильтры требуют полного набора до пагинации, а не одной страницы. BFF не обрезает список без сообщения.

В браузере пути плоские: `/bff/tests/catalog`, `/bank`, `/{test}`, `/{test}/content|history|bank|archive|settings|attach|copy|access|attempts|draft`, `DELETE /{test}/attachment`, `/drafts` и `/drafts/{draft}/lock|publish`, `/attempts` и `/attempts/{attempt}/…`, `/submissions`, `/submissions/stats`, `/submissions/{submission}/review|calculations|retry-calculation`. Подразделение берётся только из текущей BFF-сессии. Даже полному администратору глобальный ID попытки/сдачи другого выбранного подразделения не открывает обход текущего контекста.

## Несовпадения прежнего UI и нового контракта

| Прежний UI | Действующее поведение | Требуемое изменение |
|---|---|---|
| `/create-test`, `/test/{id}`, тест по имени направления | CRUD владельца и числовой `direction_id` | Адаптация транспорта/DTO и явный выбор текущего подразделения |
| `published`, `visible`, даты и время как общие поля | `placements[].settings` | Менять настройки только текущего подключения; `visible` соответствует `answers_visible` |
| Удаление теста вместе со сессиями | Архив общего теста или снятие чужого подключения | Раздельные действия и понятные названия; сохранение старых сдач |
| Один список своего подразделения | Каталог подразделения и банк организации | Переключение каталога/банка, фильтры владельца и relation |
| Копирование как обычное создание | `attach` общего документа и `copy` независимого | Явно различать действия; копия начинается скрытой без переноса публикации |
| Плоские legacy вопросы, `single-choice`/`multiple-choice`/`text-answer` | `question_id`, `single`/`multiple`/`text`, `answers[].id`, `correct_answers` | Двусторонняя адаптация типов, сохранение ID и правильных ответов |
| Новым вопросам повторно назначаются ID из индекса | Удалённые ID вопросов/вариантов не переиспользуются | Использовать `max_question_id`, `used_option_ids`; reorder сохраняет идентичность |
| Сохранение без ревизии | CAS `expected_revision` | Прочитать текущую ревизию; конфликт сохраняет локальную форму для обновления |
| POST без ключа повторяемости | Idempotency-Key 16–128 символов | Один устойчивый ключ на операцию; сетевой повтор с тем же телом |
| Черновик lock `{force}`, unlock отдельным POST | Ревизия, lock POST/DELETE, 90 секунд | Продление, новая ревизия после lock, другой редактор `409` без силового захвата |
| `/test-attempt/start`, camelCase snapshot wrapper | Прямой snake_case финальный снимок | Перевести версию, даты, ответы и reason; необязательный `snapshot_hash` старого клиента не пересылать: владелец вычисляет семантический хеш |
| Завершение сразу показывает `score` | `202 accepted`, `queued`, `result:null` | Экран «Ответы приняты / Проверка ответов», polling сдачи, отдельная готовность |
| `score || 0` | Готовый 0 и отсутствие результата различаются | Pending показывает статус, готовый ноль показывает 0 |
| Отдельный progress теста | Проверка относится к сдаче | Статус в строке попытки/сдачи, независим для каждого ученика и подразделения |
| Синхронный recalc `updated/sessions` | Событие истории и асинхронные задания | Сохранение сообщает о поставленном пересчёте; история и готовность читаются отдельно |
| `studentId` core профиля | `account_id` auth | Использовать явное соответствие из core |
| Мониторинг может грузить полный тест для результатов | Право results отдельно от tests | Не расширять доступ и не ломать results-only роль |
| Контекст сохранённой попытки не включал разделение | Попытка закрепляет org/division/account/version | Ключ локального хранилища и восстановление учитывают контекст; чужие данные не подмешиваются |

## Матрица требований и приёмки

| ID | Требование | Проверка на dev |
|---|---|---|
| S01 | Организации изолированы | Второй org admin не читает тест/версию/черновик/попытку/сдачу первого org; контекст в UI очищает список |
| S02 | Подразделения изолируют прохождение и результаты | Один attached тест в двух divisions; попытки, оценки, total и stats независимы |
| S03 | Полный admin управляет доступными подразделениями | Верхний переключатель, catalog/bank/detail/settings после смены; старая revision BFF не принимается |
| S04 | Фильтрованный admin имеет права выбранного division | view-only не создаёт/редактирует; edit работает только владельцу; в другом division права обновляются |
| S05 | Права результатов независимы | tests.edit без results.view не открывает чужую сдачу; results.view без tests.view читает свой division |
| S06 | Живой отзыв прав и отключение аккаунта | Изменение роли/отключение через core сразу закрывает метод и доступ UI |
| C01 | Список применяет фильтры до пагинации | Направление, поиск, владелец, relation и archive пересекаются; `total` одинаков на страницах |
| C02 | Банк видит только свою организацию | Full admin видит private; staff только own/attached/open bank; закрытие банка не снимает attachment |
| C03 | Create повторяем | Один ключ и тело возвращают один тест; другое тело/операция с тем же ключом — 409 |
| C04 | Attach сохраняет общий документ | Один test ID, общие content edits, независимые settings; повтор не дублирует placement |
| C05 | Deep copy независима | Новый test/version/owner, revision1, один placement, вопросы/ключи/веса полностью скопированы; hidden/bank false |
| C06 | Изменения/архив источника не меняют копию | Edit/archive source, GET copy content/settings/history сохранены |
| C07 | Архив блокирует все подключения | Student start/review закрыты; восстановление сохраняет настройки; owner detach409 |
| C08 | Настройки публикации валидны | Обе RFC3339 даты, end>start, time1–1440, не пустые вопросы, pass0–100/null |
| Q01 | Все три типа сохраняются и оцениваются | Single exact ID, multiple exact set, text нормализация; неизвестный option400 |
| Q02 | Student DTO не раскрывает ключи | Network response attempt/questions без `is_correct`/`correct_answers`; pending без question results |
| Q03 | ID редактора стабильны | Удалить и добавить question/option: старые ID не используются; reorder сохраняет IDs |
| Q04 | История версий подтверждена | Один save→один batch; no-op без версии; конфликт409 не выдаёт кандидат и не создаёт event |
| D01 | Черновик сохраняет незавершённое содержимое и layout | Пустые поля/нулевые веса в draft, восстановление координат, публикация проверяет полноту |
| D02 | Lock не перетирает другого редактора | Два edit пользователя,409; heartbeat90s, ревизия после каждого lock/save, unlock свой |
| D03 | Публикация draft повторяема | Одна исходная revision→один новый hidden тест; repeated publish возвращает тот же ID |
| A01 | Start/resume закрепляет версию и время | Повтор start сохраняет ID/order/deadline; editor change после start не меняет вопросы |
| A02 | В процессе имеется собственная попытка | Карточка «В процессе», reload продолжает, timeout прекращает ввод |
| A03 | Коммиты повторяемы | Same commit ack; изменённый payload/sequence409; максимум25; committed ответ не меняется |
| A04 | Offline финальный снимок авторитетен | Не доставленный промежуточный answer входит в finalize; владелец проверяет версию и вычисляет семантический хеш; старый клиентский хеш не подмешивается |
| A05 | Принятие официальной сдачи асинхронно | Finalize202, accepted/queued, result:null, status URL; immediate UI без оценки |
| A06 | Повтор finalize не создаёт вторую сдачу | Тот же снимок→тот же submission; изменённый409; повтор после upload deadline подтверждает принятое |
| A07 | Ожидающие статусы не считаются нулевым результатом | queued/processing/retrying/failed→null; ready score0→0; фильтр pending отражает статусы |
| A08 | Тренировка отдельна от official | Practice only after accepted/end и answers_visible; instant feedback; completion200 без submission/event |
| A09 | Разбор доступен только после ready и с допуском | Pending409; answers hidden403; ready показывает scoring version и реальные answers |
| M01 | Мониторинг выбранного подразделения | Попытки/сдачи/details/admin result actions ограничены текущей областью |
| M02 | Статистика соответствует списку | Test/account/calculation filter одинаков; two students разный score, summary account A только A |
| M03 | Force submit и reset используют отдельные edit права | Viewer403; editor принимает202; reset снимает active, поздний callback не возвращает старую попытку |
| M04 | Retry не заменяет историю | Pending retry→новая calculation revision, old callback409, ready retry409 |
| R01 | Реальная очередь и callback работают | Принятый finalize→outbox→quorum queue→calculate inbox→tests ready; один подтверждённый history row |
| R02 | Остановка calculate сохраняет принятую сдачу | Координированный stop только worker; accepted/null остаётся, Rabbit хранит event; restart даёт ready |
| R03 | Недоступный Rabbit не отменяет принятие | Координированный broker pause; finalize202; outbox сохраняет event; восстановление доставляет ровно один результат |
| R04 | Повтор сообщения/callback не дублирует балл | Same envelope и immutable output; одна history row; changed callback409 |
| R05 | Значимое редактирование пересчитывает старые сдачи | Change key/points/delete→queued/null→ready; исходные answers/division/version неизменны |
| R06 | Организационная настройка фиксируется в событии | Disabled edit не меняет grade; later enable не пересчитывает прошлое; следующий scoring edit действует |
| R07 | Исторические исключения сохраняются | Removed/type-changed вопрос не возвращается в максимум; новые вопросы не увеличивают старую сдачу |
| U01 | UI имеет банк, каталог, копию, подключение и filters | Выполнить действия мышью в существующем admin UI, проверить данные после reload |
| U02 | Две вкладки не смешивают контекст | Смена division/org обновляет обе; editor/попытка с локальными данными защищены, старые calls отменяются |
| U03 | Browser ходит только на локальный BFF | Ноль production/remote calls; cookie HttpOnly; bearer/service key не появляются в browser/localStorage |
| U04 | Ошибка транспорта сохраняет работу | Form/commits/final snapshot не удаляются после500/network, повтор выполняется по своему idempotency contract |

## Реальная асинхронная цепочка

1. Tests атомарно сохраняет окончательные ответы, ID сдачи, `status=accepted`, `calculation_status=queued`, ревизию задания и outbox в документе попытки. Браузеру сразу возвращается `202`.
2. Publisher отправляет постоянное сообщение в `cpm.events`; использует confirms и mandatory routing. Принятие не зависит от доступности Rabbit.
3. Quorum очередь `calculate.events.v1` получает `tests.grade.requested.v1` или `tests.content.changed.v1`. Calculate ACK после собственного долговечного inbox, дедупликация по `message_id`.
4. Worker читает неизменный input у tests по submission/calculation revision; проверяет org/division/account/version/hash, вычисляет score и сохраняет immutable output до callback.
5. Tests принимает callback только для текущего активного задания; записывает result и одну подтверждённую историю. Старые/reset callbacks отклоняются, идентичный повтор безопасен.
6. UI раз в 5 секунд обновляет разрешённые публичные списки catalog/access/submissions и показывает готовый результат отдельно от подтверждения принятия. Прямой запрос `status_url` текущий UI не использует; BFF всё равно возвращает корректную публичную ссылку. Пересчёт снова делает текущий result null, прошлый балл доступен в административной истории.

UI не вызывает calculate напрямую. Синхронное исключение — тренировка; запрос всё равно проходит через публичный tests API.

## Исправления владельца в этой интеграции

`services/tests/internal/tests/attempts.go`: статистика теперь использует общий со списком фильтр `submissionFilter`. Раньше OpenAPI разрешал `account_id` для stats, но реализация его игнорировала и могла показывать среднее всего подразделения вместо выбранного ученика. Теперь учитываются `account_id`, `test_id`, `calculation_status`; студент всегда ограничен своим auth account. Некорректный `practice` в попытках отклоняется400 вместо неявного выбора official.

Регрессии в `submission_filters_test.go` проверяют scoped selection, раздельные права каталога/результатов, невалидные фильтры, различие pending/null и готового нуля, строгий practice. Схема/данные Mongo не менялись. Go race/vet tests и calculate проходят. В calculate по аудиту доставки, алгоритма, callback и пересчёта дополнительных изменений не потребовалось.

## Правила локального прогона

API fixtures создаются через auth/core/tests/BFF. Уникальный префикс каждого прогона предотвращает изменение соседних заданий. Прямые SQL/Mongo mutations не используются. Старые service scripts содержат `clock_fixture`/cleanup с прямой записью в Mongo; их нельзя автоматически запускать в текущем режиме проверки. Результат server unit test не заменяет сценарий браузера или реальный Rabbit callback.

Остановки/restarts брокера и calculate выполняются последовательно одним владельцем стенда после завершения обычного прогона; восстановление идёт в `finally`. Docker neighbours не перезапускаются. Production migration, суммарный рейтинг по нескольким тестам и нагрузка450 пользователей не относятся к текущему подключению test workflow.

## Выполненные проверки 8 октября 2026

`services/bff/scripts/tests_http_test.py` прошёл обычный API-only прогон (313 проверок) и расширенный `--queue-outage` (370 проверок, включая дополнительные проверки закрытого банка). Последний отчёт: `dev/local/bff-tests-http-report.json`; demo: `dev/local/bff-tests-demo.json`. Пишущих SQL/Mongo операций в скрипте нет. Синтетические API fixtures сохранены для проверки интерфейса.

Подтверждены создание/повтор/CAS, bank open/closed, самостоятельная copy с ключами и весами, source edit/archive independence, attach/detach, пересечение relation/owner/direction и пагинация, org isolation и переключение org студента, view/edit права по двум divisions и results-only роль, живой отзыв права, draft lock двух редакторов/layout/publish repeat, повтор commit/finalize, official queued/null и реальные оценки80/0, scoped account stats, division fence, review rights, disabled policy и реальный пересчёт80→50 с двумя записями истории, тренировка через calculate.

Координированные outages подтвердили сохранение `202 accepted` при pause calculate и Rabbit, pending review409, новую ревизию retry и eventual ready, а также skipped старого задания после administrative reset. После прогона Docker inspect подтвердил оба контейнера `paused=false`, `status=running`. Ошибки создания callback и crash recovery на каждой внутренней точке не имитировались этим прогоном; эти более узкие условия имеют отдельные service unit tests.

Go `test -race ./...` и `vet ./...` в tests/calculate прошли с четырьмя новыми регрессиями фильтра/состояния. Факт API-прохождения не заменяет проверку кнопок, локального хранилища и переходов в браузере.

Student Dev UI3001: `services/bff/scripts/tests_browser_student.mjs` прошёл 12 сценариев; отчёт `dev/local/bff-tests-browser-student-report.json`. Реально подтверждены pinned/randomized start, запрет выхода из соседней вкладки во время попытки, reload с сохранёнными ответами и порядком, offline finalize с сохранением запечатанного снимка для повтора, `202 queued` без балла, pending после reload, реальная доставка Rabbit/calculate после восстановления worker и оценка 100, допущенный разбор, тренировка с обратной связью для single/multiple/text, очистка данных при смене division/org, отсутствие browser errors и внешних запросов. Worker восстановлен в `finally`. Скриншоты: `bff-tests-student-accepted.png`, `bff-tests-student-pending.png`.

Полный admin Dev UI3001: `services/bff/scripts/tests_browser_admin.mjs` прошёл 17 сценариев; отчёт `dev/local/bff-tests-admin-browser-report.json`, отдельные UI fixtures `bff-tests-admin-browser-demo.json`. Создание classic hidden теста с вопросом, ключом и весом, открытие банка, переключение подразделений, общее подключение, независимая copy, снятие подключения, архив источника с сохранением копии выполнены реальными кликами. Проверены пересекающиеся фильтры, действующая оценка 50 и история 80→50, canvas create/lock/autosave вопроса и текстового ключа/публикация полного скрытого теста, view-only в1001 и edit в1002, results-only landing и реальные строки без прав tests/users, изоляция другого org admin. Внешних запросов, page errors и неожиданных alerts нет; браузер закрыт в `finally`. Ширина документа не превышает viewport1440 на desktop и390 на mobile после завершения адаптивного перехода.

Скриншоты admin: `bff-tests-admin-desktop.png`, `bff-tests-admin-mobile.png`, `bff-tests-admin-canvas.png`, `bff-tests-results-only.png`. Частичные диагностические `--roles-only` и `--canvas-only` тоже прошли; итоговые 17 относятся к единому полному прогону, а не сумме частичных запусков.

Выявлены и исправлены две гонки UI: поздняя выдача Web Lock после cleanup удерживала защиту контекста даже после закрытия редактора; первый ответ draft lock мог перезаписать уже введённые название/направление/вопрос. Общий hook учитывает disposed callback; draft editor ждёт подтверждения блокировки и сохраняет локальные изменения при её подтверждении. Также исправлена нулевая высота canvas внутри обёртки доступа: редактор теперь получает высоту viewport, и вопросы доступны настоящему клику без обхода pointer checks. Для тренировки исправлены DTO обратной связи и завершение с немедленным локальным итогом, отдельным от официальной сдачи. Все исправления подтверждены последующим полным браузерным прогоном.

Ограничение публичного core для multi-org staff подтверждено реальным запросом: admin второй организации создал изолированную роль10066, но добавление dev staff аккаунта251 из первой организации через `PUT /v1/core/divisions/2001/memberships/251` вернуло403 `access_denied`. `core_user_owners.organization_id` должен совпадать с организацией назначающего администратора. Поэтому сценарий переключения двух организаций одним filtered-admin нельзя создать этим публичным API; BFF не расширяет данный контракт. Реальный multi-org student switch проверен в браузере, org isolation администратора — отдельно. Фикстура и ответ сохранены в `bff-tests-demo.json.staff_multi_org_grant`.

Матрица выше включает весь требуемый контракт, а не только выполненные smoke-сценарии. Через браузер не имитировались сетевой сбой сохранения административной формы, истечение/heartbeat блокировки по реальному таймеру, успешный force-submit, каждое историческое исключение при удалении/смене типа вопроса и авария процесса на каждой внутренней точке доставки. Эти узкие случаи проверяются отдельными owner/adapter unit tests либо требуют отдельного согласованного runtime сценария. Нагрузка 450 пользователей не запускалась. Оптимизированная сборка и финальный smoke после rebuild учитываются отдельным итогом стенда.


## Финальная сборка и запуск

Финальная версия BFF пересобрана и запущена, UI собран через `./dev/bff build-ui` и работает через `./dev/bff start-ui` на `http://127.0.0.1:3001`. TypeScript и optimized build прошли. Полный Vitest: 141 тест в 26 файлах. Go race/vet BFF также прошли; ссылки `status_url` адаптируются к BFF без округления числовых идентификаторов. OpenAPI работающего BFF содержит 95 разрешённых операций.

После пересборки `tests_browser_smoke.mjs` прошёл 5 проверок: административный каталог/банк, viewport390 без переполнения, вход results-only сотрудника сразу в разрешённый журнал, готовый результат ученика100, отсутствие JS ошибок/внешнего трафика и HttpOnly cookie. Отчёт `dev/local/bff-tests-optimized-report.json`; скриншоты `bff-tests-optimized-mobile.png`, `bff-tests-optimized-student.png`. Финальный API smoke прошёл 23 проверки, включая реальные50/0, запрет чтения сдачи другого выбранного подразделения, защиту bank scope, чтение homework и правильную cookie-схему OpenAPI (`bff-tests-final-http-report.json`). Это дополнительные smoke-проверки, отдельные от 370 основного API-прогона.

Полный ESLint остаётся с 80 исходными ошибками и 22 предупреждениями. Сравнение 95 затронутых JS/TS файлов с Git HEAD не нашло новых нарушений: в затронутой области ошибки25→20, предупреждения3→3. Новые boundary/helpers/tests проходят ESLint; отчёт `bff-tests-ui-eslint-baseline.json`. Неизменённые старые ошибки не включены в результат успешной сборки.

Финальный Docker inspect: BFF/tests/calculate/Rabbit работают, `paused=false`, health=`healthy`. Синтетические dev fixtures сохранены; production не изменялся, прямые SQL/Mongo writes не использовались.
