# Изменения экзаменов и актуальность рейтинга

Ratings постоянно принимает уже существующий outbox Exams. Publisher владельца отправляет persistent `exams.source.changed` в durable topic exchange `cpm.events` с mandatory delivery и publisher confirms. Ratings создаёт durable quorum queue `cpm.ratings.exams-source.v1` с этим binding. Повреждённые события попадают в `cpm.ratings.exams-source.dead.v1` через `cpm.events.dead` / `ratings.exams-source.invalid.v1`; dead lettering at least once, overflow reject-publish, delivery limit отключён.

Конверт: `message_id` UUID, `message_type:"exams.source.changed"`, `schema_version:1`, `occurred_at` RFC3339Nano, `producer:"exams-service"`, `correlation_id` UUID, `payload:{organization_id,division_id,exam_id,exam_revision,source_revision,reason}`. `source_revision` строго возрастает в конкретной паре org/div; `exam_revision` события технический. HTTP снимок содержит отдельные смысловые exam/division revisions и `source_revisions[division]` — именно последний счётчик сохраняется в `ExamsSnapshot.source_revision`.

Миграция `003_source_events.sql` создаёт собственные таблицы `ratings_source_events_v1` (UUID+canonical payload hash) и `ratings_source_revisions_v1` (source/org/div high watermark). Consumer подтверждает сообщение после единого commit этих записей. Потеря ACK безопасна: exact duplicate повторно подтверждается; тот же UUID с другим содержимым отклоняется. Запоздавшая версия не откатывает high watermark. Ошибка БД закрывает consumer channel, unacked message возвращается в очередь; соединение восстанавливается с паузой2 секунды.

Событие не создаёт jobs, не изменяет сохранённые оценки, публикации или историю. При чтении `list/me/account/report` версия события больше сохранённого snapshot counter даёт `freshness:{is_stale:true,reason:"source_changed",...}`. Проверка hash всех владельцев и календарных переходов также сохраняется. Перед атомарным переключением публикации транзакция блокирует scoped watermark и запрещает публикацию, если новый source event уже подтверждён: job становится `superseded`, `error_code:"source_changed_before_publication"`. Новый расчёт требует явного запроса.

Изменения завершённой попытки, выбранной пересдачи или действующей апелляции относятся к её подразделению. Изменение общего названия/направления и подключения экзамена выпускается Exams для всех затронутых подразделений. Незавершённые попытки и неподтверждённые голоса не меняют смысловой рейтинг соседнего подразделения. Отбор последней завершённой попытки/апелляции остаётся владельцу Exams.

Tests external использует согласованный HTTP snapshot/hash contract. Импорт/ручная коррекция/удаление меняют external source version; Ratings проверяет её перед staging, перед публикацией и при чтении. Нового Tests outbox без постоянного потребителя в этом этапе не создаём.

## Проверка

`source_events_probe.go` использует только приватный localhost `cpm_dev` SQL13306 и Rabbit5672/vhostcpm_dev. Создаёт собственные изолированные namespace IDs, не изменяет предметные Exams/Core records, не перезапускает брокер. Проверяет duplicate/out-of-order/scope/conflict/schema/DLQ и отсутствие автоматически созданных jobs. Фикстуры сохраняются.

```sh
cd services/ratings
go build -o /tmp/cpm-ratings-source-events-probe ./scripts/source_events_probe.go
```

Запускать бинарник с переменными из приватного `dev/local/ratings.env` без вывода их значений и абсолютным `RATINGS_EVENTS_REPORT`. Для локального smoke после migration003 и targeted Ratings update получен report `dev/local/ratings-source-events-report.json`: passed, 8 cases, 7 messages, 4 inbox records. Это не заменяет совместный lifecycle HTTP прогон Exams→Ratings.

Отдельный живой [shared classic lifecycle](exam-lifecycle-acceptance.md) прошёл 808 HTTP-проверок, 8 jobs и 16 персональных расчётов. Последующий read-only аудит root проверил конкретный exam98 в своих A1/A2: 52 проверки, 10 из 10 опубликованных owner outbox events с точным canonical hash сохранены в Ratings inbox, scoped watermarks 9/7, отсутствие autojobs. Журнал переходов совпал с фактическими delta событий; pending/historical изменения не выпустили новых событий. Отчёт `dev/local/ratings-exam-events-audit-report.json` имеет `0600`, без credentials. Владельцы, базы и очереди при диагностике не изменялись.
