# Расписание и посещаемость: AS IS → отдельный attendance

Имя нового владельца: `services/attendance`, `/v1/attendance`. Анализ 08.10.2026. Старые endpoint и таблицы не переключены. Никаких dual-write, прямых запросов attendance к чужим таблицам, очередей или изменений BFF/UI.

| Реальные методы legacy | Новый core сегодня | Цель attendance |
|---|---|---|
| GET/POST `/api/schedule`, PUT/DELETE `/api/schedule/{lesson_id}` | GET/POST `/v1/core/divisions/{division}/schedule`, PUT/DELETE `.../schedule/{lesson}` | отдельный календарь division; все предметные поля сохранены; scoped IDs |
| POST `/api/schedule/bulk` (`creates/updates/deletes`) | POST `.../schedule/bulk` только добавляет `lessons` | атомарный create/update/delete нескольких division, CAS и receipt |
| POST `/api/get-attendance-by-date`, `/get-attendance-by-month`, `/add-attendance` | нет адаптера числовой модели | отдельно мигрировать старую attendance; не смешивать по совпадению даты |
| POST `/api/get-attendance` (exams_bp) | нет алиаса | личный журнал дневной модели; legacy числовой журнал остаётся legacy |
| GET `/api/attendance-types` | GET `/v1/core/attendance-types`: общий attendance_types | свои типы org+division, immutable версии, archive |
| GET/POST `/api/class-days`, GET/PUT/DELETE `/api/class-days/{day}` | те же операции под `.../divisions/{division}` | один day на org+division+date; необязательные ссылки на занятия; защита истории |
| GET/POST `/api/class-days/{day}/attendance`, DELETE `.../{attendance_id}` | GET/POST `.../attendance`, DELETE `.../{account_id}` | отметка account+day, группа/тип на момент записи, CAS, audit, tombstone |
| GET `/api/attendance-report` | GET `.../attendance-report` | scoped entries/days/summary, JSON для UI экспорта |
| GET `/api/students/{student_id}/class-day-attendance` | фильтр account_id отчёта | GET `/v1/attendance/me/attendance`; account_id не legacy profile_id |
| POST `/api/create-zap`, `/get-zaps-student`, GET `/get-all-zaps`, `/get-zap/{zap}` | GET/POST `.../zaps`, GET `.../zaps/{zap}`, GET `.../images/{image}` | весь домен отгулов переносится в attendance (ответ пользователя), старые записи core до cutover |
| POST `/api/process-zap`, `/api/zap-dates/{date}/retry`, `/unlink` | POST `.../zaps/{zap}/process`, `.../dates/{date}/retry`, DELETE `.../dates/{date}` | будущая идемпотентная команда core→attendance, одна запись/один владелец |
| QR: локально генерируется строка profile id; scan вызывает обычный POST class-day attendance, тип 1 | специального QR API нет | краткоживущий подписанный bearer QR на account+org+division+day; scanner и группа проверяются сервером |

## Подтверждения в коде

- `cpm-back-main/cpm_back/services/serv/schedule_manager.py`: Mongo `_id`, поля date/start_time/end_time/lesson_name/teacher_name/location/classroom/color/is_changed/is_in_person/is_for_all/is_public/school_id; школьная фильтрация, полуоткрытое пересечение, virtual batch. Запись Mongo batch последовательная, не транзакционная: не объявляем её настоящей атомарностью.
- `services/core/internal/core/schedule.go`: SQL core_lessons, division lock, bulk только create. Конфликт по совпадению видимости, не подтверждённому ресурсу.
- `services/core/internal/core/attendance.go`: дневной журнал, JOIN текущей группы и общего типа; день нельзя перенести/удалить с отметками; ручной override zap запрещён.
- `services/core/internal/core/organization.go:174`: revoke проверяет только core_attendance/core_zaps/рейтинги. Новый FK attendance не сохраняет эту внешнюю защиту.
- `services/core/internal/core/zaps.go`: специальный attendance type ID 2; `services/serv/zap_dates.py` legacy также пишет тип 2, retry при no_class_day/failed и unlink. Вложения core защищает владельцем/правом zaps; attendance их не проксирует.
- `cpm_front_new/lib/attendance/attendance-api.ts`: scan → setClassDayAttendance; `attendance-qr-card.tsx`: QRCode(String(user.id)); `attendance-palette.ts`: восемь кодов и UI символы; report-export создаёт клиентский экспорт. Новый owner возвращает display данные, BFF должен адаптировать DTO явно.
- `services/auth/internal/auth/{http,model}.go`: introspect = me, актуальные organizations[].divisions[] с group_id и permissions. Они позволяют все личные division и delegated edit независимо от active division. JWT RS256 signed, auth проверяет отозванные сессии/context_version.
- `services/core/internal/core/homework_references.go`: уже существующий service-key reference API проверяет division/group/account, возвращает account_id отдельно от profile_id, группы и назначения проктора. Используем read-only API, документируем временное имя. Школы текущей org доступны через core schools; собственная школа через core/me.
- `services/ratings/internal/ratings` и `services/calculate`: формула получает homework/tests/exams, attendance отсутствует. Обязательную зависимость или коэффициент не добавляем.
- `services/bff/README.md`: расписание/посещаемость ещё не подключены; generic calendar filter нельзя реализовать через auth/context. Сервисы tests, homework, cards, exams, ratings имеют отдельные модели/владельцев и собственные snapshot/reference API.

## Модель и права

Core владеет людьми, org/division/groups/schools/directions; auth — сессиями и правами. Attendance хранит только собственный агрегат организации: календарные настройки division, занятия, помещения как ресурсы расписания, типы с версиями, дни, отметки, audit и receipts. Числовой ID уникален внутри организации; всегда требует org scope, не принимаем org из тела. Хранилище JSON агрегата в собственной таблице MySQL обеспечивает одну блокировку org и атомарную проверку итогового расписания; это ограниченная dev-версия с явным лимитом, не заявка на неограниченный throughput.

Просмотр: календарь closed — участник division с правом/предметной ролью, organization — любые участники org; школьная/групповая адресность всё равно фильтрует студента. Личная сборка только собственные актуальные division в выбранной организации (ответ пользователя), включая адресные занятия своей группы/школы; is_for_all=false требует явные target_group_ids. Доступ к calendar никогда не разрешает journal. Staff permissions берём из выбранного объекта division в introspect, admin только текущая собственная org. Проктор читает и отмечает свою назначенную группу; supervisor читает журнал своего division; студент только себя.

Занятия разных calendars не сливаются. Пересечения ответ содержит только среди видимых занятий. Надёжные ресурсы: teacher_account_id (подтверждён core, staff/proctor/examinator/supervisor), room_resource_id из собственного каталога org и target_group_ids core. teacher_name/location/classroom остаются display и не дают гарантию конфликта. Все пересечения разрешены (ответ пользователя). Сервер возвращает связные overlap_clusters для раскрытия «гармошкой» в UI; ресурсные совпадения только информативны. DST ambiguous/nonexistent local time отвергаем; timezone calendar неизменна после занятий, UTC интервалы в выдаче, [start,end), date_from/date_to включительно ≤366 дней.

Одна отметка account+day; lesson не обязателен. Отсутствие отметки — unmarked, не автоматически absence. Тип: code неизменен, name/display/semantic/presence/absence/excused/scan_default как конфигурация; новая версия при редактировании, старый snapshot не пересчитывается. Типы всегда scoped; удаление = archive. Группа и имя в отметке — snapshot, смена текущей группы не переписывает прошлое. Tombstone сохраняет отменённую отметку и постоянную историю day/account; даже после отмены нельзя удалить/перенести day. Связь day-lessons только одной даты/подразделения, урок с связью не удалять/не переносить.

## Переход

Dev создаёт исключительно attendance_* таблицы и вымышленные события; старая история остаётся у старого владельца. Перед cutover: постоянный core guard, scoped references, перенос заявок/вложений/дат отгулов, reconciliation двух legacy journals с provenance и mapped account IDs; импорт immutable типов отдельно для каждого division, запись source-id mapping, dry run. На время cutover старые writes заморозить, single import и сверка, затем переключить BFF; legacy endpoints read-only/adapter к единому владельцу по отдельному решению. До этого два набора dev-данных не представляют одну и ту же production запись.
