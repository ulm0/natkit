# Mock EC2 IMDSv2 + EC2 Query API + SSM GetParameter + Auto Scaling's lifecycle hook calls + CloudWatch
# PutMetricData for test/init.sh, on the wire the AWS SDK for Go v2 speaks: EC2's and Auto Scaling's Query APIs,
# SSM's JSON 1.1 and CloudWatch's RPC v2 CBOR (gzipped). User-data comes from /user-data, if mounted.
# The primary ENI's MAC is the test container's (docker run --mac-address); the static ENI appears in the metadata
# after AttachNetworkInterface. The ENI starts out on an old instance: DescribeNetworkInterfaces shows that attachment
# (or is denied, with NO_DESCRIBE set), DetachNetworkInterface starts the detach, and the first attach still fails.
# With ATTACHED set the instance already has it, as after a reboot.
# Auto Scaling: the instance is in the group natkit-test, Pending and then held by the hook natkit-ready
# (Pending:Wait); ASG_STATE=InService for an instance already in service, ASG_STATE=none for one in no group;
# NO_COMPLETE denies CompleteLifecycleAction. NO_REGION: the metadata has no region. THROTTLE: the first
# DescribeNetworkInterfaces and the first DetachNetworkInterface are throttled. CREDS_LATE: the instance role's
# credentials are missing the first time they're asked for, as right after launch. CW_FAIL=<n>: the nth
# PutMetricData fails (500), recorded all the same.
# GET /_calls lists the EC2, Auto Scaling and CloudWatch actions and their parameters (CloudWatch's flattened as
# the Query API has them).
import gzip, json, os, struct
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qsl

TOKEN = "imds-token"
MAC, ENI = "02:42:ac:11:00:99", "eni-0aaaaaaaaaaaaaaaa"
STATIC_MAC, STATIC_ENI = "02:42:ac:11:00:98", "eni-0bbbbbbbbbbbbbbbb"
IID, GROUP, HOOK = "i-0123456789abcdef0", "natkit-test", "natkit-ready"
META = {
    "meta-data/placement/region": "us-east-1",
    "dynamic/instance-identity/document": json.dumps({"region": "us-east-1", "instanceId": IID, "availabilityZone": "us-east-1a"}),
    "meta-data/instance-id": IID,
    "meta-data/mac": MAC,
    f"meta-data/network/interfaces/macs/{MAC}/interface-id": ENI,
    f"meta-data/network/interfaces/macs/{STATIC_MAC}/interface-id": STATIC_ENI,
    "meta-data/iam/security-credentials/": "natkit",
    "meta-data/iam/security-credentials/natkit": json.dumps({
        "Code": "Success", "Type": "AWS-HMAC", "AccessKeyId": "AKIAEXAMPLE", "SecretAccessKey": "secret",
        "Token": "session", "LastUpdated": "2026-01-01T00:00:00Z", "Expiration": "2099-01-01T00:00:00Z"}),
}
PARAMS = {"/natkit/test/root-hash": "$6$salt$hash", "/natkit/test/plain": "hunter2",
          "/natkit/test/cw": '{"metrics": {"namespace": "natkit"}}', "/natkit/test/notjson": "[1, 2]"}
OLD_ATTACHMENT = "eni-attach-0ccccccccccccccc"
calls, attached, attach_tries, describes = [], bool(os.environ.get("ATTACHED")), 0, 0
throttled, creds_asked = set(), 0


def ok_xml(action):
    return f'<{action}Response xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><return>true</return></{action}Response>'.encode()


def err_xml(code, msg):
    return f"<Response><Errors><Error><Code>{code}</Code><Message>{msg}</Message></Error></Errors></Response>".encode()


def asg_ok(action):  # Auto Scaling's success shape (awsquery), not EC2's
    return (f'<{action}Response xmlns="http://autoscaling.amazonaws.com/doc/2011-01-01/"><{action}Result/>'
            f"<ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></{action}Response>").encode()


def asg_err(code, msg):  # Auto Scaling's error shape, not EC2's
    return f"<ErrorResponse><Error><Type>Sender</Type><Code>{code}</Code><Message>{msg}</Message></Error></ErrorResponse>".encode()


# CBOR (RFC 8949), as much as CloudWatch's RPC v2 needs: decoding a request, encoding a map of strings
def cbor_decode(b, i=0):
    ib = b[i]
    i += 1
    major, info = ib >> 5, ib & 31
    if major == 7:
        if info in (20, 21):
            return info == 21, i
        if info in (22, 23):
            return None, i
        size, fmt = {25: (2, ">e"), 26: (4, ">f"), 27: (8, ">d")}[info]
        return struct.unpack(fmt, b[i:i + size])[0], i + size
    if info < 24:
        n = info
    elif info == 31:
        n = None  # indefinite length
    else:
        size = {24: 1, 25: 2, 26: 4, 27: 8}[info]
        n, i = int.from_bytes(b[i:i + size], "big"), i + size
    if major == 0:
        return n, i
    if major == 1:
        return -1 - n, i
    if major in (2, 3):
        if n is None:
            parts = []
            while b[i] != 0xFF:
                p, i = cbor_decode(b, i)
                parts.append(p)
            return (b"" if major == 2 else "").join(parts), i + 1
        s = b[i:i + n]
        return (bytes(s) if major == 2 else s.decode()), i + n
    if major in (4, 5):
        out, count = ([] if major == 4 else {}), 0
        while (count < n) if n is not None else b[i] != 0xFF:
            v, i = cbor_decode(b, i)
            if major == 5:
                out[v], i = cbor_decode(b, i)
            else:
                out.append(v)
            count += 1
        return out, (i if n is not None else i + 1)
    if major == 6:  # a tag: its value
        return cbor_decode(b, i)
    raise ValueError(f"CBOR major type {major}")


def cbor_text(s):
    e = s.encode()
    return (bytes([0x60 + len(e)]) if len(e) < 24 else bytes([0x78, len(e)])) + e


def cbor_map(d):
    return bytes([0xA0 + len(d)]) + b"".join(cbor_text(k) + cbor_text(v) for k, v in d.items())


def flat_metrics(req):  # PutMetricData's parameters as the Query API names them: what test/init.sh compares
    p = {"Action": "PutMetricData", "Namespace": req.get("Namespace")}
    for n, d in enumerate(req.get("MetricData", []), 1):
        m = f"MetricData.member.{n}."
        v = d.get("Value")
        p[m + "MetricName"] = d.get("MetricName")
        p[m + "Value"] = str(int(v)) if isinstance(v, float) and v.is_integer() else str(v)
        p[m + "Unit"] = d.get("Unit")
        for k, dim in enumerate(d.get("Dimensions", []), 1):
            p[m + f"Dimensions.member.{k}.Name"] = dim.get("Name")
            p[m + f"Dimensions.member.{k}.Value"] = dim.get("Value")
    return p


def asg(action, p):
    global describes
    if p.pop("Version", "") != "2011-01-01":
        return 400, asg_err("ValidationError", "version")
    calls.append({"Action": action, **p})
    if action == "DescribeAutoScalingInstances" and p == {"InstanceIds.member.1": IID}:
        describes += 1
        state = os.environ.get("ASG_STATE") or ("Pending" if describes == 1 else "Pending:Wait")
        member = "" if state == "none" else (
            f"<member><HealthStatus>HEALTHY</HealthStatus><AutoScalingGroupName>{GROUP}</AutoScalingGroupName>"
            f"<InstanceId>{IID}</InstanceId><LifecycleState>{state}</LifecycleState></member>")
        return 200, (
            '<DescribeAutoScalingInstancesResponse xmlns="http://autoscaling.amazonaws.com/doc/2011-01-01/">'
            f"<DescribeAutoScalingInstancesResult><AutoScalingInstances>{member}</AutoScalingInstances>"
            "</DescribeAutoScalingInstancesResult></DescribeAutoScalingInstancesResponse>").encode()
    held = {"AutoScalingGroupName": GROUP, "LifecycleHookName": HOOK, "InstanceId": IID}
    if action == "RecordLifecycleActionHeartbeat" and p == held:
        return 200, asg_ok(action)
    if action == "CompleteLifecycleAction" and p == {**held, "LifecycleActionResult": "CONTINUE"}:
        if os.environ.get("NO_COMPLETE"):
            return 403, asg_err("AccessDenied", "not authorized")
        return 200, asg_ok(action)
    return 400, asg_err("ValidationError", f"No active Lifecycle Action found with instance ID {IID}")


class H(BaseHTTPRequestHandler):
    def reply(self, code, body=b"", headers=()):
        self.send_response(code)
        for k, v in headers:
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def do_PUT(self):
        ttl = self.headers.get("X-aws-ec2-metadata-token-ttl-seconds")
        if self.path != "/latest/api/token" or not ttl:
            return self.reply(400)
        self.reply(200, TOKEN.encode(), [("X-aws-ec2-metadata-token-ttl-seconds", ttl)])

    def do_GET(self):
        global creds_asked
        if self.path == "/_calls":
            return self.reply(200, json.dumps(calls).encode())
        if self.headers.get("X-aws-ec2-metadata-token") != TOKEN:
            return self.reply(401)  # IMDSv1 is off, like httpTokens=required
        key = self.path.removeprefix("/latest/")
        if key == "user-data":
            if not os.path.exists("/user-data"):
                return self.reply(404)
            return self.reply(200, open("/user-data", "rb").read())
        if key == "meta-data/network/interfaces/macs/":
            return self.reply(200, (MAC + "/\n" + (STATIC_MAC + "/" if attached else "")).encode())
        if key in ("meta-data/placement/region", "dynamic/instance-identity/document") and os.environ.get("NO_REGION"):
            return self.reply(404)
        if key == "meta-data/iam/security-credentials/natkit":
            creds_asked += 1
            if os.environ.get("CREDS_LATE") and creds_asked == 1:
                return self.reply(404)
        self.reply(200, META[key].encode()) if key in META else self.reply(404)

    def do_POST(self):  # EC2, Auto Scaling, SSM and CloudWatch: SigV4-signed with the session token
        global attached, attach_tries
        auth = self.headers.get("Authorization", "")
        body = self.rfile.read(int(self.headers["Content-Length"]))
        if self.headers.get("Content-Encoding") == "gzip":
            body = gzip.decompress(body)
        if not auth.startswith("AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") or self.headers.get("x-amz-security-token") != "session":
            return self.reply(403, err_xml("AuthFailure", "unsigned"))
        if self.headers.get("X-Amz-Target") == "AmazonSSM.GetParameter":
            req = json.loads(body)
            if "/us-east-1/ssm/" not in auth or req.get("WithDecryption") is not True or req["Name"] not in PARAMS:
                return self.reply(400)
            return self.reply(200, json.dumps({"Parameter": {"Name": req["Name"], "Value": PARAMS[req["Name"]]}}).encode())
        if "/us-east-1/monitoring/" in auth:
            cbor = [("Smithy-Protocol", "rpc-v2-cbor"), ("Content-Type", "application/cbor")]
            if not self.path.endswith("/operation/PutMetricData"):
                return self.reply(400, cbor_map({"__type": "InvalidAction", "message": self.path}), cbor)
            req, _ = cbor_decode(body)
            calls.append(flat_metrics(req))
            if os.environ.get("CW_FAIL") == str(sum(c["Action"] == "PutMetricData" for c in calls)):
                return self.reply(500, cbor_map({"__type": "InternalFailure", "message": "try again"}), cbor)
            return self.reply(200, cbor_map({}), cbor)
        p = dict(parse_qsl(body.decode()))
        action = p.pop("Action", "")
        if "/us-east-1/autoscaling/" in auth:
            return self.reply(*asg(action, p))
        if "/us-east-1/ec2/" not in auth:
            return self.reply(403, err_xml("AuthFailure", "wrong service"))
        if p.pop("Version", "") != "2016-11-15":
            return self.reply(400, err_xml("InvalidParameterValue", "version"))
        calls.append({"Action": action, **p})
        if os.environ.get("THROTTLE") and action in ("DescribeNetworkInterfaces", "DetachNetworkInterface") and action not in throttled:
            throttled.add(action)
            return self.reply(503, err_xml("RequestLimitExceeded", "Request limit exceeded."))
        if action == "AssociateAddress" and p == {"AllocationId": "eipalloc-0123", "NetworkInterfaceId": ENI, "AllowReassociation": "true"}:
            return self.reply(200, ok_xml(action))
        if action == "ModifyNetworkInterfaceAttribute" and p == {"NetworkInterfaceId": ENI, "SourceDestCheck.Value": "false"}:
            return self.reply(200, ok_xml(action))
        if action == "DescribeNetworkInterfaces" and p == {"NetworkInterfaceId.1": STATIC_ENI}:
            if os.environ.get("NO_DESCRIBE"):
                return self.reply(403, err_xml("UnauthorizedOperation", "not authorized"))
            return self.reply(200, (
                '<DescribeNetworkInterfacesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><networkInterfaceSet><item>'
                f"<networkInterfaceId>{STATIC_ENI}</networkInterfaceId><attachment><attachmentId>{OLD_ATTACHMENT}</attachmentId>"
                "<instanceId>i-0old</instanceId><status>attached</status></attachment></item></networkInterfaceSet>"
                "</DescribeNetworkInterfacesResponse>").encode())
        if action == "DetachNetworkInterface" and p == {"AttachmentId": OLD_ATTACHMENT, "Force": "true"}:
            return self.reply(200, ok_xml(action))
        if action == "AttachNetworkInterface" and p == {"InstanceId": IID, "NetworkInterfaceId": STATIC_ENI, "DeviceIndex": "1"}:
            attach_tries += 1
            if attach_tries == 1:
                return self.reply(400, err_xml("InvalidNetworkInterface.InUse", "attached to i-old"))
            attached = True
            return self.reply(200, ok_xml(action))
        self.reply(400, err_xml("InvalidParameterValue", f"unexpected {action} {p}"))


if __name__ == "__main__":
    HTTPServer(("", 8080), H).serve_forever()
