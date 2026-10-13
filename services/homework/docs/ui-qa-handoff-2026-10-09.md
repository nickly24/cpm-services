# Homework / File-work: передача UI QA 9 октября 2026

Предметная реализация и адресные проверки владельца завершены. Сейчас разрешена
подготовка QA: чтение исходников и составление матриц. Новые UI/HTTP прогоны,
фикстуры, изменения настроек и сбои инфраструктуры — после отдельного QA RELEASE
координатора по `outputs/microservices/ui-qa-coordination.md`.

## Исходники и работающая сборка

Основной контракт и описание: `services/homework/README.md`,
`services/homework/internal/homework/openapi.json`,
`services/homework/docs/handoff-2026-10-09.md`.
Трасса предметного процесса: `internal/homework/catalog.go`, `workflow.go`,
`review.go`, `worker_health.go`; обработчик:
`services/file-work-service/internal/filework/` и его README/handoff.
Первоначальный план: `outputs/microservices/homework-service-plan.md`.

Контейнеры на момент передачи running/healthy; это чтение Docker metadata,
новый HTTP прогон не запускался:

| Сервис | Порт | Docker image ID |
|---|---|---|
| Homework | 8006 | sha256:61f249351a52093f8da9e7cfaffc4e452026e71689894979c042c6c93dbd41d8 |
| File-work | 8007 | sha256:b166a1f03d794f50e29c423055f446ec9b0f5e182fe91c2d844a6dd30924a52d |

Контракт Homework v1.1.0. Отпечатки текущих исходников: для отсортированных
относительных путей `.go`, `go.mod`, `go.sum`, `openapi.json`, `Dockerfile` SHA256
от последовательности `path UTF-8 + NUL + SHA256(file bytes)`:

- Homework, 23 файла: `cdc22171e994ee6e56efdddaabe9a3608be3fac32fa11a11b9894644d9aba65c`.
- File-work, 19 файлов: `3db07edd9c818a40507d3782b0d9e3f35362ee14a706dff6b347f43e89027b4e`.

Dev зависимости: MySQL13306/cpm_dev, Auth8002, Core8003, общий RabbitMQ,
локальная S3-фикстура MinIO19000/cpm-homework-test. Внутренние ключи Homework
и Core разделены. Значения ключей, env и подписанные URL в отчёт не включать.
Новых compile flags для этой доработки нет. Текущие BFF/UI сборки и готовность
экранов подтверждает их владелец до общего release.

## Доказательства владельца

9 октября: `audit_test.py` — 459 успешных утверждений, оба сервиса
`go test -race ./...` и `go vet ./...` — PASS; стандартные Docker builds — PASS.
Проверен реальный двухстраничный PDF: signed POST → Rabbit → worker → S3 →
Homework DB → сдача → проверка → оценка → скачивание.
Проверены изоляция org/div, класс из Core, неизменный снимок лимитов,
разделение внутренних ключей и свежий мониторинг с реальной S3 пробой.
Это проверки сервисов, не прохождение UI QA.

Совместно с Uploads проверены регистрация своей работы и точный повтор,
metadata/receipt, запрет скачивания через registry, запрет чужих student/admin/
org/div и отсутствие утечки private homework в jobs/events/sessions.
Это адресный Homework subset, не весь Uploads.

Первоначальные HTTP/workflow/maintenance/group-move проверки дали 1179
утверждений 7 октября. Они исторические, а не повторный прогон текущего UI.
Все собственные фикстуры аудита и совместного Uploads теста удалены;
настройки восстановлены. Активных тестов, fault windows и ожидаемых
предметных исправлений Homework/File-work нет.

## Обязательные UI сценарии после release

| Область | Что проверить через UI | QA владелец |
|---|---|---|
| Каталог | Создание/переименование/архив типа отдельно в подразделениях; задания, публикация, фильтры, редактирование; чужие IDs и смена кабинета | learning |
| Загрузка PDF | Создание, реальная загрузка, очередь, обработка, черновик, cancel/retry, обновление страницы и повтор действия | learning |
| Проверка | Сдать, claim/release/takeover, доработка, повторная сдача, автооценка/0/ручная оценка/edit-grade/resubmit, история и download privacy | learning |
| Отчёт | `class` integer/null, результаты null/0, периоды/type/group/account, видимая таблица/фильтр класса и XLSX | learning |
| Лимиты | Текущие workspace/settings и принятый `job.limits`, сообщения исходного/конечного размера и страниц; изменение settings только в согласованном scope | learning |
| Мониторинг | Worker/storage unknown/stale/error/ok, processing_ready, права monitoring.view и scope счётчиков; fault только в согласованном окне | learning + imports по согласованию |
| Registry | Своя Homework-ссылка, повтор регистрации, metadata/receipt, чужие IDs, невозможность второго upload/download процесса | imports |
| Восстановление | Дубликаты/lost reply/queue/retry с доказательствами job/command; общие Rabbit/S3 сбои только в согласованном окне | learning + imports по согласованию |

Бизнесовая модель сохраняется: тип и задание принадлежат org/div, обработанный
файл сначала приватный черновик; сдача — отдельное действие. Worker возвращает
метаданные результата через Rabbit, Homework проверяет S3 и записывает
предметные данные. Байты файла идут через S3. Настройки находятся в существующей
`application_settings`, ключ `homework.{division}.limits`; уже начатые задачи
сохраняют снимок. Подробные DTO и error codes — в основном handoff/OpenAPI.

QA пишет только свои планы/скрипты/отчёты. Баги Homework/File-work передавать
владельцу `01a11614-37af-7170-9d90-9e517638b4c8` и координатору; UI/BFF/Core
баги — BFF владельцу. После исправления нужен свежий UI retest на новой сборке.
Нагрузка 450 пользователей, production, CI/CD и новая Auth background apply
граница не проверены и этим handoff не разрешаются. Общий QA RELEASE не выдан
этим документом.
