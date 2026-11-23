# Проект: pgsight — визуализатор EXPLAIN-планов PostgreSQL в терминале

## Цель
CLI/TUI-утилита на Go, которая читает готовый план выполнения запроса PostgreSQL
(вывод EXPLAIN или EXPLAIN ANALYZE) и показывает его в виде интерактивного дерева
с цветами, процентами времени и подсказками по оптимизации.
Утилита НЕ подключается к базе и НЕ выполняет запросы — только читает текст плана.
Проект для open source и портфолио: важны чистый код, тесты и отличный README.

Позиционирование: веб-аналоги (explain.depesz.com, explain.dalibo.com, pgMustard)
требуют вставить план на сторонний сайт, а в планах есть имена таблиц, колонок и литералы.
pgsight работает локально и без переключения из терминала.
Ближайший проект по стеку для изучения крайних случаев: github.com/bright98/pgexplain.

## Стек
- Go: в go.mod — актуальная стабильная версия (локальный Go 1.21+ скачает toolchain сам через GOTOOLCHAIN=auto)
- github.com/charmbracelet/bubbletea, lipgloss, bubbles — TUI, закрепить на v1 (v2 меняет API)
- github.com/spf13/cobra — CLI
- github.com/mattn/go-isatty (или golang.org/x/term) — определение TTY

## Использование
```
pgsight plan.txt                                   # план из файла
psql -c "EXPLAIN ANALYZE SELECT ..." | pgsight     # план из stdin
pgsight --plain plan.txt                           # без интерактивности (для CI и пайпов)
pgsight --plain --fail-on=warning plan.txt         # ненулевой код выхода, если сработали эвристики
```

### Поведение ввода/вывода
- Если stdout не терминал (`pgsight plan.txt > out.txt`, `| less`) — автоматически `--plain`.
- Если план пришёл через stdin, TUI читает клавиатуру из `/dev/tty` (Windows: `CONIN$`)
  через `tea.WithInput`. Если терминала нет вообще — fallback на `--plain`.
- Флаг `--no-color` и учёт переменной окружения `NO_COLOR`.
- Коды выхода: 0 — успех, 1 — ошибка ввода/парсинга, 2 — сработали эвристики при `--fail-on`.

## Входные форматы
Формат определяется автоматически после снятия обёртки psql.

### Предобработка (общая для обоих форматов)
Вывод psql в aligned-режиме нужно очистить до определения формата:
- заголовок `QUERY PLAN` и разделитель из дефисов;
- строка `(N rows)` / `(1 row)` в конце;
- `+` в конце строк (в JSON из psql так выглядит каждая строка);
- общий ведущий отступ (при копировании из psql — один пробел), `\r\n`.
После этого: начинается с `[` или `{` → JSON, иначе текст.

### 1. Текстовый формат — основной
- Вложенность задают отступы и стрелки `->`.
- Строки-узлы без стрелки, открывающие поддеревья: `InitPlan N (returns $0)`, `SubPlan N`, `CTE name`.
  Узлы под ними — дети ближайшего узла уровнем выше, но помечаются ролью (InitPlan / SubPlan / CTE).
- Строки-свойства узла: `Filter:`, `Index Cond:`, `Rows Removed by Filter:`, `Sort Key:`, `Sort Method:`,
  `Buckets: ... Batches: ... Memory Usage:`, `Buffers:`, `Workers Planned/Launched:`,
  `Worker N: actual time=...`, `Heap Blocks:` и т.д. Неизвестные свойства сохраняются как есть
  (для панели деталей), а не роняют парсер.
- Секции после дерева: `Planning:` (с PG13, внутри может быть `Buffers`), `Planning Time`,
  `Trigger ...`, `JIT:` (с вложенными строками), `Execution Time`.
- Несколько планов подряд во вводе — в MVP берём первый и выдаём предупреждение.

### 2. JSON-формат
`EXPLAIN (FORMAT JSON)` — массив из одного объекта с `Plan`, `Planning Time`, `Execution Time`, `Triggers`, `JIT`.

### Оба формата
Поддержать планы с ANALYZE (есть actual time) и без него (только cost и оценки строк),
с BUFFERS и без. Целевые версии PostgreSQL: 12–17.

## Функциональность MVP

### Модель узла
Тип узла, роль (обычный / InitPlan / SubPlan / CTE), таблица, алиас, индекс, cost (startup..total),
ожидаемые строки, ширина, фактическое время (startup..total), фактические строки, loops,
Filter, Rows Removed by Filter, Index Cond, Sort Key, Sort Method и Space, Buckets/Batches/Memory Usage,
Buffers (shared hit/read/dirtied/written, temp read/written), Workers Planned/Launched,
прочие свойства как список ключ–значение.

### Расчёт времени
- `actual time` и `actual rows` в плане — средние за один loop.
  Полное время узла = actual total time × loops, полное число строк = actual rows × loops.
- Exclusive time = полное время узла − сумма полного времени прямых детей. Результат ограничивать снизу нулём.
- Параллельные узлы (под Gather / Gather Merge, loops = воркеры + лидер): время параллельно,
  поэтому полное время делить на loops, а не умножать. Отдельные тесты.
- CTE Scan и InitPlan: не считать время CTE дважды. Отдельные тесты.
- База для 100%: `Execution Time`, если есть, иначе total time корня.
- Без ANALYZE: те же формулы на total cost (без умножения на loops), база — total cost корня.
  Ограничение (описать в README): у Nested Loop стоимость внутренней стороны уже учтена с умножением,
  поэтому exclusive cost бывает неинтуитивным — не «чиним».

### Отображение
- Дерево: тип узла, таблица/индекс, время (или cost), строки (факт/оценка), полоса процента.
- Цвета считаются по exclusive-доле: зелёный < 10%, жёлтый 10–30%, красный > 30%.
- Узлы с подсказками помечаются 💡.
- В шапке: общее время выполнения и планирования (если есть), режим (ANALYZE / только оценки).

### Навигация в TUI
- ↑↓ (и j/k) — перемещение
- ←→ или пробел — свернуть/развернуть узел
- Enter — панель с деталями узла и его подсказками
- `?` — справка по клавишам, `q` / `Ctrl+C` — выход
- Длинные деревья скроллятся, текущий узел всегда виден; корректная перерисовка при изменении размера окна.

## Эвристики (подсказки 💡)
Каждая эвристика — отдельная функция с тестами. У подсказки есть уровень (info / warning),
узел и текст. Пороги — именованные константы. Эвристики, которым нужны фактические данные,
работают только для планов с ANALYZE.

| Эвристика | Условие | Подсказка |
|---|---|---|
| Seq Scan по большой таблице с фильтром | Seq Scan с Filter и прочитано (rows + removed) × loops > 100 000 | возможно, нужен индекс по колонкам фильтра |
| Ошибка оценки строк | max(actual, estimated) ≥ 100 и actual/estimated > 10 или < 0.1 (actual = 0 обрабатывать отдельно) | устаревшая статистика — выполнить ANALYZE таблицы |
| Сортировка на диск | Sort Method: external merge / external sort | увеличить work_mem |
| Много итераций Nested Loop | у внутреннего дочернего узла Nested Loop loops > 1000 | возможно, лучше Hash Join или нужен индекс на внутренней стороне |
| Неэффективный фильтр | Rows Removed by Filter > 10 × actual rows и removed × loops > 10 000 | фильтр отбрасывает почти всё — возможно, нужен индекс |
| Hash не влез в память | Hash с Batches > 1 | увеличить work_mem |

Кандидаты на после MVP: Rows Removed by Index Recheck / lossy Heap Blocks в Bitmap Heap Scan,
высокий `Buffers: read` при малом `hit`, Workers Launched < Workers Planned.

## Структура проекта
```
cmd/pgsight/       — точка входа, cobra-команды
internal/plan/     — модель дерева, предобработка ввода, парсеры текста и JSON, расчёт exclusive time
internal/analyze/  — эвристики и подсказки
internal/tui/      — интерфейс Bubble Tea
internal/render/   — plain-вывод без TUI
testdata/          — реальные планы в обоих форматах, с ANALYZE и без
```

## testdata
- Снимать с реальной базы (локально есть PostgreSQL 15), плюс несколько планов из PG 16/17.
- Покрыть: простой Seq Scan; Index/Bitmap Scan; Nested Loop / Hash Join / Merge Join;
  Sort в памяти и на диске; Hash с Batches > 1; параллельный план (Gather);
  InitPlan, SubPlan, CTE; Planning с Buffers; JIT; триггеры; вывод psql с обёрткой и без;
  JSON из psql с `+`; мусорный ввод.
- Golden-тесты: для каждого плана — ожидаемый plain-вывод (`-update` флаг для перегенерации).

## Требования к качеству
- Unit-тесты для обоих парсеров, расчёта времени и каждой эвристики на файлах из testdata/.
- Парсер не падает на неожиданном вводе — понятная ошибка с номером строки. Fuzz-тест парсера (`go test -fuzz`).
- golangci-lint без ошибок.
- GitHub Actions: тесты и линтер на каждый push и PR (Linux, macOS, Windows).
- GoReleaser: бинарники под Linux, macOS, Windows (amd64, arm64), Homebrew tap — по желанию.
- Установка: `go install github.com/adam-kad/pgsight/cmd/pgsight@latest`
- Лицензия MIT.

## README
- Основной README на английском, перевод — `README.ru.md`.
- Первая строка: «Readable PostgreSQL EXPLAIN plans, right in your terminal»
  (ru: «Понятные EXPLAIN-планы PostgreSQL прямо в терминале»).
- GIF-демонстрация сразу под заголовком (записать через charmbracelet/vhs, `.tape` хранить в репозитории).
- Установка одной командой.
- Примеры: файл, пайп из psql, `--plain`, `--fail-on` в CI.
- Список эвристик с порогами.
- Подчеркнуть: работает локально, ничего не отправляет и не подключается к базе.
- Ограничения (exclusive cost у Nested Loop без ANALYZE, один план за раз).
- Раздел «Contributing» (как добавить эвристику и пример в testdata).

## Не делать в MVP
- Подключение к базе и выполнение запросов
- Поддержку MySQL и других баз
- Сравнение двух планов
- Веб-интерфейс
- Форматы YAML и XML

## Порядок работы
1. Модель дерева + предобработка ввода + парсер JSON + тесты
2. Парсер текстового формата + тесты
3. Расчёт exclusive time и процентов (включая параллельные узлы и CTE)
4. Plain-вывод (`--plain`, `--fail-on`, `--no-color`) + golden-тесты
5. Эвристики + тесты
6. TUI (включая чтение клавиатуры из /dev/tty)
7. CI, README, GIF, релиз
После каждого шага проект собирается и тесты проходят.
