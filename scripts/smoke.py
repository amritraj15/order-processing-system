#!/usr/bin/env python3
"""Exercise a running API. Set SMOKE_EXPECT_WORKER=1 for a short-interval worker."""
import json
import os
import time
import urllib.error
import urllib.request
import uuid


def call(method, path, token=None, body=None, expected=200, key=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if key is not None:
        headers["Idempotency-Key"] = key
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        content = response.read()
        assert response.status in (expected if isinstance(expected, tuple) else (expected,)), (method, path, response.status, content)
        return json.loads(content) if content else None


def main():
    password = os.environ.get("ADMIN_PASSWORD")
    if not password:
        raise SystemExit("Set ADMIN_PASSWORD for a provisioned admin account")
    suffix = uuid.uuid4().hex
    admin = call("POST", "/api/v1/auth/login", body={"email": os.environ.get("ADMIN_EMAIL", "admin@example.com"), "password": password})["token"]
    customer = call("POST", "/api/v1/auth/register", body={"name": "Smoke Customer", "email": suffix + "@example.com", "password": "smoke-password"}, expected=201)["token"]
    other = call("POST", "/api/v1/auth/register", body={"name": "Other Customer", "email": "other-" + suffix + "@example.com", "password": "smoke-password"}, expected=201)["token"]
    call("GET", "/api/v1/ready")
    call("GET", "/api/v1/orders", expected=401)
    products = [call("POST", "/api/v1/products", admin, {"sku": suffix + str(i), "name": "Item " + str(i), "price_minor": price}, 201) for i, price in enumerate([1299, 299])]
    call("POST", "/api/v1/products", customer, {"sku": "forbidden", "name": "Item", "price_minor": 1}, 403)
    payload = {"items": [{"product_id": p["id"], "quantity": qty} for p, qty in zip(products, [2, 3])]}
    cancelled = call("POST", "/api/v1/orders", customer, payload, 201)
    cancel_result = call("POST", "/api/v1/orders/" + cancelled["id"] + "/cancel", customer, expected=(200, 409))
    cancelled_state = call("GET", "/api/v1/orders/" + cancelled["id"], customer)["status"]
    assert cancelled_state in ("CANCELLED", "PROCESSING")
    if cancelled_state == "CANCELLED":
        assert call("POST", "/api/v1/orders/" + cancelled["id"] + "/cancel", customer)["status"] == "CANCELLED"
    key = "smoke-" + suffix
    order = call("POST", "/api/v1/orders", customer, payload, 201, key=key)
    assert call("POST", "/api/v1/orders", customer, payload, key=key)["id"] == order["id"]
    changed = {"items": [{"product_id": products[0]["id"], "quantity": 1}]}
    call("POST", "/api/v1/orders", customer, changed, 409, key=key)
    path = "/api/v1/orders/" + order["id"]
    assert order["total_minor"] == 3495 and len(order["items"]) == 2
    call("GET", path, other, expected=404)
    call("PATCH", path + "/status", customer, {"status": "PROCESSING"}, 403)
    # Skipped-stage rejection is verified with a controlled worker in integration tests.
    if os.environ.get("SMOKE_EXPECT_WORKER") == "1":
        deadline = time.monotonic() + 20
        while call("GET", path, customer)["status"] == "PENDING":
            if time.monotonic() > deadline:
                raise AssertionError("worker did not process the order within 20 seconds")
            time.sleep(0.2)
    else:
        call("PATCH", path + "/status", admin, {"status": "PROCESSING"}, expected=(200, 409))
    assert call("GET", path, customer)["status"] == "PROCESSING"
    call("POST", path + "/cancel", customer, expected=409)
    for status in ["SHIPPED", "DELIVERED"]:
        call("PATCH", path + "/status", admin, {"status": status})
    assert call("GET", "/api/v1/orders?status=DELIVERED", customer)["items"][0]["id"] == order["id"]
    assert not call("GET", "/api/v1/orders", other)["items"]
    page = call("GET", "/api/v1/orders?limit=1", customer)
    assert page["next_cursor"]
    assert len(call("GET", "/api/v1/orders?limit=1&cursor=" + page["next_cursor"], customer)["items"]) == 1
    quote_region = "IN" if os.environ.get("SMOKE_EXPECT_WORKER") == "1" else "US"
    quote = call("POST", "/api/v1/order-quotes", customer, dict(payload, region=quote_region), 201)
    quoted = call("POST", "/api/v1/orders", customer, {"quote_id": quote["id"]}, 201)
    assert quoted["currency"] == quote["currency"] and quoted["total_minor"] == quote["total_minor"]
    assert call("POST", "/api/v1/orders", customer, {"quote_id": quote["id"]})["id"] == quoted["id"]
    call("POST", "/api/v1/orders", other, {"quote_id": quote["id"]}, 404)
    call("POST", "/api/v1/auth/logout", customer, expected=204)
    call("GET", path, customer, expected=401)
    print("API smoke test passed: auth, catalog, orders, idempotency, isolation, transitions, cancellation, pagination, logout")


base = os.environ.get("API_URL", "http://127.0.0.1:8080").rstrip("/")
if __name__ == "__main__":
    main()
