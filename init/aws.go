package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
)

// natkit's AWS calls go through the AWS SDK for Go v2: IMDSv2, the instance role's credentials, EC2, Auto Scaling,
// SSM and CloudWatch, only those modules. The SDK's own retries are off: natkit's loops are the policy (most calls
// tried 10 times, 3 s apart; the static ENI waited for up to 15 min), and every call has its own timeout.

const (
	callTimeout   = 20 * time.Second // EC2 and Auto Scaling
	shortTimeout  = 10 * time.Second // SSM and CloudWatch
	imdsTimeout   = 5 * time.Second
	tokenTimeout  = 2 * time.Second
	metricsName   = "natkit" // CloudWatch namespace
	metricsDimKey = "Name"
)

// metadata: the instance metadata service (IMDSv2).
type metadata interface {
	get(ctx context.Context, path string) (string, error) // meta-data/<path>
	userData(ctx context.Context) ([]byte, error)         // nil when there is none
	region(ctx context.Context) (string, error)
}

type imdsMetadata struct{ c *imds.Client }

func newIMDS(endpoint string) *imds.Client {
	return imds.New(imds.Options{Endpoint: endpoint, EnableFallback: aws.FalseTernary, Retryer: aws.NopRetryer{}})
}

func (m imdsMetadata) get(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, imdsTimeout)
	defer cancel()
	out, err := m.c.GetMetadata(ctx, &imds.GetMetadataInput{Path: path})
	if err != nil {
		return "", fmt.Errorf("instance metadata %s: %s", path, short(err))
	}
	defer out.Content.Close()
	b, err := io.ReadAll(out.Content)
	return strings.TrimSpace(string(b)), err
}

func (m imdsMetadata) userData(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, imdsTimeout)
	defer cancel()
	out, err := m.c.GetUserData(ctx, &imds.GetUserDataInput{})
	if httpStatus(err) == 404 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("instance metadata user-data: %s", short(err))
	}
	defer out.Content.Close()
	return io.ReadAll(out.Content)
}

func (m imdsMetadata) region(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, imdsTimeout)
	defer cancel()
	out, err := m.c.GetRegion(ctx, &imds.GetRegionInput{})
	if err != nil {
		return "", fmt.Errorf("instance metadata placement/region: %s", short(err))
	}
	return out.Region, nil
}

// cloud: the AWS calls natkit makes. Errors read "<Action>: HTTP <status> <code>: <message>", or the transport's.
type cloud interface {
	sourceDestCheckOff(ctx context.Context, eni string) error
	attachment(ctx context.Context, eni string) (string, error) // "" when the ENI is attached nowhere
	detach(ctx context.Context, attachment string) error
	attach(ctx context.Context, instance, eni string) error
	associateAddress(ctx context.Context, allocation, eni string) error
	asgInstance(ctx context.Context, instance string) (group, state string, err error) // "" when in no group
	heartbeat(ctx context.Context, group, hook, instance string) error
	complete(ctx context.Context, group, hook, instance string) error
	parameter(ctx context.Context, name string) (string, error) // decrypted
	putMetrics(ctx context.Context, name string, exceeded, available float64) error
}

type sdkCloud struct {
	ec2 *ec2.Client
	asg *autoscaling.Client
	ssm *ssm.Client
	cw  *cloudwatch.Client
}

// newCloud: the SDK's clients for the region, with the instance role's credentials (fetched through IMDS when first
// needed, cached until they expire; one that can't be fetched yet is fetched again on the next call).
func newCloud(c config, region string, md *imds.Client) *sdkCloud {
	cfg := aws.Config{
		Region:      region,
		Credentials: aws.NewCredentialsCache(ec2rolecreds.New(func(o *ec2rolecreds.Options) { o.Client = md })),
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
		AppID:       "natkit-" + version, // CloudTrail's userAgent says which natkit made the call
	}
	ep := func(u string) *string {
		if u == "" {
			return nil
		}
		return aws.String(u)
	}
	return &sdkCloud{
		ec2: ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = ep(c.ec2) }),
		asg: autoscaling.NewFromConfig(cfg, func(o *autoscaling.Options) { o.BaseEndpoint = ep(c.autoscaling) }),
		ssm: ssm.NewFromConfig(cfg, func(o *ssm.Options) { o.BaseEndpoint = ep(c.ssm) }),
		cw:  cloudwatch.NewFromConfig(cfg, func(o *cloudwatch.Options) { o.BaseEndpoint = ep(c.cloudwatch) }),
	}
}

func (s *sdkCloud) sourceDestCheckOff(ctx context.Context, eni string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := s.ec2.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
		NetworkInterfaceId: aws.String(eni), SourceDestCheck: &ec2types.AttributeBooleanValue{Value: aws.Bool(false)},
	})
	return apiError("ModifyNetworkInterfaceAttribute", err)
}

func (s *sdkCloud) attachment(ctx context.Context, eni string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	out, err := s.ec2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{eni}})
	if err != nil {
		return "", apiError("DescribeNetworkInterfaces", err)
	}
	if len(out.NetworkInterfaces) == 0 || out.NetworkInterfaces[0].Attachment == nil {
		return "", nil
	}
	return aws.ToString(out.NetworkInterfaces[0].Attachment.AttachmentId), nil
}

func (s *sdkCloud) detach(ctx context.Context, attachment string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := s.ec2.DetachNetworkInterface(ctx, &ec2.DetachNetworkInterfaceInput{AttachmentId: aws.String(attachment), Force: aws.Bool(true)})
	return apiError("DetachNetworkInterface", err)
}

func (s *sdkCloud) attach(ctx context.Context, instance, eni string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := s.ec2.AttachNetworkInterface(ctx, &ec2.AttachNetworkInterfaceInput{
		InstanceId: aws.String(instance), NetworkInterfaceId: aws.String(eni), DeviceIndex: aws.Int32(1),
	})
	return apiError("AttachNetworkInterface", err)
}

func (s *sdkCloud) associateAddress(ctx context.Context, allocation, eni string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := s.ec2.AssociateAddress(ctx, &ec2.AssociateAddressInput{
		AllocationId: aws.String(allocation), NetworkInterfaceId: aws.String(eni), AllowReassociation: aws.Bool(true),
	})
	return apiError("AssociateAddress", err)
}

func (s *sdkCloud) asgInstance(ctx context.Context, instance string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	out, err := s.asg.DescribeAutoScalingInstances(ctx, &autoscaling.DescribeAutoScalingInstancesInput{InstanceIds: []string{instance}})
	if err != nil {
		return "", "", apiError("DescribeAutoScalingInstances", err)
	}
	if len(out.AutoScalingInstances) == 0 {
		return "", "", nil
	}
	i := out.AutoScalingInstances[0]
	return aws.ToString(i.AutoScalingGroupName), aws.ToString(i.LifecycleState), nil
}

func (s *sdkCloud) heartbeat(ctx context.Context, group, hook, instance string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := s.asg.RecordLifecycleActionHeartbeat(ctx, &autoscaling.RecordLifecycleActionHeartbeatInput{
		AutoScalingGroupName: aws.String(group), LifecycleHookName: aws.String(hook), InstanceId: aws.String(instance),
	})
	return apiError("RecordLifecycleActionHeartbeat", err)
}

func (s *sdkCloud) complete(ctx context.Context, group, hook, instance string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := s.asg.CompleteLifecycleAction(ctx, &autoscaling.CompleteLifecycleActionInput{
		AutoScalingGroupName: aws.String(group), LifecycleHookName: aws.String(hook), InstanceId: aws.String(instance),
		LifecycleActionResult: aws.String("CONTINUE"),
	})
	return apiError("CompleteLifecycleAction", err)
}

func (s *sdkCloud) parameter(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	out, err := s.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(true)})
	if err != nil {
		return "", apiError("GetParameter", err)
	}
	if out.Parameter == nil {
		return "", errors.New("GetParameter: no parameter in the answer")
	}
	return aws.ToString(out.Parameter.Value), nil
}

func (s *sdkCloud) putMetrics(ctx context.Context, name string, exceeded, available float64) error {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	dim := []cwtypes.Dimension{{Name: aws.String(metricsDimKey), Value: aws.String(name)}}
	_, err := s.cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(metricsName),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("conntrack_allowance_exceeded"), Value: aws.Float64(exceeded), Unit: cwtypes.StandardUnitCount, Dimensions: dim},
			{MetricName: aws.String("conntrack_allowance_available"), Value: aws.Float64(available), Unit: cwtypes.StandardUnitCount, Dimensions: dim},
		},
	})
	return apiError("PutMetricData", err)
}

// apiError: "<Action>: HTTP <status> <code>: <message>" for an answer from AWS, "<Action>: <cause>" for a request
// that got none (the SDK's own wrapping left out: one console line).
func apiError(action string, err error) error {
	if err == nil {
		return nil
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return fmt.Errorf("%s: HTTP %d %s: %s", action, httpStatus(err), ae.ErrorCode(), ae.ErrorMessage())
	}
	if s := httpStatus(err); s >= 300 {
		return fmt.Errorf("%s: HTTP %d", action, s)
	}
	return fmt.Errorf("%s: %s", action, short(err))
}

func httpStatus(err error) int {
	var hs interface{ HTTPStatusCode() int }
	if errors.As(err, &hs) {
		return hs.HTTPStatusCode()
	}
	return 0
}

// short: an SDK error's cause, without the operation and middleware it went through
func short(err error) string {
	if s := httpStatus(err); s >= 300 {
		return fmt.Sprintf("HTTP %d", s)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "no answer in time"
	}
	var re *aws.RequestCanceledError
	if errors.As(err, &re) {
		return "no answer in time"
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ", "); i >= 0 && strings.HasPrefix(msg, "operation error ") {
		msg = msg[i+2:]
	}
	return msg
}
