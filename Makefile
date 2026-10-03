.PHONY: build run test integration vet fmt fmt-check migrate-up migrate-down seed smoke smoke-docker up acceptance
acceptance:
	python3 scripts/acceptance.py
build:
	go build -mod=readonly -o bin/orders ./cmd/orders
run:
	go run -mod=readonly ./cmd/orders server
test:
	go test -mod=readonly -race ./...
integration:
	test -n "$$TEST_DATABASE_URL"
	go test -mod=readonly -race -tags=integration ./...
vet:
	go vet -mod=readonly ./...
fmt:
	gofmt -w api cmd configs db docs domain internal ports service
fmt-check:
	test -z "$$(gofmt -l api cmd configs db docs domain internal ports service)"
migrate-up:
	go run -mod=readonly ./cmd/orders migrate up
migrate-down:
	go run -mod=readonly ./cmd/orders migrate down
seed:
	go run -mod=readonly ./cmd/orders seed
smoke:
	python3 scripts/smoke.py
smoke-docker:
	sh scripts/smoke-docker.sh
up:
	docker compose up --build -d --wait
