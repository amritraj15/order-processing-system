//go:build integration

package gorm_test

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	orm "gorm.io/gorm"

	database "order_management/db/gorm"
	"order_management/internal/testutil"
)

// backlogPrimaryKeySQL is a candidate rewrite of database.PendingBatchSQL.
//
// The selected IDs are collected into an array once and the UPDATE locates each
// row through the primary key (o.id = ANY(array)). The current statement joins
// the updated table to the batch CTE and leaves the join strategy to the
// planner, which can choose a scan over every PENDING row. MATERIALIZED makes
// the single evaluation of the FOR UPDATE SKIP LOCKED subquery explicit.
//
// This statement is used only by this comparison test. It is not wired into
// the application.
const backlogPrimaryKeySQL = `WITH batch AS MATERIALIZED (
    SELECT id FROM orders
    WHERE status = 'PENDING' AND created_at <= ?
    ORDER BY created_at, id
    LIMIT ? FOR UPDATE SKIP LOCKED
), updated AS (
    UPDATE orders AS o SET status = 'PROCESSING', updated_at = now()
    WHERE o.id = ANY(ARRAY(SELECT id FROM batch)) AND o.status = 'PENDING'
    RETURNING o.id
)
SELECT count(*) FROM updated`

var backlogExecutionTime = regexp.MustCompile(`Execution Time: ([0-9.]+) ms`)

type backlogResult struct {
	name        string
	batches     int
	startPlanMS float64
	latePlanMS  float64
	firstMS     float64
	lastMS      float64
	p50MS       float64
	p95MS       float64
	maxMS       float64
	totalS      float64
}

func backlogEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		t.Fatalf("%s must be a non-negative integer, got %q", key, raw)
	}
	return n
}

func backlogMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

func backlogMean(values []time.Duration) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum time.Duration
	for _, v := range values {
		sum += v
	}
	return backlogMS(sum) / float64(len(values))
}

// backlogExplain runs EXPLAIN (ANALYZE, BUFFERS) on the real statement inside a
// transaction and rolls it back, so the measured writes are not kept.
func backlogExplain(t *testing.T, db *orm.DB, statement string, cutoff time.Time, limit int) (string, float64) {
	t.Helper()
	var plan []struct {
		QueryPlan string `gorm:"column:QUERY PLAN"`
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := tx.Raw("EXPLAIN (ANALYZE, BUFFERS) "+statement, cutoff, limit).Scan(&plan).Error; err != nil {
		_ = tx.Rollback().Error
		t.Fatal(err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, len(plan))
	for _, line := range plan {
		lines = append(lines, line.QueryPlan)
	}
	text := strings.Join(lines, "\n")
	ms := -1.0
	if m := backlogExecutionTime.FindStringSubmatch(text); m != nil {
		if parsed, err := strconv.ParseFloat(m[1], 64); err == nil {
			ms = parsed
		}
	}
	return text, ms
}

// backlogShape lists the plan features that matter for this comparison.
func backlogShape(plan string) string {
	markers := []string{
		"orders_pending_idx",
		"orders_pkey",
		"orders_status_id_idx",
		"Seq Scan on orders",
		"Hash Join",
		"Nested Loop",
	}
	var found []string
	for _, marker := range markers {
		if strings.Contains(plan, marker) {
			found = append(found, marker)
		}
	}
	return strings.Join(found, ", ")
}

// backlogDrain seeds a fresh isolated schema with a large PENDING backlog plus
// historical rows, explains the first batch, then drains the whole backlog with
// committed batches (as the worker does) while timing every batch.
func backlogDrain(t *testing.T, name, statement string, rows, batch int) backlogResult {
	t.Helper()
	db := testutil.PostgreSQL(t)
	customer, _ := fixtures(t, db)

	var hasUUIDv7 bool
	if err := db.Raw("SELECT to_regprocedure('uuidv7()') IS NOT NULL").Scan(&hasUUIDv7).Error; err != nil {
		t.Fatal(err)
	}
	idExpr := "gen_random_uuid()"
	if hasUUIDv7 {
		idExpr = "uuidv7()"
	}
	t.Logf("[%s] seeding %d PENDING and %d DELIVERED rows (id expression: %s)", name, rows, rows/4, idExpr)

	history := rows / 4
	if err := db.Exec(`INSERT INTO orders (id,customer_id,status,currency,total_minor,created_at,pricing_mode)
        SELECT `+idExpr+`, ?, 'DELIVERED', 'USD', 100, now() - interval '1 day' + g * interval '1 millisecond', 'legacy'
        FROM generate_series(1, ?) AS g`, customer, history).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO orders (id,customer_id,status,currency,total_minor,created_at,pricing_mode)
        SELECT `+idExpr+`, ?, 'PENDING', 'USD', 100, now() - interval '1 hour' + g * interval '1 millisecond', 'legacy'
        FROM generate_series(1, ?) AS g`, customer, rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE orders").Error; err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().UTC()

	startPlan, startMS := backlogExplain(t, db, statement, cutoff, batch)
	t.Logf("[%s] EXPLAIN at full backlog (%d pending, batch %d):\n%s", name, rows, batch, startPlan)
	t.Logf("[%s] plan features at full backlog: %s", name, backlogShape(startPlan))

	var durations []time.Duration
	processed := 0
	latePlanMS := -1.0
	explainedLate := false
	for {
		if !explainedLate && processed >= rows*9/10 {
			explainedLate = true
			var latePlan string
			latePlan, latePlanMS = backlogExplain(t, db, statement, cutoff, batch)
			t.Logf("[%s] EXPLAIN with about 10%% of the backlog left (%d processed):\n%s", name, processed, latePlan)
			t.Logf("[%s] plan features near the end: %s", name, backlogShape(latePlan))
		}
		var n int64
		began := time.Now()
		err := db.Transaction(func(tx *orm.DB) error {
			return tx.Raw(statement, cutoff, batch).Scan(&n).Error
		})
		elapsed := time.Since(began)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if int(n) > batch {
			t.Fatalf("[%s] batch limit exceeded: %d > %d", name, n, batch)
		}
		durations = append(durations, elapsed)
		processed += int(n)
	}

	var pending, processing, delivered int64
	db.Table("orders").Where("status = 'PENDING'").Count(&pending)
	db.Table("orders").Where("status = 'PROCESSING'").Count(&processing)
	db.Table("orders").Where("status = 'DELIVERED'").Count(&delivered)
	if processed != rows || pending != 0 || processing != int64(rows) || delivered != int64(history) {
		t.Fatalf("[%s] unexpected final state: processed=%d pending=%d processing=%d delivered=%d (rows=%d history=%d)",
			name, processed, pending, processing, delivered, rows, history)
	}
	if len(durations) == 0 {
		t.Fatalf("[%s] no batches were processed", name)
	}

	k := 10
	if len(durations) < 2*k {
		k = len(durations) / 2
	}
	if k < 1 {
		k = 1
	}
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p95Index := int(float64(len(sorted)) * 0.95)
	if p95Index >= len(sorted) {
		p95Index = len(sorted) - 1
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}

	return backlogResult{
		name:        name,
		batches:     len(durations),
		startPlanMS: startMS,
		latePlanMS:  latePlanMS,
		firstMS:     backlogMean(durations[:k]),
		lastMS:      backlogMean(durations[len(durations)-k:]),
		p50MS:       backlogMS(sorted[len(sorted)/2]),
		p95MS:       backlogMS(sorted[p95Index]),
		maxMS:       backlogMS(sorted[len(sorted)-1]),
		totalS:      total.Seconds(),
	}
}

// TestPendingBacklogBatchPlanComparison compares the production batch SQL with a
// primary-key rewrite on a large PENDING backlog. It is opt-in so the normal
// integration suite stays fast:
//
//	BACKLOG_ROWS=200000 go test -mod=readonly -count=1 -tags=integration \
//	  -run TestPendingBacklogBatchPlanComparison -v -timeout 30m ./db/gorm
//
// BACKLOG_BATCH (default 500, the application default) sets the batch size.
// Each variant runs in its own freshly seeded schema and drains the whole
// backlog with committed batches. The test fails only if a variant processes the
// wrong rows; plan shapes and timings are reported, not asserted, because they
// depend on the PostgreSQL version and hardware.
func TestPendingBacklogBatchPlanComparison(t *testing.T) {
	rows := backlogEnvInt(t, "BACKLOG_ROWS", 0)
	if rows == 0 {
		t.Skip("set BACKLOG_ROWS=200000 to run the pending-backlog plan comparison")
	}
	batch := backlogEnvInt(t, "BACKLOG_BATCH", 500)
	if batch < 1 || batch > 10000 {
		t.Fatalf("BACKLOG_BATCH must be between 1 and 10000, got %d", batch)
	}

	variants := []struct {
		name      string
		statement string
	}{
		{"current_join", database.PendingBatchSQL},
		{"primary_key_array", backlogPrimaryKeySQL},
	}

	var results []backlogResult
	for _, v := range variants {
		v := v
		t.Run(v.name, func(t *testing.T) {
			results = append(results, backlogDrain(t, v.name, v.statement, rows, batch))
		})
	}
	if len(results) != len(variants) {
		return
	}

	t.Logf("pending backlog %d rows, batch %d, %d batches per variant", rows, batch, results[0].batches)
	t.Logf("%-18s %10s %10s %8s %8s %8s %8s %9s %10s %10s",
		"variant", "first10ms", "last10ms", "growth", "p50ms", "p95ms", "maxms", "total_s", "explain0ms", "explainEms")
	for _, r := range results {
		growth := 0.0
		if r.firstMS > 0 {
			growth = r.lastMS / r.firstMS
		}
		t.Logf("%-18s %10.2f %10.2f %7.1fx %8.2f %8.2f %8.2f %9.2f %10.2f %10.2f",
			r.name, r.firstMS, r.lastMS, growth, r.p50MS, r.p95MS, r.maxMS, r.totalS, r.startPlanMS, r.latePlanMS)
	}
	t.Log("first10ms and last10ms are mean batch times for the first and last 10 batches (a smaller sample on tiny runs).")
	t.Log("growth = last10ms / first10ms. Near 1x means per-batch cost does not depend on the remaining backlog.")
	t.Log("explain0ms and explainEms are EXPLAIN ANALYZE execution times at full backlog and with about 10% left.")
}
