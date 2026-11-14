# Mock EC2 IMDSv2 + EC2 Query API + SSM GetParameter + Auto Scaling's lifecycle hook calls for test/init.sh.
# User-data comes from /user-data, if mounted.
# The primary ENI's MAC is the test container's (docker run --mac-address); the static ENI appears in the metadata
# after AttachNetworkInterface. The ENI starts out on an old instance: DescribeNetworkInterfaces shows that attachment
# (or is denied, with NO_DESCRIBE set), DetachNetworkInterface starts the detach, and the first attach still fails.
# With ATTACHED set the instance already has it, as after a reboot.
# Auto Scaling: the instance is in the group natkit-test, Pending and then held by the hook natkit-ready
# (Pending:Wait); ASG_STATE=InService for an instance already in service, ASG_STATE=none for one in no group;
# NO_COMPLETE denies CompleteLifecycleAction. NO_REGION: the metadata has no region.
# GET /_calls lists the EC2 and Auto Scaling actions and their parameters.
import json, os
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qsl

TOKEN = "imds-token"
MAC, ENI = "02:42:ac:11:00:99", "eni-0aaaaaaaaaaaaaaaa"
STATIC_MAC, STATIC_ENI = "02:42:ac:11:00:98", "eni-0bbbbbbbbbbbbbbbb"
META = {
    "meta-data/placement/region": "us-east-1",
    "meta-data/instance-id": "i-0123456789abcdef0",
    "meta-data/mac": MAC,
    f"meta-data/network/interfaces/macs/{MAC}/interface-id": ENI,
    f"meta-data/network/interfaces/macs/{STATIC_MAC}/interface-id": STATIC_ENI,
    "meta-data/iam/security-credentials/": "natkit",
    "meta-data/iam/security-credentials/natkit": json.dumps(
        {"AccessKeyId": "AKIAEXAMPLE", "SecretAccessKey": "secret", "Token": "session"}),
}
PARAMS = {"/natkit/test/root-hash": "$6$salt$hash", "/natkit/test/plain": "hunter2",
          "/natkit/test/cw": '{"metrics": {"namespace": "natkit"}}', "/natkit/test/notjson": "[1, 2]"}
OLD_ATTACHMENT = "eni-attach-0ccccccccccccccc"
calls, attached, attach_tries, describes = [], bool(os.environ.get("ATTACHED")), 0, 0
IID, GROUP, HOOK = "i-0123456789abcdef0", "natkit-test", "natkit-ready"


def ok_xml(action):
    return f'<{action}Response xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><return>true</return></{action}Response>'.encode()


def err_xml(code, msg):
    return f"<Response><Errors><Error><Code>{code}</Code><Message>{msg}</Message></Error></Errors></Response>".encode()


def asg_err(code, msg):  # Auto Scaling's error shape, not EC2's
    return f"<ErrorResponse><Error><Type>Sender</Type><Code>{code}</Code><Message>{msg}</Message></Error></ErrorResponse>".encode()


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
        return 200, ok_xml(action)
    if action == "CompleteLifecycleAction" and p == {**held, "LifecycleActionResult": "CONTINUE"}:
        if os.environ.get("NO_COMPLETE"):
            return 403, asg_err("AccessDenied", "not authorized")
        return 200, ok_xml(action)
    return 400, asg_err("ValidationError", f"No active Lifecycle Action found with instance ID {IID}")


class H(BaseHTTPRequestHandler):
    def reply(self, code, body=b""):
        self.send_response(code)
        self.end_headers()
        self.wfile.write(body)

    def do_PUT(self):
        ok = self.path == "/latest/api/token" and self.headers.get("X-aws-ec2-metadata-token-ttl-seconds")
        self.reply(200 if ok else 400, TOKEN.encode() if ok else b"")

    def do_GET(self):
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
        if key == "meta-data/placement/region" and os.environ.get("NO_REGION"):
            return self.reply(404)
        self.reply(200, META[key].encode()) if key in META else self.reply(404)

    def do_POST(self):  # EC2 and SSM: SigV4-signed with the session token
        global attached, attach_tries
        auth = self.headers.get("Authorization", "")
        body = self.rfile.read(int(self.headers["Content-Length"]))
        if not auth.startswith("AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") or self.headers.get("x-amz-security-token") != "session":
            return self.reply(403, err_xml("AuthFailure", "unsigned"))
        if self.headers.get("X-Amz-Target") == "AmazonSSM.GetParameter":
            req = json.loads(body)
            if "/us-east-1/ssm/" not in auth or req.get("WithDecryption") is not True or req["Name"] not in PARAMS:
                return self.reply(400)
            return self.reply(200, json.dumps({"Parameter": {"Name": req["Name"], "Value": PARAMS[req["Name"]]}}).encode())
        p = dict(parse_qsl(body.decode()))
        action = p.pop("Action", "")
        if "/us-east-1/autoscaling/" in auth:
            return self.reply(*asg(action, p))
        if "/us-east-1/ec2/" not in auth:
            return self.reply(403, err_xml("AuthFailure", "wrong service"))
        if p.pop("Version", "") != "2016-11-15":
            return self.reply(400, err_xml("InvalidParameterValue", "version"))
        calls.append({"Action": action, **p})
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
        if action == "AttachNetworkInterface" and p == {"InstanceId": "i-0123456789abcdef0", "NetworkInterfaceId": STATIC_ENI, "DeviceIndex": "1"}:
            attach_tries += 1
            if attach_tries == 1:
                return self.reply(400, err_xml("InvalidNetworkInterface.InUse", "attached to i-old"))
            attached = True
            return self.reply(200, ok_xml(action))
        self.reply(400, err_xml("InvalidParameterValue", f"unexpected {action} {p}"))


HTTPServer(("", 8080), H).serve_forever()
