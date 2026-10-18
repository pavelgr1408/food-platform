.PHONY: keys test run compose-up compose-down

keys:
	@mkdir -p config/keys
	@if [ ! -f config/keys/private.pem ] || [ ! -f config/keys/public.pem ]; then \
		openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out config/keys/private.pem; \
		openssl pkey -in config/keys/private.pem -pubout -out config/keys/public.pem; \
		chmod 600 config/keys/private.pem; \
	fi

test:
	go test ./...

run:
	go run ./cmd/auth-service

compose-up: keys
	docker compose up --build

compose-down:
	docker compose down
