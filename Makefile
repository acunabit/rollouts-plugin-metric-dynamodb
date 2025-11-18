SHELL := /bin/bash

.PHONY: build-rollouts-plugin-metric-dynamodb-debug
build-rollouts-plugin-metric-dynamodb-debug:
	CGO_ENABLED=0 go build -gcflags="all=-N -l" -o rollouts-plugin-metric-dynamodb main.go

.PHONY: build-rollouts-plugin-metric-dynamodb
build-rollouts-plugin-metric-dynamodb:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o rollouts-plugin-metric-dynamodb-linux-amd64 main.go
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o rollouts-plugin-metric-dynamodb-linux-arm64 main.go
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o rollouts-plugin-metric-dynamodb-darwin-amd64 main.go
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o rollouts-plugin-metric-dynamodb-darwin-arm64 main.go

.PHONY: build-rollouts-plugin-metric-dynamodb-x86
build-rollouts-plugin-metric-dynamodb-x86:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o rollouts-plugin-metric-dynamodb-linux-amd64 main.go
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o rollouts-plugin-metric-dynamodb-darwin-amd64 main.go
tag-release:
	if [[ $(TAG) == v?.?.? ]]; then \
		echo "Tagging $(TAG)"; \
	elif [[ $(TAG) == v?.?.?? ]]; then \
		echo "Tagging $(TAG)"; \
	else \
		echo "Bad Tag Format: $(TAG)"; \
		exit 1; \
	fi && \
	git tag -s -a $(TAG) -m "Releasing $(TAG)" && \
	read -p "Push tag: $(TAG)? " push_tag && \
	if [ "$${push_tag}" = "yes" ]; then \
		git push argoproj $(TAG); \
	fi