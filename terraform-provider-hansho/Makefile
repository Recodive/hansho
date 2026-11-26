.PHONY: build fmt vet test

build:
	go build -o terraform-provider-hansho .

fmt:
	gofmt -l .

vet:
	go vet ./...

# The provider tests run the real terraform binary (on PATH, or TF_ACC_TERRAFORM_PATH) against a fake hub, and are
# skipped without one. They need no network and no hub.
test:
	go test ./...
