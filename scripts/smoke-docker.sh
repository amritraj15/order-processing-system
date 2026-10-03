#!/bin/sh
set -eu
# A separate project, volume, and ports keep test data away from the local app.
export COMPOSE_PROJECT_NAME="${SMOKE_PROJECT_NAME:-orders-smoke-$$}"
export HTTP_PORT="${SMOKE_HTTP_PORT:-18080}"
export POSTGRES_PORT="${SMOKE_POSTGRES_PORT:-15432}"
export API_URL="http://127.0.0.1:$HTTP_PORT"
export JWT_SECRET="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
export PROCESSING_INTERVAL=5s
export STORE_REGION=US
export CURRENCY=USD
export ADMIN_EMAIL="smoke-admin-$(date +%s)@example.com"
export ADMIN_PASSWORD="smoke-password-$(date +%s)-$$"
trap 'docker compose down --volumes' EXIT
docker compose up --build -d --wait
docker compose run --rm -e ADMIN_PASSWORD api admin --email "$ADMIN_EMAIL" --name 'Smoke Admin'
SMOKE_VALID_FROM=$(python3 -c 'from datetime import datetime,timezone,timedelta; print((datetime.now(timezone.utc)-timedelta(minutes=1)).isoformat())')
SMOKE_VALID_UNTIL=$(python3 -c 'from datetime import datetime,timezone,timedelta; print((datetime.now(timezone.utc)+timedelta(hours=1)).isoformat())')
docker compose run --rm api rates add --target INR --rate 2 --valid-from "$SMOKE_VALID_FROM" --valid-until "$SMOKE_VALID_UNTIL" --source test-fixture
SMOKE_EXPECT_WORKER=1 python3 scripts/smoke.py
