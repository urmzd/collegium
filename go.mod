module github.com/urmzd/open-agent-platform

go 1.26.9

require (
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-jose/go-jose/v4 v4.1.5
	github.com/urmzd/dispatch v0.1.0
	github.com/urmzd/duraturo v0.1.1
	github.com/urmzd/opentag v0.1.0
	github.com/urmzd/saige v0.22.0
	golang.org/x/oauth2 v0.37.0
)

require (
	connectrpc.com/connect v1.20.0 // indirect
	github.com/anthropics/anthropic-sdk-go v1.30.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/redis/go-redis/v9 v9.21.0 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/urmzd/opentag => ../opentag
