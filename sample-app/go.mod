module sample-app

go 1.24.3

require github.com/opendatahub-io/mlflow-go v0.0.0

require (
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/opendatahub-io/mlflow-go => ../
