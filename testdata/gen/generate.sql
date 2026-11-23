-- Generates the plans in testdata/ from a live PostgreSQL server.
-- Everything runs inside one transaction that is rolled back, so the
-- database is left untouched.
--
--   cd testdata && psql -X -d postgres -f gen/generate.sql
\set ON_ERROR_STOP on
\pset pager off

BEGIN;

CREATE TABLE customers (id int PRIMARY KEY, country text, created_at date);
CREATE TABLE orders (id int PRIMARY KEY, customer_id int, status text, amount numeric, created_at date);
CREATE INDEX orders_customer_id_idx ON orders (customer_id);
CREATE INDEX orders_status_idx ON orders (status);
CREATE TABLE audit_log (order_id int, changed_at timestamptz);
CREATE TABLE correlated (a int, b int, payload text);

INSERT INTO customers
SELECT g, (ARRAY['DE','FR','US','RU','BR'])[1 + g % 5], date '2020-01-01' + g % 1500
FROM generate_series(1, 50000) g;

INSERT INTO orders
SELECT g, 1 + (g::bigint * 7919) % 50000,
       CASE WHEN g % 100 = 0 THEN 'cancelled' WHEN g % 10 = 0 THEN 'pending' ELSE 'done' END,
       (g % 1000) / 10.0, date '2020-01-01' + g % 1500
FROM generate_series(1, 400000) g;

INSERT INTO correlated SELECT g % 100, g % 100, 'x' FROM generate_series(1, 100000) g;
ANALYZE correlated;

-- Deliberately stale statistics on customers: analyze before a skewed update.
ANALYZE orders;
ANALYZE customers;
UPDATE customers SET country = 'DE' WHERE id % 2 = 0;

CREATE FUNCTION audit_order() RETURNS trigger LANGUAGE plpgsql AS
$$ BEGIN INSERT INTO audit_log VALUES (NEW.id, now()); RETURN NEW; END $$;
CREATE TRIGGER orders_audit AFTER UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION audit_order();

SET max_parallel_workers_per_gather = 0;

-- 1. Seq Scan with a selective filter on a large table.
\o seqscan_filter.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM orders WHERE amount = 42.5;
\o seqscan_filter.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT * FROM orders WHERE amount = 42.5;

-- 2. Hash join whose hash spills into several batches.
SET work_mem = '256kB';
\o hash_batches.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT c.country, o.amount FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.created_at > '2023-01-01';
\o hash_batches.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT c.country, o.amount FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.created_at > '2023-01-01';

-- 3. Sort that goes to disk.
\o sort_disk.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM orders ORDER BY amount DESC, created_at;
\o sort_disk.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT * FROM orders ORDER BY amount DESC, created_at;
RESET work_mem;

-- 4. Nested loop with many inner index scans.
SET enable_hashjoin = off;
SET enable_mergejoin = off;
\o nested_loop.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT c.id, o.amount FROM customers c JOIN orders o ON o.customer_id = c.id WHERE c.country = 'FR';
\o nested_loop.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT c.id, o.amount FROM customers c JOIN orders o ON o.customer_id = c.id WHERE c.country = 'FR';
RESET enable_hashjoin;
RESET enable_mergejoin;

-- 5. Bitmap heap scan + stale estimate on customers.country.
\o bitmap_misestimate.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT count(*) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.status = 'cancelled' AND c.country = 'DE';
\o bitmap_misestimate.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT count(*) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.status = 'cancelled' AND c.country = 'DE';

-- 6. CTE, InitPlan and correlated SubPlan.
\o cte_subplans.analyze.txt
EXPLAIN (ANALYZE, BUFFERS)
WITH big AS MATERIALIZED (SELECT customer_id, sum(amount) AS total FROM orders GROUP BY customer_id)
SELECT c.id, b.total,
       (SELECT max(amount) FROM orders) AS global_max,
       (SELECT count(*) FROM orders o2 WHERE o2.customer_id = c.id AND o2.status = 'pending') AS pending
FROM customers c JOIN big b ON b.customer_id = c.id
WHERE c.id < 200;
\o cte_subplans.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
WITH big AS MATERIALIZED (SELECT customer_id, sum(amount) AS total FROM orders GROUP BY customer_id)
SELECT c.id, b.total,
       (SELECT max(amount) FROM orders) AS global_max,
       (SELECT count(*) FROM orders o2 WHERE o2.customer_id = c.id AND o2.status = 'pending') AS pending
FROM customers c JOIN big b ON b.customer_id = c.id
WHERE c.id < 200;

-- 7. Parallel aggregate.
SET max_parallel_workers_per_gather = 2;
SET parallel_setup_cost = 0;
SET parallel_tuple_cost = 0;
\o parallel_agg.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT status, count(*), avg(amount) FROM orders WHERE amount > 10 GROUP BY status;
\o parallel_agg.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT status, count(*), avg(amount) FROM orders WHERE amount > 10 GROUP BY status;
SET max_parallel_workers_per_gather = 0;

-- 8. UPDATE with a row trigger.
\o update_trigger.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) UPDATE orders SET status = 'done' WHERE id <= 500;
\o update_trigger.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) UPDATE orders SET status = 'done' WHERE id <= 500;

-- 9. Plans without ANALYZE (estimates only).
\o join_estimate.plan.txt
EXPLAIN SELECT c.country, sum(o.amount) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.status <> 'cancelled' GROUP BY c.country ORDER BY 2 DESC;
\o join_estimate.plan.json
EXPLAIN (FORMAT JSON) SELECT c.country, sum(o.amount) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.status <> 'cancelled' GROUP BY c.country ORDER BY 2 DESC;

-- 10. Correlated columns: the planner multiplies selectivities and
--     underestimates the row count by ~100x.
\o misestimate.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM correlated WHERE a = 7 AND b = 7;
\o misestimate.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT * FROM correlated WHERE a = 7 AND b = 7;

-- 11. Inner side of a nested loop that never runs.
SET enable_hashjoin = off;
SET enable_mergejoin = off;
\o never_executed.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM customers c JOIN orders o ON o.customer_id = c.id WHERE c.country = 'XX';
\o never_executed.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT * FROM customers c JOIN orders o ON o.customer_id = c.id WHERE c.country = 'XX';
RESET enable_hashjoin;
RESET enable_mergejoin;

-- 12. LIMIT stops its child early: estimates below it look wrong but are fine.
\o limit.analyze.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM orders WHERE status = 'done' ORDER BY id LIMIT 10;
\o limit.analyze.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT * FROM orders WHERE status = 'done' ORDER BY id LIMIT 10;

-- 13. VERBOSE + SETTINGS: schema-qualified names, Output lines, Settings.
SET random_page_cost = 1.1;
\o verbose.analyze.txt
EXPLAIN (ANALYZE, VERBOSE, BUFFERS, SETTINGS) SELECT c.country, count(*) FROM customers c WHERE c.id < 1000 GROUP BY c.country;
\o verbose.analyze.json
EXPLAIN (ANALYZE, VERBOSE, BUFFERS, SETTINGS, FORMAT JSON) SELECT c.country, count(*) FROM customers c WHERE c.id < 1000 GROUP BY c.country;
RESET random_page_cost;

-- 14. COSTS OFF (text only: the node lines carry no statistics).
\o join_estimate.costs_off.plan.txt
EXPLAIN (COSTS OFF) SELECT c.country, sum(o.amount) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.status <> 'cancelled' GROUP BY c.country ORDER BY 2 DESC;

-- 15. Bare output without the psql wrapper (as copied from many GUI clients).
\pset tuples_only on
\pset format unaligned
\o seqscan_filter.analyze.bare.txt
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM orders WHERE amount = 42.5;
\o seqscan_filter.analyze.bare.json
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT * FROM orders WHERE amount = 42.5;
\o

ROLLBACK;
