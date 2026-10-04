# Live order-processing demo

Use this runbook with the [test/coverage plan](04-test-plan.md). It covers every
assignment requirement plus the approved authentication and regional-pricing
behavior. Run these checks locally when preparing the assignment demo.

Run the code blocks in order in **one Bash session**, from the repository root.
Use Bash even if your normal shell is zsh. Preparation needs Go 1.26, Python 3,
Make, Docker Compose, curl, jq and network access. Complete `make acceptance`
before presenting. Allow 15–20 minutes after preparation;
two genuine five-minute worker ticks take at least ten minutes.

## Preparation — isolated stack and fixtures

First run **only this block** in your existing terminal, then wait for the new
Bash prompt before pasting any preparation commands. Do not use `exec bash`.
Turning off exit-on-error in the parent shell means a failed demo child shell
returns you to the original prompt instead of closing the terminal.

```sh
set +e
set +u
set +x
bash --noprofile --norc
```

Now paste the preparation block below **at the Bash prompt**. Its strict options
apply only to this child shell. Keep using that same Bash session for all later
blocks so the variables and helper functions remain available. If it stops,
retain the first error printed above the exit message; do not continue later
blocks in the parent shell with missing variables.

The `dc` helper ignores local `.env`
settings and uses a unique Compose project with its own database volume. The
chosen ports must be free. Passwords below are disposable local demo credentials.
The helper disconnects Docker stdin, and one-off containers disable TTY allocation,
so Docker cannot consume subsequent pasted setup commands.

```bash
trap 'demo_exit_status=$?; if [ "$demo_exit_status" -ne 0 ]; then printf "Demo Bash session stopped (exit %s). See the error above; the parent terminal remains open.\n" "$demo_exit_status" >&2; fi' EXIT
set -euo pipefail
set +x
umask 077
export COMPOSE_PROJECT_NAME="orders-demo-$(date -u +%Y%m%d%H%M%S)-$$"
export HTTP_PORT=18081 POSTGRES_PORT=15433
export POSTGRES_PASSWORD=orders_local
export JWT_SECRET="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
export JWT_ISSUER=order-management JWT_TOKEN_TTL=24h
export STORE_REGION=US CURRENCY=USD QUOTE_TTL=5m
export PROCESSING_INTERVAL=5m PROCESSING_BATCH_SIZE=500
export AUTH_LOGIN_LIMIT=10 AUTH_REGISTER_LIMIT=5 AUTH_RATE_WINDOW=1m
export AUTH_RATE_MAX_KEYS=10000 AUTH_MAX_IN_FLIGHT=4
export ADMIN_PASSWORD=demo-admin-password
API_URL="http://127.0.0.1:$HTTP_PORT"
DEMO_TMP="$(mktemp -d "${TMPDIR:-/tmp}/orders-demo.XXXXXX")"
DEMO_BODY="$DEMO_TMP/response.json"
dc() { docker compose --env-file /dev/null "$@" </dev/null; }

# Resolve dependencies before readonly Docker builds (already done by acceptance).
go mod tidy
dc up --build -d --wait
dc run --rm -T -e ADMIN_PASSWORD api admin --email admin@example.com --name 'Demo Admin'
RATE_FROM="$(python3 -c 'from datetime import datetime,timezone,timedelta; print((datetime.now(timezone.utc)-timedelta(minutes=1)).isoformat())')"
RATE_UNTIL="$(python3 -c 'from datetime import datetime,timezone,timedelta; print((datetime.now(timezone.utc)+timedelta(hours=1)).isoformat())')"
dc run --rm -T api rates add --target INR --rate 2 \
  --valid-from "$RATE_FROM" --valid-until "$RATE_UNTIL" --source demo-fixture

```

Define a request helper. It checks the HTTP status, prints responses,
and redacts login/registration tokens. Raw responses are only kept temporarily
under `$DEMO_TMP`; do not share that directory or enable shell tracing.

```bash
api() {
  local expected="$1" method="$2" path="$3" token="${4:-}" body="${5:-}" key="${6:-}" code
  local args=(-sS --connect-timeout 3 --max-time 10 -X "$method"
    -H 'Content-Type: application/json' -o "$DEMO_BODY" -w '%{http_code}')
  if [ -n "$token" ]; then args+=(-H "Authorization: Bearer $token"); fi
  if [ -n "$key" ]; then args+=(-H "Idempotency-Key: $key"); fi
  if [ -n "$body" ]; then args+=(--data "$body"); fi
  code="$(curl "${args[@]}" "$API_URL$path")"
  printf '%s %s -> HTTP %s (expected %s)\n' \
    "$method" "$path" "$code" "$expected"
  if [ -s "$DEMO_BODY" ]; then
    jq 'if type == "object" and has("token") then .token = "<redacted>" else . end' \
      "$DEMO_BODY"
  fi
  [ "$code" = "$expected" ]
}

api 200 POST /api/v1/auth/login '' \
  '{"email":"admin@example.com","password":"demo-admin-password"}'
ADMIN_TOKEN="$(jq -er .token "$DEMO_BODY")"
api 201 POST /api/v1/auth/register '' \
  '{"name":"Demo Customer","email":"customer@example.com","password":"demo-customer-password"}'
CUSTOMER_TOKEN="$(jq -er .token "$DEMO_BODY")"
api 201 POST /api/v1/auth/register '' \
  '{"name":"Other Customer","email":"other@example.com","password":"demo-other-password"}'
OTHER_TOKEN="$(jq -er .token "$DEMO_BODY")"
api 201 POST /api/v1/products "$ADMIN_TOKEN" \
  '{"sku":"DEMO-BOOK","name":"Notebook","price_minor":1299}'
P1="$(jq -er .id "$DEMO_BODY")"
api 201 POST /api/v1/products "$ADMIN_TOKEN" \
  '{"sku":"DEMO-PEN","name":"Pen","price_minor":299}'
P2="$(jq -er .id "$DEMO_BODY")"
CART="$(jq -nc --arg p1 "$P1" --arg p2 "$P2" \
  '{items:[{product_id:$p1,quantity:2},{product_id:$p2,quantity:3}]}')"
```

Explain: prices come from the catalog, quantities from the customer, and identity
from the token. Expected total is `1299 × 2 + 299 × 3 = 3495` USD minor units
(USD 34.95). No exchange-rate lookup is needed for a base-currency order.

## D0 — Start the five-minute clock

Restart only the API **after** fixture preparation, then run D1 immediately.
The worker's first tick is five minutes after process startup, not five minutes
after an individual order is placed. Do not restart the API again until D6 ends.

```bash
dc restart api
for attempt in {1..60}; do
  if curl -fsS --max-time 2 "$API_URL/api/v1/ready" > "$DEMO_TMP/ready.json"; then break; fi
  sleep 1
done
api 200 GET /api/v1/ready
jq -e '.checks.database == "ok" and .checks.worker == "starting" and .worker.last_attempt_at == null' "$DEMO_BODY"
test "$(dc exec -T api printenv PROCESSING_INTERVAL)" = 5m
```

Readiness should be HTTP 200 with worker `starting`; this is the startup grace
period. The configured interval must be `5m`.

## D1 — Create, cancel, and reject a skipped stage (R1, R3a, R5)

Run this whole block promptly, before explaining the output. It creates an order
for cancellation and a separate order for the worker demonstration.

```bash
api 422 POST /api/v1/orders "$CUSTOMER_TOKEN" "$CART"
api 201 POST /api/v1/orders "$CUSTOMER_TOKEN" "$CART" 'demo-request-1'
CANCEL_ID="$(jq -er .id "$DEMO_BODY")"
jq -e '.status == "PENDING" and .currency == "USD" and .total_minor == 3495 and (.items | length) == 2' "$DEMO_BODY"
api 200 POST "/api/v1/orders/$CANCEL_ID/cancel" "$CUSTOMER_TOKEN"
jq -e '.status == "CANCELLED"' "$DEMO_BODY"
api 200 POST "/api/v1/orders/$CANCEL_ID/cancel" "$CUSTOMER_TOKEN"

api 201 POST /api/v1/orders "$CUSTOMER_TOKEN" "$CART" 'demo-checkout'
ORDER_ID="$(jq -er .id "$DEMO_BODY")"
jq -e '.status == "PENDING" and .total_minor == 3495 and (.items | length) == 2' "$DEMO_BODY"
api 200 POST /api/v1/orders "$CUSTOMER_TOKEN" "$CART" 'demo-checkout'
jq -e --arg id "$ORDER_ID" '.id == $id' "$DEMO_BODY"
CHANGED_CART="$(jq '.items[0].quantity += 1' <<< "$CART")"
api 409 POST /api/v1/orders "$CUSTOMER_TOKEN" "$CHANGED_CART" 'demo-checkout'
jq -e '.details.reason == "idempotency_key_conflict"' "$DEMO_BODY"

api 409 PATCH "/api/v1/orders/$ORDER_ID/status" "$ADMIN_TOKEN" '{"status":"SHIPPED"}'
```

Expected: both creations return 201; keyed replay returns the same ID with 200,
changed payload reuse returns 409; first cancellation is 200/CANCELLED,
repetition is 200/CANCELLED, and PENDING → SHIPPED is 409. No manual transition to PROCESSING
is used for `ORDER_ID` or the second worker order below.

## D2 — Retrieve, ownership and invalid input (R1, R2, R3a)

```bash
api 200 GET "/api/v1/orders/$ORDER_ID" "$CUSTOMER_TOKEN"
jq -e --arg id "$ORDER_ID" '.id == $id and .total_minor == 3495 and (.items | length) == 2' "$DEMO_BODY"
api 200 GET "/api/v1/orders/$ORDER_ID" "$ADMIN_TOKEN"
api 404 GET "/api/v1/orders/$ORDER_ID" "$OTHER_TOKEN"
api 404 POST "/api/v1/orders/$ORDER_ID/cancel" "$OTHER_TOKEN"
UNKNOWN_ID="$(python3 -c 'import uuid; print(uuid.uuid4())')"
api 404 GET "/api/v1/orders/$UNKNOWN_ID" "$CUSTOMER_TOKEN"
api 401 GET /api/v1/orders
api 403 PATCH "/api/v1/orders/$ORDER_ID/status" "$CUSTOMER_TOKEN" '{"status":"PROCESSING"}'
api 403 POST /api/v1/products "$CUSTOMER_TOKEN" \
  '{"sku":"FORBIDDEN","name":"Forbidden","price_minor":1}'
api 422 POST /api/v1/orders "$CUSTOMER_TOKEN" '{"items":[]}' 'demo-request-2'
api 422 POST /api/v1/orders "$CUSTOMER_TOKEN" \
  "$(jq -c '.items[0].quantity = 0' <<< "$CART")" 'demo-request-3'
api 422 POST /api/v1/orders "$CUSTOMER_TOKEN" \
  "$(jq -c '.items[1] = .items[0]' <<< "$CART")" 'demo-request-4'
api 422 POST /api/v1/orders "$CUSTOMER_TOKEN" \
  "$(jq -nc --arg id "$UNKNOWN_ID" '{items:[{product_id:$id,quantity:1}]}')" 'demo-request-5'
api 400 POST /api/v1/orders "$CUSTOMER_TOKEN" \
  "$(jq -c '.items[0].unit_price_minor = 1' <<< "$CART")" 'demo-request-6'
```

Explain the difference between 401 (no authentication), 403 (wrong role), 404
(unknown or inaccessible order), 400 (unexpected JSON field) and 422 (invalid
business input). Verify rejected creation requests did not add orders in D3.

## D3 — List all, filter and paginate (R4)

```bash
api 200 GET /api/v1/orders "$CUSTOMER_TOKEN"
jq -e --arg a "$ORDER_ID" --arg b "$CANCEL_ID" \
  '(.items | map(.id) | sort) == ([$a,$b] | sort) and .next_cursor == null' "$DEMO_BODY"
api 200 GET '/api/v1/orders?status=CANCELLED' "$CUSTOMER_TOKEN"
jq -e --arg id "$CANCEL_ID" '(.items | length) == 1 and .items[0].id == $id and .items[0].status == "CANCELLED"' "$DEMO_BODY"
api 200 GET '/api/v1/orders?limit=1' "$CUSTOMER_TOKEN"
FIRST_PAGE_ID="$(jq -er '.items[0].id' "$DEMO_BODY")"
CURSOR="$(jq -er .next_cursor "$DEMO_BODY")"
api 200 GET "/api/v1/orders?limit=1&cursor=$CURSOR" "$CUSTOMER_TOKEN"
jq -e --arg first "$FIRST_PAGE_ID" '(.items | length) == 1 and .items[0].id != $first and .next_cursor == null' "$DEMO_BODY"
api 200 GET /api/v1/orders "$OTHER_TOKEN"
jq -e '(.items | length) == 0' "$DEMO_BODY"
api 422 GET '/api/v1/orders?status=UNKNOWN' "$CUSTOMER_TOKEN"
api 422 GET '/api/v1/orders?limit=0' "$CUSTOMER_TOKEN"
```

## D4 — First automatic tick, then delivery (R3, R5)

Define the polling helper and wait for automatic processing. It allows up to
330 seconds from invocation and checks readiness only after a successful drain.
The two-second polling interval is observation frequency, not processing cadence.

```bash
wait_processed() {
  local id="$1" deadline=$((SECONDS + 330)) state
  while (( SECONDS < deadline )); do
    curl -fsS --connect-timeout 3 --max-time 10 \
      -H "Authorization: Bearer $CUSTOMER_TOKEN" \
      "$API_URL/api/v1/orders/$id" > "$DEMO_TMP/poll.json"
    state="$(jq -er .status "$DEMO_TMP/poll.json")"
    printf '%s %s\n' "$id" "$state"
    if [ "$state" = PROCESSING ]; then
      api 200 GET /api/v1/ready
      if jq -e '.checks.worker == "ok" and .worker.running == false and .worker.last_success_at != null' "$DEMO_BODY" >/dev/null; then
        return 0
      fi
    elif [ "$state" != PENDING ]; then
      printf 'Unexpected order state: %s\n' "$state" >&2
      return 1
    fi
    sleep 2
  done
  printf 'Automatic processing timed out\n' >&2
  return 1
}
wait_processed "$ORDER_ID"

# Immediately create the next tick's order, before presenting the lifecycle.
api 201 POST /api/v1/orders "$CUSTOMER_TOKEN" "$CART" 'demo-request-7'
SECOND_ID="$(jq -er .id "$DEMO_BODY")"
jq -e '.status == "PENDING"' "$DEMO_BODY"

api 200 GET "/api/v1/orders/$ORDER_ID" "$CUSTOMER_TOKEN"
jq -e '.status == "PROCESSING"' "$DEMO_BODY"
api 409 POST "/api/v1/orders/$ORDER_ID/cancel" "$CUSTOMER_TOKEN"
api 200 PATCH "/api/v1/orders/$ORDER_ID/status" "$ADMIN_TOKEN" '{"status":"SHIPPED"}'
jq -e '.status == "SHIPPED"' "$DEMO_BODY"
api 409 POST "/api/v1/orders/$ORDER_ID/cancel" "$CUSTOMER_TOKEN"
api 409 PATCH "/api/v1/orders/$ORDER_ID/status" "$ADMIN_TOKEN" '{"status":"PROCESSING"}'
api 200 PATCH "/api/v1/orders/$ORDER_ID/status" "$ADMIN_TOKEN" '{"status":"DELIVERED"}'
jq -e '.status == "DELIVERED"' "$DEMO_BODY"
api 409 POST "/api/v1/orders/$ORDER_ID/cancel" "$CUSTOMER_TOKEN"
api 409 PATCH "/api/v1/orders/$ORDER_ID/status" "$ADMIN_TOKEN" '{"status":"DELIVERED"}'
api 200 GET '/api/v1/orders?status=DELIVERED' "$CUSTOMER_TOKEN"
jq -e --arg id "$ORDER_ID" '(.items | length) == 1 and .items[0].id == $id and .items[0].status == "DELIVERED"' "$DEMO_BODY"
```

Expected: worker changes PENDING to PROCESSING without a PATCH; admin advances
SHIPPED then DELIVERED. Cancellation fails at every non-pending stage. Reversing
and repeating a transition fail. Show that the worker is healthy.

## D5 — Regional quotes and access scope during the second interval

Run this within the next four minutes. `INR/USD = 2` is a labelled fixture. Default
region is US; choosing IN produces a new INR quote, while the catalog and existing
USD order retain their denomination. Consume the quote immediately; TTL is 5m.

```bash
# Omitted region uses STORE_REGION=US and identity pricing.
api 201 POST /api/v1/order-quotes "$CUSTOMER_TOKEN" "$CART"
jq -e '.region == "US" and .currency == "USD" and .total_minor == 3495' "$DEMO_BODY"
api 201 POST /api/v1/order-quotes "$CUSTOMER_TOKEN" \
  "$(jq -c '. + {region:"IN"}' <<< "$CART")"
QUOTE_ID="$(jq -er .id "$DEMO_BODY")"
jq -e '.region == "IN" and .base_currency == "USD" and .currency == "INR" and .source_total_minor == 3495 and .total_minor == 6990 and .rate_source == "demo-fixture"' "$DEMO_BODY"
QUOTE_BODY="$(jq -nc --arg id "$QUOTE_ID" '{quote_id:$id}')"
api 201 POST /api/v1/orders "$CUSTOMER_TOKEN" "$QUOTE_BODY" 'demo-request-8'
QUOTED_ORDER_ID="$(jq -er .id "$DEMO_BODY")"
jq -e '.currency == "INR" and .total_minor == 6990' "$DEMO_BODY"
api 200 POST /api/v1/orders "$CUSTOMER_TOKEN" "$QUOTE_BODY" 'demo-request-9'
jq -e --arg id "$QUOTED_ORDER_ID" '.id == $id and .total_minor == 6990' "$DEMO_BODY"
api 404 POST /api/v1/orders "$OTHER_TOKEN" "$QUOTE_BODY" 'demo-request-10'
api 409 POST /api/v1/order-quotes "$CUSTOMER_TOKEN" \
  "$(jq -c '. + {region:"DE"}' <<< "$CART")"
api 422 POST /api/v1/order-quotes "$CUSTOMER_TOKEN" \
  "$(jq -c '. + {region:"XX"}' <<< "$CART")"
api 200 GET "/api/v1/orders/$ORDER_ID" "$CUSTOMER_TOKEN"
jq -e '.currency == "USD" and .total_minor == 3495 and .status == "DELIVERED"' "$DEMO_BODY"

# Give the second customer an order to prove admin visibility across owners.
api 201 POST /api/v1/orders "$OTHER_TOKEN" "$CART" 'demo-request-11'
OTHER_ORDER_ID="$(jq -er .id "$DEMO_BODY")"
api 200 GET /api/v1/orders "$CUSTOMER_TOKEN"
jq -e --arg id "$OTHER_ORDER_ID" 'all(.items[]; .id != $id)' "$DEMO_BODY"
api 200 GET /api/v1/orders "$ADMIN_TOKEN"
jq -e --arg a "$ORDER_ID" --arg b "$OTHER_ORDER_ID" \
  'any(.items[]; .id == $a) and any(.items[]; .id == $b)' "$DEMO_BODY"
```

EUR has no imported rate, so DE returns 409; unknown region XX returns 422. The
same quote can be retried without another order. Changing region/items requires
a new quote; changing the store default does not convert existing catalog prices.
Quote expiry, rounding boundaries and simultaneous submissions are covered in the
automated tests; avoid a configuration restart during the timed demonstration.

## D6 — Second automatic tick and preserved terminal states (R3b, R5)

```bash
wait_processed "$SECOND_ID"
api 200 GET "/api/v1/orders/$CANCEL_ID" "$CUSTOMER_TOKEN"
jq -e '.status == "CANCELLED"' "$DEMO_BODY"
api 200 GET "/api/v1/orders/$ORDER_ID" "$CUSTOMER_TOKEN"
jq -e '.status == "DELIVERED"' "$DEMO_BODY"
```

Confirm the second order is PROCESSING and the earlier terminal states remain
unchanged.

## D7 — Manual processing and logout (R3a and authentication)

After observing the two worker runs, demonstrate the optional manual processing
path on a fresh order. It does not replace the automatic processing check.

```bash
api 201 POST /api/v1/orders "$CUSTOMER_TOKEN" "$CART" 'demo-request-12'
MANUAL_ID="$(jq -er .id "$DEMO_BODY")"
api 200 PATCH "/api/v1/orders/$MANUAL_ID/status" "$ADMIN_TOKEN" '{"status":"PROCESSING"}'
jq -e '.status == "PROCESSING"' "$DEMO_BODY"
api 204 POST /api/v1/auth/logout "$CUSTOMER_TOKEN"
api 401 GET "/api/v1/orders/$ORDER_ID" "$CUSTOMER_TOKEN"
api 200 GET /api/v1/auth/session "$CUSTOMER_TOKEN"
jq -e '.authenticated == false' "$DEMO_BODY"
```

## Results, recovery and cleanup

Use the [local demo checklist](04-test-plan.md#local-demo-checklist) to review
the expected behavior. Run the automated suite for migration, concurrency,
rollback and race checks.

| Interruption | Recovery / accurate outcome |
| --- | --- |
| Terminal closes immediately after pasting preparation | `set -e` was applied to the main shell, so a failure exited it. Reopen the terminal and run the separate child-Bash launch block above first. Interactive zsh may interpret `#` comments as commands unless `INTERACTIVE_COMMENTS` is enabled; use the documented Bash child. |
| Setup exits with a port-already-allocated error | A previous demo or another service may still own 18081/15433. Inspect the named container/service or choose unused ports in the preparation block; a new project name alone does not release ports. Do not remove unrelated volumes. |
| Dependencies, Docker or readiness unavailable | Resolve the local dependency or service error before continuing. |
| D1 cancellation returns 409 after a long presentation pause | Worker may have won. Check the order; repeat with a fresh immediately cancelled order. Restart the full isolated sequence if the exact two-order list assertions no longer apply. A 409 does not pass the pending-cancellation demonstration. |
| An assertion fails | Stop, preserve responses/logs, fix the cause and rerun the affected sequence. Do not continue with stale IDs or edit expected results to fit. |
| A 429 occurs while repeating authentication setup | Respect Retry-After. A clean run uses two registrations; defaults permit five/minute/IP. Use a fresh isolated run rather than disabling limits. |
| Quote expires or fixture rate expires | Create and consume a fresh quote; if the one-hour fixture expired, restart preparation with a fresh project. |
| Only a few minutes are available | Run `make smoke-docker` and label its 5-second interval. Mark the real five-minute cadence demonstration NOT RUN. |

Cleanup removes only the dedicated demo project's containers and volume. Run from
the same shell so `COMPOSE_PROJECT_NAME` still identifies this run. Remove temporary raw token responses. To rehearse again, start
with a fresh Bash session and new project rather than rerunning fixture creation
against an existing catalog.

```bash
case "$COMPOSE_PROJECT_NAME" in
  orders-demo-*) dc down --volumes ;;
  *) printf 'Refusing cleanup: unexpected project name\n' >&2; exit 1 ;;
esac
rm -f "$DEMO_TMP/response.json" "$DEMO_TMP/ready.json" "$DEMO_TMP/poll.json"
rmdir "$DEMO_TMP"
unset ADMIN_TOKEN CUSTOMER_TOKEN OTHER_TOKEN JWT_SECRET ADMIN_PASSWORD
```
