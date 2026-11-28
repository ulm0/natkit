module github.com/ulm0/natkit/init

go 1.27.4

require (
	github.com/aws/aws-sdk-go-v2 v1.48.2
	github.com/aws/aws-sdk-go-v2/credentials v1.21.5
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.11
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.82.2
	github.com/aws/aws-sdk-go-v2/service/cloudwatch v1.77.0
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.350.2
	github.com/aws/aws-sdk-go-v2/service/ssm v1.84.2
	github.com/aws/smithy-go v1.28.11
	go.yaml.in/yaml/v4 v4.0.0
	golang.org/x/sys v0.50.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.14 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.14 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.27 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.14 // indirect
)
