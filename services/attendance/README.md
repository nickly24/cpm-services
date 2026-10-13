# CPM Attendance: расписание, посещаемость и отгулы

Самостоятельный Go-сервис `services/attendance`, локальный dev `http://127.0.0.1:8013`. Владелец своих календарей, занятий, дней журнала, отметок, версий типов, заявок/дат/вложений отгулов и истории. BFF и React UI подключены в локальном dev режиме; production endpoints и исходные таблицы/коллекции не переключены. Telegram и очередь не используются.

Карта всех legacy/core методов и отличий — [docs/as-is.md](docs/as-is.md). План attendance→BFF→UI — [docs/implementation-plan.md](docs/implementation-plan.md). Подключение, проверки и ограничения — [docs/bff-ui-integration.md](docs/bff-ui-integration.md).

**Постоянная защита истории работает:** core сохраняет собственный durable marker до записи attendance. Удаление участия после отметки или заявки возвращает `409 membership_has_history`, включая снятые отметки и отвязанные даты. Реальные auth/core/MySQL проверены напрямую и через BFF. [Контракт и гонки core](docs/core-history-guard-proposal.md).

## Запуск

Уже существующие dev MySQL/auth/core и Docker network `cpm-dev_default`:

```sh
./dev/attendance setup
./dev/attendance up
./dev/attendance check
./dev/attendance status
```

Compose самостоятельный (`dev/compose.attendance.yaml`, profile attendance), запускает только attendance. Не останавливает соседей и не запускает Rabbit. Миграция только трёх собственных attendance_* таблиц; allowlist перечитывается/дополняется с сохранением чужих записей. Секреты в `dev/local/attendance.env` (0600), собственный QR key отдельно от service key. Config запрещает другие DB и удалённый MySQL; JWT подписан RS256, проверяется актуальным auth introspect на каждом запросе. API timeout 20 с, peer 4 с, redirects отключены, context cancellation распространяется.

OpenAPI: `GET /openapi.json`; process/DB health: `/health/live`, `/health/ready`. Готовность DB не означает готовность history guard. HTTP-test оставляет собственные `DEV Attendance` fixtures и отчёт без токенов/паролей в `dev/local/attendance-http-report.json`. Повтор setup не меняет старый QR key и не пересоздаёт чужие данные.

## Контракт

Ответ `{status:true,data:{revision,value}}` или `{status:false,error}`. IDs scoped организацией из проверенной сессии. `account_id` отличается от `profile_id`. Не принимать X-User/Role или клиентский organization_id. Все мутации, включая DELETE, требуют JSON:

```json
{"expected_revision":5,"data":{"date":"2026-10-08","comment":"Первый день","lesson_ids":[]}}
```

Обязателен `Idempotency-Key` 8..100 символов. CAS revision общая для организации: параллельные изменения имеют одного победителя, 409 требует перечитать состояние; неизвестный исход повторять с тем же ключом и исходным телом. Ключ scoped account+org. Возвращаются исходные ID/revision при байтово идентичном повторе; изменённое тело/метод/path =409. Receipt replay также требует актуального auth и object edit permission. QR выдаётся через POST с `{}`, без CAS/receipt. После reload неизвестной команды UI использует отдельный receipt GET и обновляет данные; QR/body/credentials в браузере не сохраняются. Ответ404 не доказывает отсутствие commit: UUID остаётся UNKNOWN, автоматической новой отправки нет. Полный контракт — [U04](docs/command-receipts-u04-proposal.md).

| Метод | `/v1/attendance` путь | Значение data / read value |
|---|---|---|
| GET | `/command-receipts/{canonical_uuid}` | только metadata подтверждённого commit своего actor/org, original revision/method/path/scope; current права проверяются заново, без query/body |
| GET | `/calendars` | доступные calendar metadata, source division/name, membership и can_edit |
| GET | `/schedule?division_ids=1,2&date_from=...&date_to=...` | calendars/events/overlap_clusters |
| GET | `/me/schedule?date_from=...&date_to=...` | только student: все свои действующие division текущей org |
| POST | `/schedule/bulk` | `{creates:[Lesson],updates:[Lesson с id],deletes:[{id,division_id}]}` ≤100 операций |
| GET/POST | `/resources/rooms` | свои stable room resources, создать только admin `{code,name}` |
| GET | `/audit?after_revision=0` | admin: следующие ≤100 metadata ревизий; собственные immutable snapshots остаются в DB |
| GET | `/me/attendance?date_from=...&date_to=...` | собственные журналы доступных division |
| GET/PUT | `/divisions/{division}/calendar` | `{visibility:"closed" или "organization",timezone:IANA}` |
| GET/POST | `/divisions/{division}/schedule` | список за период / Lesson |
| PUT/DELETE | `/divisions/{division}/schedule/{lesson}` | Lesson / `{}` |
| GET | `/divisions/{division}/schedule-references` | только schedule.edit: groups/teachers/schools/rooms |
| GET/POST | `/divisions/{division}/attendance-types` | свои типы / TypeInput |
| PUT/DELETE | `/divisions/{division}/attendance-types/{type}` | новая версия TypeInput / archive `{}` |
| GET/POST | `/divisions/{division}/class-days` | за период / `{date,comment,lesson_ids}` |
| GET/PUT/DELETE | `/divisions/{division}/class-days/{day}` | scoped Day / DayInput / `{}`; история защищает дату/удаление |
| GET/POST | `/divisions/{division}/class-days/{day}/attendance` | scoped journal / `{account_id,attendance_type_id}` |
| DELETE | `/divisions/{division}/class-days/{day}/attendance/{account}` | `{}`; tombstone, zap-link защищён |
| GET | `/divisions/{division}/attendance-report` | date_from/date_to, optional account_id/group_id; days/entries/counts_by_type_code |
| GET | `/divisions/{division}/students` | разрешённый live roster; student только себя, proctor назначенную группу |
| POST | `/divisions/{division}/class-days/{day}/qr` | собственный student, `{}` → signed QR + expires_at (3 мин) |
| POST | `/divisions/{division}/class-days/{day}/scan` | `{qr}` → тип с настроенным scan_default, обычные scope/guard проверки |
| GET/POST | `/divisions/{division}/zaps` | scoped заявки / `{account_id?: для staff,text,dates:[YYYY-MM-DD],images:[{mime,data base64}]}` |
| GET | `/divisions/{division}/zaps/{zap}` | `{zap,image_count}`, без незащищённого URL вложений |
| GET | `/divisions/{division}/zaps/{zap}/images/{image}` | owner/zaps.view, 1-based index, image/png или image/jpeg binary |
| POST | `/divisions/{division}/zaps/{zap}/process` | `{status:"approved" или "rejected",answer}` |
| POST | `/divisions/{division}/zaps/{zap}/dates/{date}/retry` | `{}` для одобренной заявки |
| DELETE | `/divisions/{division}/zaps/{zap}/dates/{date}` | `{}`; убрать только принадлежащую ей отметку, сохранить историю |

Lesson input сохраняет date/start_time/end_time/lesson_name/teacher_name/location/classroom/color/is_changed/is_in_person/is_for_all/is_public/school_id; добавлены division_id (в batch), target_group_ids, nullable teacher_account_id/room_resource_id. Простой POST/PUT ID не принимает; PUT id в path. School скрывает адресное событие от чужой школы; is_for_all=false требует явные target groups. Нельзя ссылаться на school/group/teacher чужой организации/подразделения. Day-lessons только той же даты/подразделения; создание lesson не создаёт посещения, для старой отметки lesson не обязателен.

TypeInput: `{code,name,color,mark,sort_order,is_active,counts_as_present,counts_as_absent,is_excused,scan_default,zap_default}`. Смысл — конфигурация, без branch по ID1..8/ID2 или известному коду. Code immutable/unique в division, default scan/zap максимум один active. Записанный Entry содержит immutable snapshot всего Type/version/name/semantic/display и имени/группы студента. Archive не меняет прошлое. Пустая отметка =unmarked, не автоматически absence. UI/XLSX должны использовать snapshot полей.

Даты строго YYYY-MM-DD (1900..2200), HH:MM, включительные периоды ≤366 календарных дней. Интервалы [start,end), календарь имеет явный IANA timezone; неоднозначные/отсутствующие DST минуты отвергаются. Timezone нельзя менять после появления lessons. Все наложения разрешены по решению пользователя: overlap_clusters содержит только доступные события и source division; teacher_name не ID ресурса. UTC starts_at/ends_at помогают UI правильно накладывать calendars разных timezone.

Отгулы: pending → approved/rejected с последующей возможностью исправить решение; no_class_day отдельно по каждой дате, retry после создания day; unlink сохраняет tombstone. Вложение: ≤3 PNG/JPEG по512КБ, проверка подписи/формата/размеров картинки, не URL клиента. Нельзя вручную перезаписать/снять zap-mark. Одобрение заменяет ручную/QR отметку настроенным типом отгула, сохраняя исходную историю; повторное решение разрешено, answer может быть пустым. До60 дат с trim/dedup, текст/ответ до5000 Unicode символов. Отгул другого владельца не подменяется молча.

## Права и хранилище

Admin — все division своей текущей организации. Staff — permissions конкретного division из актуального introspect, даже если active division другой. Проктор — только группа с подтверждённым core назначением; supervisor — journal read своего division; student — только свои записи. Scan-only staff может использовать только scan_default через подписанный QR и читать нужные дни/тип/состав/отметки дня, не произвольный ручной тип или общий отчёт. Право calendar.view не открывает journal/roster/zaps. Опубликованный organization calendar доступен другим участникам org; private только участникам и authorized editors.

Own MySQL aggregate по org под SELECT FOR UPDATE, параметризованный SQL, snapshots/audit и receipts в той же транзакции. Нет FK к чужим таблицам. Ограничения dev: document32MiB, calendars100, rooms1000, lessons10000, days5000, entries50000, types2000,zaps5000. Это ограниченный single-aggregate этап, сериализующий writes организации; archive/audit snapshots содержат изображения и требуют будущей политики хранения/backup, масштабирование и blob-store отделяются до production.

Read-only core references используют существующие `/internal/core/test-references`, `/internal/core/homework-references` временно по их точному DTO. Schools/current profile через public scoped core API. Недоступность peers —503; отсутствующий объект —404. Надёжные стабильные ссылки присутствуют; нет своего справочника людей/организаций/division. Ratings/calculate не используют attendance в действующей формуле; новый коэффициент и очередь не вводились.

## Переход старых данных

Один владелец production record после cutover. Импортировать два legacy журнала с provenance отдельно, не склеивать account+date без division. Маппинг legacy profile→account подтверждать core; старые числовые rate и type IDs преобразовать в отдельные versioned codes каждого division с сохранением исходного значения. `is_for_all=false` без старой адресности требует явного решения миграции, не автоматически «все группы». Отгулы/защищённые файлы/даты и уже записанные marks перенести вместе с source ID mapping. Dry run/reconciliation, заморозка старых writes, единый import, сверка, переключение BFF; никаких dual-write. Старые core endpoints и таблицы в этой работе сохранены. Production migration отдельно.

Свежая приёмка09.10.2026: Attendance HTTP178, Core metadata88, Uploads attachments290 HTTP/envelope/content assertions, browser journal8 с5 XLSX, BFF/UI отгулы15 и scan4 с3 XLSX. U04 завершён отдельно: Native39/5 реальных HTTP-сценариев/925 assertions и BFF296/UI535/57/15 actual browser сценариев lost-reply/reload/relogin/cross-tab без повторных записей. Все проверки выполнялись в dev на собственных fixtures. Точные отчёты и текущий handoff: [handoff](docs/acceptance-handoff-2026-10-09.md). Общий build/runtime UI/Core/BFF выполняет их единый владелец.
