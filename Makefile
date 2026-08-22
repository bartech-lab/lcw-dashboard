.PHONY: build web dev test check clean install-linux install-macos

BIN := lcw-dashboard

build: web
	go build -o $(BIN) ./cmd/lcwd

web:
	go run ./tools/build-web

dev:
	go run ./tools/build-web -dev
	go run -tags dev ./cmd/lcwd

test:
	go test -race ./...

check:
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...
	go test -race ./...
	go run ./tools/check-contrast
	go run ./tools/build-web

# Optional type check. esbuild strips TypeScript types without checking them.
typecheck:
	npx --yes typescript@5 tsc --noEmit -p web/tsconfig.json

clean:
	rm -f $(BIN)
	rm -f web/dist/bundle.js web/dist/bundle.css web/dist/index.html

install-linux: build
	install -Dm755 $(BIN) $$HOME/.local/bin/$(BIN)
	install -Dm644 packaging/lcw-dashboard.service $$HOME/.config/systemd/user/lcw-dashboard.service
	systemctl --user daemon-reload
	@echo "Now run: systemctl --user enable --now lcw-dashboard"

install-macos: build
	install -Dm755 $(BIN) $$HOME/.local/bin/$(BIN)
	mkdir -p $$HOME/Library/LaunchAgents
	sed "s|__HOME__|$$HOME|g" packaging/com.lcw-dashboard.plist > $$HOME/Library/LaunchAgents/com.lcw-dashboard.plist
	@echo "Now run: launchctl load -w $$HOME/Library/LaunchAgents/com.lcw-dashboard.plist"
