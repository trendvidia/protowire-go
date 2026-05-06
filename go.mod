module github.com/trendvidia/protowire-go

go 1.25.0

require (
	github.com/bufbuild/protocompile v0.14.1
	github.com/stretchr/testify v1.11.1
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/sync v0.20.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace google.golang.org/protobuf => github.com/trendvidia/protobuf-go v1.36.12
