# Exams → BFF/UI: контракт и проверка 9 октября 2026

Проверка выполнена чтением `services/bff/internal/bff/exams.go`, `server.go` и адаптеров `cpm_front_new/lib/bff/exams-{admin,student,conduct,shared}.ts`, `cards.ts`. BFF и UI не изменялись. HTTP-подтверждения нового прогона записываются отдельно в игнорируемый `dev/local/exams-audit-http-report.json`; наличие скрипта само по себе не подтверждает успешный прогон.

## Согласованные контракты

| Экран/процесс | Маршрут владельца | Что проверено в адаптере |
| --- | --- | --- |
| Банк и каталог | `GET /v1/exams?scope=division|bank|organization` | BFF проксирует только публичные разрешённые маршруты. Область организации/подразделения берётся из live Auth; frontend `/api/exams` — локальный alias. |
| Проведение | `/assignments/{id}/attempts/ensure`, `/attempts/{id}/{ready,start,vote,next-question,replace-question}` | DTO `expected_history_generation`, `expected_state_version`, `presented_question_id`, `round_id`, голос `0/0.5/1` согласованы. `readiness?assignment_id=…` привязан к назначению. |
| Текущий результат и история | `GET /results/{account_id}` → `{current,history}` | Эффективная оценка сохраняет явный ноль. Незавершённая пересдача оставляет последний завершённый результат; апелляция меняет effective grade. Студенту не выдаются calculated grade и result CAS version. |
| Административный статус пересдачи | `/results`, `/assignments`, `/attempts` | UI соединяет коллекции по `student.account_id`; `retakeStatus` берётся из назначения второй попытки, а текущая оценка — из результата. |
| Протокол | `/attempts/{id}/protocol`, `/questions/{id}/rounds` | Стабильные границы страниц `through_sequence_no` и `through_round_no`; student protocol не раскрывает индивидуальные голоса. |
| Внешний результат | `/outside-results`, `/metadata` | UI alias `/outside-lms/results` переводит PATCH в PUT. Баллы и оценка независимы; дата внешнего экзамена меняется через metadata. |
| Подготовка к экзамену | `/training`, `/training/settings`, `/training/progress/{question_id}` | Владелец Exams. Контекст — организация, подразделение, экзамен, account. У карточки `card_ref=exam:{exam_id}:{question_id}`, для отметки обязателен content fingerprint. |
| Обычные карточки | `/v1/cards/*` | Владелец Cards. Содержание и ручной прогресс имеют контекст организации; переключение подразделения не должно сбрасывать этот прогресс. Экзаменационные карточки нельзя записывать через Cards API. |

## Точные места для BFF/UI

1. Добавить `GET {exam}/imports/{questions|assignments|outside}/template` в allowlist BFF. Владелец возвращает XLSX и проверяет Exams view + Upload view; template не содержит реальных пользователей. Общий Uploads уже обращается к этому owner route.
2. При сборке всех страниц import preview зафиксировать первую `revision` и отправлять `expected_revision` на следующих GET. Владелец отвечает `409 preview_modified`, если пользователь редактировал другой вкладкой. Native UI уже собирает все страницы; общему Uploads adapter тоже нужен полный preview, а не первая страница.
3. Каталог UI пока запрашивает все детали и сам фильтрует даты. Передать `date_from/date_to` (либо совместимые `from_date/to_date`) владельцу до пагинации. Дата classic — текущий start выбранного подразделения в UTC+03; outside — календарная date. Даты включительны. Неправильный период — 400 даже для пустого каталога. Историческое `exam_window_snapshot` остаётся отдельным полем.
4. `cardsAdminTree` читает `training_enabled` из catalogue summary. Если summary не отдаёт это поле, экран показывает подготовку выключенной даже после переключения. Владелец добавляет состояние выбранного подразделения; UI должен использовать его, либо уже полученный training preview.
5. `ownerAssignment` читает `privilege_revision`; при отсутствии поля получает 0. Владелец добавляет текущую версию персональной привилегии. Отдельная коллекция `/privileges` уже содержит правильную revision.
6. Student result adapter знает завершённые current/history и `hasPreviousAttempt` из номера текущей попытки. Владелец теперь добавляет безопасные `has_active_retake` и `retake_status` (`null/not_started/pending_ready/in_progress/completed`); pending пересдача сохраняет опубликованный current result. Student UI может использовать эти поля для отдельного индикатора. Административный экран уже получает статус из assignments.

`canManageContent` в UI выводит разрешение из наличия подключённых подразделений в identity. Это не полная проверка edit permission каждого подразделения; окончательная проверка shared content остаётся у владельца. Не считать доступ к списку подразделений гарантией права изменять общий банк.

## Новая проверка

`scripts/http_audit.py` использует только loopback HTTP и отдельную `cpm_dev`. Создаёт собственные синтетические подразделения в заранее посеянных организациях 1001 и 2001; cleanup проверяет конкретные IDs, организацию и префикс текущего прогона. BFF использует отдельные cookie jars, токены и профили не выводятся.

Покрытие: двусторонняя изоляция read/write/protocol/каталога, чужие student references и copy/connect targets; shared training progress по подразделениям; completed result + appeal + pending retake; live изменение scoped роли и повтор старого idempotency receipt после отзыва edit; BFF смена контекста/устаревшая context revision; отзыв текущего membership; XLSX templates; 251 или 5000 строк preview, все страницы, CAS и source hash, исключение ошибки вне первой страницы, атомарный commit и восстановление потерянного ответа; московская полночь и включительный `as_of` rating snapshot.

Запуск после подтверждения готовности runtime:

```sh
dev/.venv/bin/python services/exams/scripts/http_audit.py --import-rows 251
dev/.venv/bin/python services/exams/scripts/http_audit.py --import-rows 5000
```

Для полного baseline и расширения: `scripts/regression.py --http --audit-http`. Проверки не являются нагрузочной гарантией и не заменяют BFF/browser UI проверки владельца интерфейса.

## Подтверждённые результаты

| Прогон | Результат | Артефакт в `dev/local` |
| --- | --- | --- |
| Baseline Docker Exams/Core8003 | 1462 проверки, PASS. Concurrent read smoke: 450 запросов, 32 workers, 0.31s, p95 38.71ms, p99 46.13ms. | `exams-http-report.json` |
| 5000 строк импорта, две организации | 10510 проверок, PASS, 12.227s. Все 25 страниц проверены, CAS/atomic commit/recovery подтверждены. | `exams-audit-http-report-761b54589aca.json` |
| Финальный 251-row аудит после исправления DELETE race | 1033 проверки, PASS, 5.196s. 8 пар одновременно одинаковых DELETE + replay после физического удаления. | `exams-audit-http-report-b21e44c6d128.json` |
| Короткая проверка каталога после финального review | 153 проверки, PASS, 1.794s. Student/examiner не получают bank metadata; direction sort использует scoped имя, стабильный ID и exam-name fallback, до пагинации. | `exams-audit-http-report-a44744b8b0ae.json` |

В финальном прогоне 33 собственных scoped события записаны в outbox, все 33 опубликованы и все 33 наблюдались в `ratings_source_events_v1`. Это подтверждает доставку события; создание jobs и freshness вычисленного рейтинга остаются предметной проверкой Ratings. Синтетические Exams/Core/Auth данные и собственный outbox удалены после захвата доказательств. Данные dedup/watermark Ratings не изменялись; точные IDs переданы владельцу Ratings в `exams-audit-fixtures-{run}.json` для его узкой очистки.

Схемы OpenAPI проверены разрешением всех ссылок, sweep аутентификации зарегистрированных маршрутов, соответствием реальных DTO объявленным полям, bounded preview и бинарным XLSX response. Владелец обновил summary даты/training и assignment privilege revision; BFF/UI изменения описанные выше принимает его отдельный чат.

Последний `--catalog-only` создаёт только собственные три направления и шесть экзаменов/назначений. Порядок создания направлений намеренно противоположен их именам; архивирование третьего собственного направления проверяет fallback к имени экзамена. Для администратора, ученика и экзаменатора проверены три страницы по две записи и совпадение с полной выборкой. `connected_division_ids`, `owner_division_id`, `origin_exam_id`, `in_bank` остаются только в административном DTO; OpenAPI описывает их как необязательные административные поля. Все фикстуры этого короткого прогона очищены. Доказательство 33 событий выше относится к сохранённому прогону `b21e44c6d128`, а не к повтору всего audit suite.
