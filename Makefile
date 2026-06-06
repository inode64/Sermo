BIN := bin
PREFIX ?= /usr/local

.PHONY: all build test vet fmt fmt-check tidy clean install

all: build

build:
	go build -o $(BIN)/sermoctl ./cmd/sermoctl
	go build -o $(BIN)/sermod ./cmd/sermod

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l internal cmd)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)

install: build
	install -Dm755 $(BIN)/sermoctl $(DESTDIR)$(PREFIX)/bin/sermoctl
	install -Dm755 $(BIN)/sermod $(DESTDIR)$(PREFIX)/bin/sermod
	install -d $(DESTDIR)/usr/share/sermo/profiles
	install -m644 profiles/*.yml $(DESTDIR)/usr/share/sermo/profiles/
