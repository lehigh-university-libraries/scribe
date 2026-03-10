module github.com/lehigh-university-libraries/scribe

go 1.24.4

require (
	connectrpc.com/connect v1.19.1
	github.com/go-sql-driver/mysql v1.9.0
	github.com/lehigh-university-libraries/htr v0.12.0
	github.com/otiai10/gosseract/v2 v2.4.1
	google.golang.org/protobuf v1.36.11
)

require filippo.io/edwards25519 v1.1.1 // indirect

replace github.com/lehigh-university-libraries/scribe => .
