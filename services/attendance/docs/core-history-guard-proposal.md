# Постоянная защита участия: реализованный контракт core

Реализовано 09.10.2026 после явного разрешения пользователя «да, доделай». Core владеет только постоянным признаком защищённой истории; все отметки, заявки, даты и изображения принадлежат attendance. Контракт закрыт от browser/Bearer и доступен только по служебному ключу.

## Запрос

`POST /internal/core/attendance/history-reservations`, только `X-Service-Key` (`AUTH_INTERNAL_KEY`, не JWT и не заголовки пользователя). JSON, неизвестные/дублирующиеся поля отклонять:

```json
{"organization_id":1001,"division_id":1002,"account_id":9,"operation_key":"53d25eb0-793f-41eb-9b5a-7b2265ac8a62"}
```

Все ID положительные int64; account_id — auth account, не legacy profile. operation_key 8..100 символов. Ответ 200:

```json
{"status":true,"data":{"organization_id":1001,"division_id":1002,"account_id":9,"permanent":true,"reservation_id":"9f7a0d4173ef4ef68e7bf7ccaf62b7049f7a0d4173ef4ef68e7bf7ccaf62b704"}}
```

400 invalid DTO, 401 service key, 404 нет активного student membership/account/division/org, 503 unavailable. Повтор той же области безопасен, возвращает тот же durable reservation. Обычная проверка «участие существует» или временный lease недостаточны.

## Атомарность и жизненный цикл

Core берёт совместную блокировку активной организации, затем эксклюзивные блокировки division, account и account state в порядке, совместимом с revoke. Проверяет активный аккаунт ученика и действующее участие, сохраняет постоянный marker в `core_attendance_history_reservations` одной MySQL-транзакцией. Совместная блокировка организации совместима с FK при параллельной выдаче участия. Административное снятие участия проверяет marker под теми же блокировками. Архивация подразделения также проверяет его; смена группы и блокировка аккаунта сохраняют участие и marker. FK защищают физическое удаление связанного org/division/account. Создание marker и revoke не могут одновременно успешно пройти: победивший revoke даёт отказ резервированию; победивший marker даёт `409 membership_has_history` снятию.

Marker не снимается при отмене отметки, отвязке отгула, смене группы, деактивации или удалении type. История attendance остаётся постоянно. Прямого FK в attendance и GET «есть история» перед revoke недостаточно: возможна гонка. Недоступность attendance не должна открывать удаление участия; решение локально хранит core как projection protection, а не копию отметки.

Attendance вызывает guard только после проверки собственного запроса, непосредственно перед записью первой/исправленной отметки или заявки. Если ответ guard потерян, повтор безопасен. Если собственная транзакция потом откатилась, marker остаётся консервативно: это резервирование истории, не распределённая транзакция. Автоматического release не существует; возможное административное исправление лишнего marker требует отдельной проверяемой процедуры и аудита. Эта консервативная семантика реализована без метода release.

Не предлагается обход через core_rating_locks, записи в core_attendance/core_zaps, общий JOIN или отключение core revoke защиты.

## Подтверждение готовности

`services/core/scripts/attendance_history_http_test.py` прошёл 53 реальные проверки, включая 8 параллельных гонок guard против revoke: успешен либо marker с отказом revoke, либо revoke с отказом guard. Повторы возвращают неизменный reservation_id; деактивация/реактивация и отсутствие commit в attendance не снимают защиту. Отчёт: `dev/local/attendance-core-history-report.json`.

Миграция `services/core/migrations/004_attendance_history_reservations.sql` идемпотентна. Обычный core setup включает её; для существующего стенда `dev/.venv/bin/python services/core/scripts/attendance_history_setup.py` применяет только эту миграцию и дополняет allowlist под файловой блокировкой. Core Go/race/vet также пройдены. При закрытой БД положительный ответ не выдаётся. Реальный core JSON 404 означает исчезнувшее участие; старый отсутствующий endpoint/plain 404 по-прежнему вызывает `503 core_history_guard_required` в attendance.
