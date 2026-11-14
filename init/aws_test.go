package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go/encoding/cbor"
)

// awsServer: IMDSv2 and the four AWS APIs natkit calls, on the wire the SDK speaks: EC2's and Auto Scaling's Query
// APIs, SSM's JSON 1.1 and CloudWatch's RPC v2 CBOR. Every request is recorded.
type awsServer struct {
	t           *testing.T
	srv         *httptest.Server
	mu          sync.Mutex
	reqs        []awsRequest
	credsLate   int                                       // credential requests answered 404 first
	userData    string                                    // "" for none (404)
	answer      map[string]func(http.ResponseWriter) bool // by action: true when it answered
	credsAsked  int
	imdsNoToken int // IMDS requests without a token (IMDSv1)
}

type awsRequest struct {
	service, action string
	params          map[string]string // Query APIs
	json            map[string]any    // SSM
	cbor            cbor.Value        // CloudWatch
	auth, token, ua string
}

func newAWSServer(t *testing.T) *awsServer {
	s := &awsServer{t: t, answer: map[string]func(http.ResponseWriter) bool{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *awsServer) config() config {
	return config{imds: s.srv.URL, ec2: s.srv.URL, autoscaling: s.srv.URL, ssm: s.srv.URL, cloudwatch: s.srv.URL}
}

func (s *awsServer) cloud() (*sdkCloud, imdsMetadata) {
	md := newIMDS(s.srv.URL)
	return newCloud(s.config(), "us-east-1", md), imdsMetadata{md}
}

func (s *awsServer) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/latest/") {
		s.serveIMDS(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" { // PutMetricData's body, compressed by the SDK
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			s.t.Errorf("gzip: %v", err)
			return
		}
		body, _ = io.ReadAll(zr)
	}
	req := awsRequest{auth: r.Header.Get("Authorization"), token: r.Header.Get("X-Amz-Security-Token"), ua: r.Header.Get("User-Agent")}
	for _, svc := range []string{"ec2", "autoscaling", "ssm", "monitoring"} {
		if strings.Contains(req.auth, "/us-east-1/"+svc+"/aws4_request") {
			req.service = svc
		}
	}
	switch req.service {
	case "ec2", "autoscaling":
		v, _ := url.ParseQuery(string(body))
		req.params = map[string]string{}
		for k := range v {
			req.params[k] = v.Get(k)
		}
		req.action = req.params["Action"]
	case "ssm":
		req.action = strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.")
		_ = json.Unmarshal(body, &req.json)
	case "monitoring":
		req.action = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var err error
		if req.cbor, err = cbor.Decode(body); err != nil {
			s.t.Errorf("CBOR: %v (%s, %d bytes: %x)", err, r.Header.Get("Content-Encoding"), len(body), body)
		}
	default:
		w.WriteHeader(403)
		return
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	answer := s.answer[req.action]
	s.mu.Unlock()
	if answer != nil && answer(w) {
		return
	}
	switch req.service {
	case "ec2":
		io.WriteString(w, `<`+req.action+`Response xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><return>true</return></`+req.action+`Response>`)
	case "autoscaling":
		io.WriteString(w, `<`+req.action+`Response xmlns="http://autoscaling.amazonaws.com/doc/2011-01-01/"><`+req.action+`Result/>`+
			`<ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></`+req.action+`Response>`)
	case "ssm":
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		io.WriteString(w, `{"Parameter":{"Name":"/natkit/x","Value":"$6$salt$hash"}}`)
	case "monitoring":
		cborAnswer(w, 200, cbor.Map{})
	}
}

func cborAnswer(w http.ResponseWriter, status int, v cbor.Map) {
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(status)
	w.Write(cbor.Encode(v))
}

func (s *awsServer) serveIMDS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/latest/api/token" {
		if r.Method != http.MethodPut || r.Header.Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds") == "" {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", r.Header.Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds"))
		io.WriteString(w, "tok")
		return
	}
	if r.Header.Get("X-Aws-Ec2-Metadata-Token") != "tok" {
		s.mu.Lock()
		s.imdsNoToken++
		s.mu.Unlock()
		w.WriteHeader(401) // IMDSv1 is off, as httpTokens=required has it
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/latest/") {
	case "user-data":
		if s.userData == "" {
			w.WriteHeader(404)
			return
		}
		io.WriteString(w, s.userData)
	case "meta-data/instance-id":
		io.WriteString(w, testIID)
	case "meta-data/mac":
		io.WriteString(w, testMAC)
	case "meta-data/network/interfaces/macs/":
		io.WriteString(w, testMAC+"/")
	case "meta-data/network/interfaces/macs/" + testMAC + "/interface-id":
		io.WriteString(w, testENI)
	case "dynamic/instance-identity/document":
		io.WriteString(w, `{"region": "us-east-1", "instanceId": "`+testIID+`", "availabilityZone": "us-east-1a"}`)
	case "meta-data/iam/security-credentials/":
		io.WriteString(w, "natkit\n")
	case "meta-data/iam/security-credentials/natkit":
		s.mu.Lock()
		s.credsAsked++
		late := s.credsAsked <= s.credsLate
		s.mu.Unlock()
		if late {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"Code": "Success", "Type": "AWS-HMAC", "AccessKeyId": "AKIAEXAMPLE", "SecretAccessKey": "secret", "Token": "session",
			"LastUpdated": time.Now().UTC().Format(time.RFC3339), "Expiration": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	default:
		w.WriteHeader(404)
	}
}

func (s *awsServer) last() awsRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		s.t.Fatal("no AWS request")
	}
	return s.reqs[len(s.reqs)-1]
}

func TestSDKEC2(t *testing.T) {
	s := newAWSServer(t)
	cl, _ := s.cloud()
	ctx := context.Background()
	for _, tc := range []struct {
		call func() error
		want map[string]string
	}{
		{func() error { return cl.sourceDestCheckOff(ctx, testENI) },
			map[string]string{"Action": "ModifyNetworkInterfaceAttribute", "NetworkInterfaceId": testENI, "SourceDestCheck.Value": "false"}},
		{func() error { return cl.detach(ctx, "eni-attach-0ccccccccccccccc") },
			map[string]string{"Action": "DetachNetworkInterface", "AttachmentId": "eni-attach-0ccccccccccccccc", "Force": "true"}},
		{func() error { return cl.attach(ctx, testIID, testStaticENI) },
			map[string]string{"Action": "AttachNetworkInterface", "InstanceId": testIID, "NetworkInterfaceId": testStaticENI, "DeviceIndex": "1"}},
		{func() error { return cl.associateAddress(ctx, "eipalloc-0123", testENI) },
			map[string]string{"Action": "AssociateAddress", "AllocationId": "eipalloc-0123", "NetworkInterfaceId": testENI, "AllowReassociation": "true"}},
	} {
		if err := tc.call(); err != nil {
			t.Fatalf("%s: %v", tc.want["Action"], err)
		}
		r := s.last()
		if r.params["Version"] != "2016-11-15" {
			t.Errorf("%s: version %q", tc.want["Action"], r.params["Version"])
		}
		delete(r.params, "Version")
		if !reflect.DeepEqual(r.params, tc.want) {
			t.Errorf("params %v, want %v", r.params, tc.want)
		}
		// signed with the instance role, the session token in a header (never in the URL or the body)
		if !strings.HasPrefix(r.auth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") || r.token != "session" {
			t.Errorf("auth %q, token %q", r.auth, r.token)
		}
		if !strings.Contains(r.ua, "app/natkit-") {
			t.Errorf("User-Agent %q: no natkit app ID for CloudTrail", r.ua)
		}
	}

	s.answer["DescribeNetworkInterfaces"] = func(w http.ResponseWriter) bool {
		io.WriteString(w, `<DescribeNetworkInterfacesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><networkInterfaceSet><item>`+
			`<networkInterfaceId>`+testStaticENI+`</networkInterfaceId><attachment><attachmentId>eni-attach-0ccccccccccccccc</attachmentId>`+
			`<instanceId>i-0old</instanceId><status>attached</status></attachment></item></networkInterfaceSet></DescribeNetworkInterfacesResponse>`)
		return true
	}
	if att, err := cl.attachment(ctx, testStaticENI); err != nil || att != "eni-attach-0ccccccccccccccc" {
		t.Errorf("attachment %q %v", att, err)
	}
	if got := s.last().params["NetworkInterfaceId.1"]; got != testStaticENI {
		t.Errorf("NetworkInterfaceId.1 %q", got)
	}
	s.answer["DescribeNetworkInterfaces"] = func(w http.ResponseWriter) bool {
		io.WriteString(w, `<DescribeNetworkInterfacesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><networkInterfaceSet><item>`+
			`<networkInterfaceId>`+testStaticENI+`</networkInterfaceId></item></networkInterfaceSet></DescribeNetworkInterfacesResponse>`)
		return true
	}
	if att, err := cl.attachment(ctx, testStaticENI); err != nil || att != "" {
		t.Errorf("a free ENI: attachment %q %v", att, err)
	}
}

func TestSDKAutoScaling(t *testing.T) {
	s := newAWSServer(t)
	cl, _ := s.cloud()
	ctx := context.Background()
	s.answer["DescribeAutoScalingInstances"] = func(w http.ResponseWriter) bool {
		io.WriteString(w, `<DescribeAutoScalingInstancesResponse xmlns="http://autoscaling.amazonaws.com/doc/2011-01-01/"><DescribeAutoScalingInstancesResult>`+
			`<AutoScalingInstances><member><HealthStatus>HEALTHY</HealthStatus><AutoScalingGroupName>natkit-test</AutoScalingGroupName>`+
			`<InstanceId>`+testIID+`</InstanceId><LifecycleState>Pending:Wait</LifecycleState></member></AutoScalingInstances>`+
			`</DescribeAutoScalingInstancesResult></DescribeAutoScalingInstancesResponse>`)
		return true
	}
	g, st, err := cl.asgInstance(ctx, testIID)
	if err != nil || g != "natkit-test" || st != "Pending:Wait" {
		t.Errorf("asgInstance %q %q %v", g, st, err)
	}
	if r := s.last(); r.params["InstanceIds.member.1"] != testIID || r.params["Version"] != "2011-01-01" {
		t.Errorf("params %v", r.params)
	}
	s.answer["DescribeAutoScalingInstances"] = func(w http.ResponseWriter) bool {
		io.WriteString(w, `<DescribeAutoScalingInstancesResponse><DescribeAutoScalingInstancesResult><AutoScalingInstances/></DescribeAutoScalingInstancesResult></DescribeAutoScalingInstancesResponse>`)
		return true
	}
	if g, st, err := cl.asgInstance(ctx, testIID); err != nil || g != "" || st != "" {
		t.Errorf("in no group: %q %q %v", g, st, err)
	}
	if err := cl.heartbeat(ctx, "natkit-test", "natkit-ready", testIID); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Action": "RecordLifecycleActionHeartbeat", "Version": "2011-01-01", "AutoScalingGroupName": "natkit-test", "LifecycleHookName": "natkit-ready", "InstanceId": testIID}
	if r := s.last(); !reflect.DeepEqual(r.params, want) {
		t.Errorf("params %v", r.params)
	}
	if err := cl.complete(ctx, "natkit-test", "natkit-ready", testIID); err != nil {
		t.Fatal(err)
	}
	want["Action"], want["LifecycleActionResult"] = "CompleteLifecycleAction", "CONTINUE"
	if r := s.last(); !reflect.DeepEqual(r.params, want) {
		t.Errorf("params %v", r.params)
	}
}

func TestSDKSSM(t *testing.T) {
	s := newAWSServer(t)
	cl, _ := s.cloud()
	v, err := cl.parameter(context.Background(), "/natkit/x")
	if err != nil || v != "$6$salt$hash" {
		t.Fatalf("parameter %q %v", v, err)
	}
	r := s.last()
	if r.action != "GetParameter" || r.json["Name"] != "/natkit/x" || r.json["WithDecryption"] != true {
		t.Errorf("request %s %v", r.action, r.json)
	}
}

func TestSDKCloudWatch(t *testing.T) {
	s := newAWSServer(t)
	cl, _ := s.cloud()
	if err := cl.putMetrics(context.Background(), "nat-a", 5, 1234); err != nil {
		t.Fatal(err)
	}
	r := s.last()
	if r.action != "PutMetricData" {
		t.Fatalf("action %q", r.action)
	}
	m, ok := r.cbor.(cbor.Map)
	if !ok || m["Namespace"] != cbor.String("natkit") {
		t.Fatalf("body %#v", r.cbor)
	}
	data := m["MetricData"].(cbor.List)
	got := map[string]float64{}
	for _, d := range data {
		d := d.(cbor.Map)
		v, _ := cbor.AsFloat64(d["Value"])
		got[string(d["MetricName"].(cbor.String))] = v
		dim := d["Dimensions"].(cbor.List)[0].(cbor.Map)
		if d["Unit"] != cbor.String("Count") || dim["Name"] != cbor.String("Name") || dim["Value"] != cbor.String("nat-a") {
			t.Errorf("datum %#v", d)
		}
	}
	if !reflect.DeepEqual(got, map[string]float64{"conntrack_allowance_exceeded": 5, "conntrack_allowance_available": 1234}) {
		t.Errorf("metrics %v", got)
	}
}

func TestSDKErrors(t *testing.T) {
	s := newAWSServer(t)
	cl, _ := s.cloud()
	ctx := context.Background()
	s.answer["AttachNetworkInterface"] = func(w http.ResponseWriter) bool {
		w.WriteHeader(400)
		io.WriteString(w, `<Response><Errors><Error><Code>InvalidParameterValue</Code><Message>Network interface 'eni-0b' is currently in use.</Message></Error></Errors><RequestID>r</RequestID></Response>`)
		return true
	}
	s.answer["CompleteLifecycleAction"] = func(w http.ResponseWriter) bool {
		w.WriteHeader(403)
		io.WriteString(w, `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>not authorized</Message></Error></ErrorResponse>`)
		return true
	}
	s.answer["PutMetricData"] = func(w http.ResponseWriter) bool {
		cborAnswer(w, 500, cbor.Map{"__type": cbor.String("InternalFailure"), "message": cbor.String("try again")})
		return true
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{cl.attach(ctx, testIID, testStaticENI), "AttachNetworkInterface: HTTP 400 InvalidParameterValue: Network interface 'eni-0b' is currently in use."},
		{cl.complete(ctx, "g", "h", testIID), "CompleteLifecycleAction: HTTP 403 AccessDenied: not authorized"},
		{cl.putMetrics(ctx, "nat-a", 0, 1), "PutMetricData: HTTP 500 InternalFailure: try again"},
	} {
		if tc.err == nil || tc.err.Error() != tc.want {
			t.Errorf("%v, want %q", tc.err, tc.want)
		}
	}
	// a request that gets no answer: the cause, on one line
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	c := s.config()
	c.cloudwatch = u
	err := newCloud(c, "us-east-1", newIMDS(s.srv.URL)).putMetrics(ctx, "nat-a", 0, 1)
	if err == nil || !strings.HasPrefix(err.Error(), "PutMetricData: Post \"") || !strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "\n") {
		t.Errorf("transport error %q", err)
	}
	// an answer it can't read: the cause, not just a status that says all is well
	s.answer["RecordLifecycleActionHeartbeat"] = func(w http.ResponseWriter) bool {
		io.WriteString(w, `<Response><return>true</return></Response>`)
		return true
	}
	if err := cl.heartbeat(ctx, "g", "h", testIID); err == nil || err.Error() == "RecordLifecycleActionHeartbeat: HTTP 200" {
		t.Errorf("unreadable answer: %v", err)
	}
	if apiError("X", nil) != nil {
		t.Error("apiError(nil)")
	}
}

func TestIMDS(t *testing.T) {
	s := newAWSServer(t)
	_, md := s.cloud()
	ctx := context.Background()
	if v, err := md.get(ctx, "instance-id"); err != nil || v != testIID {
		t.Errorf("instance-id %q %v", v, err)
	}
	if v, err := md.region(ctx); err != nil || v != "us-east-1" {
		t.Errorf("region %q %v", v, err)
	}
	if b, err := md.userData(ctx); err != nil || b != nil {
		t.Errorf("no user-data: %q %v", b, err)
	}
	s.userData = "nat64: false\n"
	if b, err := md.userData(ctx); err != nil || string(b) != "nat64: false\n" {
		t.Errorf("user-data %q %v", b, err)
	}
	if _, err := md.get(ctx, "nothing/here"); err == nil || err.Error() != "instance metadata nothing/here: HTTP 404" {
		t.Errorf("missing path: %v", err)
	}
	if s.imdsNoToken != 0 {
		t.Errorf("%d IMDSv1 requests", s.imdsNoToken)
	}
}

// regression: credentials missing right after launch were kept as "null" by the shell version, and every retry failed
func TestCredentialsLate(t *testing.T) {
	s := newAWSServer(t)
	s.credsLate = 1
	cl, _ := s.cloud()
	if err := cl.sourceDestCheckOff(context.Background(), testENI); err == nil {
		t.Fatal("a call without credentials went through")
	}
	if err := cl.sourceDestCheckOff(context.Background(), testENI); err != nil {
		t.Fatalf("credentials not fetched again: %v", err)
	}
	if err := cl.sourceDestCheckOff(context.Background(), testENI); err != nil || s.credsAsked != 2 {
		t.Errorf("credentials asked %d times (cached after the first that worked?), %v", s.credsAsked, err)
	}
}

// an instance without a role (no instance profile): said as such, not as the IMDS 404 behind it
func TestNoInstanceRole(t *testing.T) {
	s := newAWSServer(t)
	s.credsLate = 1 << 30
	cl, _ := s.cloud()
	ctx := context.Background()
	_, ssmErr := cl.parameter(ctx, "/natkit/x")
	for _, tc := range []struct {
		err    error
		action string
	}{
		{cl.sourceDestCheckOff(ctx, testENI), "ModifyNetworkInterfaceAttribute"},
		{ssmErr, "GetParameter"},
		{cl.putMetrics(ctx, "nat-a", 0, 1), "PutMetricData"},
	} {
		if want := tc.action + ": no instance role credentials (an instance profile?): HTTP 404"; tc.err == nil || tc.err.Error() != want {
			t.Errorf("%v, want %q", tc.err, want)
		}
	}
	if n := len(s.reqs); n != 0 {
		t.Errorf("%d requests sent unsigned", n)
	}
}

// natkit-init end to end through the SDK, against the fake IMDS and EC2: the user-data, the metadata, a signed call
func TestRunInitSDK(t *testing.T) {
	s := newAWSServer(t)
	s.userData = "eip_id: eipalloc-0123\n"
	h := newFakeHost(t)
	c := s.config()
	c.console, c.testing = h.path("/console"), true
	if st := runInit(h.host, c); st != 0 {
		t.Fatalf("status %d, console %q\n%s", st, h.file("/console"), h.err.String())
	}
	if got := strings.TrimSpace(h.file("/console")); got != "natkit: ready, public eth0 private eth0, eip eipalloc-0123, nat64 off, cwagent off" {
		t.Errorf("console %q", got)
	}
	r := s.last()
	if r.action != "AssociateAddress" || r.params["AllocationId"] != "eipalloc-0123" || r.params["NetworkInterfaceId"] != testENI {
		t.Errorf("request %+v", r)
	}
}

func TestShort(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "no answer in time"},
		{&aws.RequestCanceledError{Err: context.Canceled}, "no answer in time"},
		{errors.New("operation error EC2: X, https response error, failed to sign: no credentials"), "failed to sign: no credentials"},
		{errors.New("plain"), "plain"},
	} {
		if got := short(tc.err); got != tc.want {
			t.Errorf("short(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestCommand(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		name string
		args []string
	}{
		{[]string{"/usr/bin/natkit-init"}, "natkit-init", []string{}},
		{[]string{"/usr/bin/natkit-metrics"}, "natkit-metrics", []string{}},
		{[]string{"natkit", "cwagent", "x"}, "natkit-cwagent", []string{"x"}},
		{[]string{"natkit"}, "natkit", []string{}},
	} {
		name, args := command(tc.argv)
		if name != tc.name || !reflect.DeepEqual(args, tc.args) {
			t.Errorf("%v: %q %q", tc.argv, name, args)
		}
	}
}
