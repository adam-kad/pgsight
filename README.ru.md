# pgsight

**Понятные EXPLAIN-планы PostgreSQL прямо в терминале.**

[![CI](https://github.com/adam-kad/pgsight/actions/workflows/ci.yml/badge.svg)](https://github.com/adam-kad/pgsight/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/adam-kad/pgsight)](https://github.com/adam-kad/pgsight/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

![Демонстрация pgsight](docs/demo.gif)

[English version](README.md)

pgsight берёт вывод `EXPLAIN` или `EXPLAIN ANALYZE` и превращает его в
интерактивное дерево. Для каждого узла видно собственное время с цветом по доле
от всего запроса, а подсказки указывают на вероятные проблемы: нет индекса,
устарела статистика, сортировка или хэш ушли на диск.

> **Работает локально.** pgsight читает только переданный ему текст плана. Он не
> подключается к базе и ничего не отправляет по сети, поэтому планы с именами
> таблиц, колонок и значениями из запроса остаются на вашей машине.

## Установка

Через Go 1.27 или новее:

```bash
go install github.com/adam-kad/pgsight/cmd/pgsight@latest
```

Или скачайте готовый бинарник для Linux, macOS или Windows со
[страницы релизов](https://github.com/adam-kad/pgsight/releases).

## Использование

```bash
# План из файла
pgsight plan.txt

# Сразу из psql
psql -c "EXPLAIN (ANALYZE, BUFFERS) SELECT ..." mydb | pgsight

# Вывести один раз, без интерактивного режима (для пайпов и CI)
pgsight --plain plan.txt

# Уронить CI-задачу, если найдено предупреждение (код выхода 2)
pgsight --plain --fail-on=warning plan.json
```

Формат определяется автоматически. Поддерживаются:

- **текстовый** формат (обычный вывод `EXPLAIN`) и **JSON** (`EXPLAIN (FORMAT JSON)`);
- вывод psql с заголовком `QUERY PLAN`, строкой `(N rows)` и `+` в конце строк,
  а также «голый» план, скопированный из GUI-клиента;
- планы с `ANALYZE` (реальное время) и без него (только оценки); также
  понимаются `BUFFERS`, `VERBOSE`, `SETTINGS`, `COSTS OFF` и `TIMING OFF`.

Больше всего пользы даёт `EXPLAIN (ANALYZE, BUFFERS)`. Помните, что `ANALYZE`
действительно выполняет запрос: изменяющие данные запросы оборачивайте в
`BEGIN; ... ROLLBACK;`.

### Параметры

| Флаг | Описание |
|---|---|
| `-p`, `--plain` | Вывести план один раз. Включается сам, если вывод идёт не в терминал. |
| `--no-color` | Без цветов. Переменная окружения `NO_COLOR` тоже учитывается. |
| `--fail-on` | `none` (по умолчанию), `info` или `warning`: код выхода 2, если есть подсказка такого уровня или выше. |
| `--version` | Показать версию. |

Коды выхода: `0` — успех, `1` — ввод не удалось прочитать или разобрать,
`2` — найдены подсказки уровня `--fail-on`.

### Клавиши

| Клавиша | Действие |
|---|---|
| `↑` `↓` / `k` `j` | Перемещение по узлам |
| `←` / `h` | Свернуть узел или перейти к родителю |
| `→` / `l` | Развернуть узел или перейти к первому потомку |
| `пробел` | Свернуть или развернуть |
| `enter` | Показать или скрыть панель с деталями узла |
| `n` | Следующий узел с подсказкой |
| `g` `G`, `PgUp` `PgDn` | В начало, в конец, на страницу вверх и вниз |
| `?` | Все клавиши |
| `esc` | Закрыть панель |
| `q` | Выход |

## Как читать вывод

```
EXPLAIN ANALYZE · text format
Execution 52.8 ms · Planning 0.185 ms
💡 2 warnings

     SELF      % SHARE       NODE
  22.8 ms  43.2% ████▍       Hash Join  107k rows (est 108k)
  20.7 ms  39.2% ███▉        ├─ Seq Scan on orders o 💡  107k rows (est 108k)
  4.05 ms   7.7% ▊           └─ Hash 💡  50k rows (est 75k)
  2.96 ms   5.6% ▌              └─ Seq Scan on customers c  50k rows (est 75k)

Hints
  [warning] Seq Scan on orders o
            Seq Scan on orders read 400,000 rows and kept 107,198 (27%)
            → An index on the filtered columns may let PostgreSQL skip the rest: (created_at > '2023-01-01'::date)
  [warning] Hash
            Hash table was split into 8 batches and spilled to disk
            → Increase work_mem so the hash table fits in memory (it used 375 kB per batch).
```

- **SELF** — собственное (*exclusive*) время узла, без учёта потомков.
  PostgreSQL печатает средние значения за один loop, поэтому pgsight умножает
  их на `loops` и вычитает время детей.
- **%** и **SHARE** — доля SELF от общего `Execution Time`. Цвета: зелёный
  до 10%, жёлтый от 10% до 30%, красный больше 30%.
- **rows** — фактические строки за один loop; в скобках оценка планировщика,
  если она отличается, и `×N` — число loops.
- 💡 отмечает узлы с подсказками. Панель деталей (`enter`) показывает всё, что
  PostgreSQL сообщил об узле: условия, буферы, детали сортировки и хэша.

Чтобы цифры соответствовали реальности, учтены особенности:

- Узлы под `Gather` выполняются в нескольких процессах одновременно; их время
  делится на число процессов, чтобы параллельная работа не считалась дважды.
- CTE выполняется тем `CTE Scan`, который его читает, поэтому время CTE
  вычитается из этого скана, а не из узла, к которому CTE прикреплён.
- Без `ANALYZE` (или с `TIMING OFF`) времени нет, и доли считаются по cost
  планировщика.

## Подсказки

Каждая подсказка — отдельное правило в [`internal/analyze`](internal/analyze/analyze.go)
со своими тестами. Всем правилам нужны данные `EXPLAIN ANALYZE`.

| Правило | Когда срабатывает | Что предлагает |
|---|---|---|
| `seq-scan-filter` | Seq Scan с фильтром читает больше 100 000 строк и отбрасывает большую часть | Индекс по колонкам фильтра |
| `row-estimate` | Фактическое число строк отличается от оценки больше чем в 10 раз (и одно из чисел не меньше 100) | `ANALYZE` таблицы; `CREATE STATISTICS` для коррелированных колонок |
| `sort-disk` | `Sort Method: external merge` — сортировка ушла на диск | Увеличить `work_mem` или добавить индекс с нужным порядком |
| `nested-loop` | Внутренняя сторона Nested Loop выполняется больше 1000 раз | Проверить внутреннюю сторону и оценку строк внешней |
| `inefficient-filter` | Фильтр отбрасывает в 10+ раз больше строк, чем оставляет (всего от 10 000 строк) | Индекс под фильтр |
| `hash-batches` | Hash разбит больше чем на один batch | Увеличить `work_mem` |

Чтобы подсказок было немного и по делу:

- ошибка оценки показывается только на узле, где она возникла, а не на каждом
  узле выше; «строк меньше, чем ожидалось» под `LIMIT` не считается ошибкой —
  там выполнение намеренно останавливается раньше;
- для узлов, которые занимают меньше 10% времени, часть подсказок понижается до `info`.

## Ограничения

- Один план за раз: если во вводе их несколько, показывается первый.
- Без `ANALYZE` собственный cost узла приблизителен. Стоимости планировщика не
  складываются так, как время (например, cost Nested Loop уже включает
  внутреннюю сторону, умноженную на ожидаемое число loops).
- Форматы YAML и XML не поддерживаются.

## Участие в разработке

Issues и pull requests приветствуются. Нужен Go 1.27+.

```bash
go test ./...                       # unit- и golden-тесты
go test -race ./...
golangci-lint run                   # линтер, конфиг в .golangci.yml
go test -run='^$' -fuzz=FuzzParse -fuzztime=1m ./internal/plan
```

Структура проекта:

```
cmd/pgsight/       точка входа, CLI (cobra)
internal/plan/     модель плана, парсеры текста и JSON, exclusive time
internal/analyze/  подсказки
internal/render/   plain-вывод и общее форматирование
internal/tui/      интерактивный режим (Bubble Tea)
testdata/          реальные планы в обоих форматах и golden-файлы
```

**Примеры планов.** Все планы в `testdata/` сняты с настоящего PostgreSQL
скриптом [`testdata/gen/generate.sql`](testdata/gen/generate.sql). Скрипт
работает внутри транзакции, которая откатывается, и не меняет базу:

```bash
cd testdata && psql -X -d postgres -f gen/generate.sql
```

Каждый сценарий сохраняется в текстовом и JSON-формате, и тесты проверяют, что
оба дают одинаковое дерево. После изменения plain-вывода обновите golden-файлы
и просмотрите diff:

```bash
go test ./internal/render -update
```

**Новая подсказка.** Напишите функцию проверки в `internal/analyze/analyze.go`,
добавьте её в `Rules`, покройте тестами в `analyze_test.go` на плане из
`testdata/` (если подходящего нет — добавьте сценарий в `generate.sql`) и на
паре граничных случаев, и внесите в таблицу выше.

**Демо-GIF.** `docs/demo.gif` записывается через [vhs](https://github.com/charmbracelet/vhs):
`vhs docs/demo.tape`.

## Лицензия

[MIT](LICENSE)
