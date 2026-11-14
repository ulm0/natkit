#!/bin/sh
# Registers a natkit disk image (make) as a UEFI AMI: S3 upload -> import-snapshot -> register-image.
# (`linuxkit push aws` can't: it registers x86_64 BIOS images only.)
#
#   scripts/ami.sh <image> <arm64|amd64> <name> [tag=value ...]
#   e.g. scripts/ami.sh out/natkit-arm64-efi.img arm64 natkit-v0.1.0-arm64 natkit.io/release=v0.1.0
#
# Needs: AWS credentials + AWS_REGION, NATKIT_BUCKET (S3 bucket in that region), and the `vmimport` service role
# (https://docs.aws.amazon.com/vm-import/latest/userguide/required-permissions.html). Prints the AMI ID.
set -eu

IMG=$1 ARCH=$2 NAME=$3
shift 3
: "${NATKIT_BUCKET:?S3 bucket for the import}"
case $ARCH in
arm64) EC2_ARCH=arm64 ;;
amd64) EC2_ARCH=x86_64 ;;
*) echo "arch must be arm64 or amd64" >&2 && exit 1 ;;
esac
KEY=import/$NAME.raw

if ami=$(aws ec2 describe-images --owners self --filters "Name=name,Values=$NAME" --query 'Images[0].ImageId' --output text) &&
	[ "$ami" != None ]; then
	echo "AMI $NAME exists: $ami" >&2
	echo "$ami"
	exit 0
fi

echo "uploading $IMG to s3://$NATKIT_BUCKET/$KEY" >&2
aws s3 cp --only-show-errors "$IMG" "s3://$NATKIT_BUCKET/$KEY"
trap 'aws s3 rm --only-show-errors "s3://$NATKIT_BUCKET/$KEY"' EXIT

task=$(aws ec2 import-snapshot --description "$NAME" \
	--disk-container "Format=raw,UserBucket={S3Bucket=$NATKIT_BUCKET,S3Key=$KEY}" \
	--query ImportTaskId --output text)
echo "import task $task" >&2
while :; do
	status=$(aws ec2 describe-import-snapshot-tasks --import-task-ids "$task" \
		--query 'ImportSnapshotTasks[0].SnapshotTaskDetail.[Status,SnapshotId,StatusMessage]' --output text)
	case $status in
	completed*) snap=$(echo "$status" | cut -f2) && break ;;
	deleted* | deleting*) echo "import failed: $status" >&2 && exit 1 ;;
	esac
	sleep 15
done
echo "snapshot $snap" >&2

tags="{Key=Name,Value=$NAME}"
snaptags="Key=Name,Value=$NAME"
for t in "$@"; do
	tags="$tags,{Key=${t%%=*},Value=${t#*=}}"
	snaptags="$snaptags Key=${t%%=*},Value=${t#*=}"
done
# shellcheck disable=SC2086 # one word per tag; boot-cleanup prunes snapshots by natkit.io/release
aws ec2 create-tags --resources "$snap" --tags $snaptags >/dev/null
aws ec2 register-image --name "$NAME" --description "natkit: LinuxKit NAT instance ($NAME)" \
	--architecture "$EC2_ARCH" --boot-mode uefi --ena-support --imds-support v2.0 \
	--virtualization-type hvm --root-device-name /dev/xvda \
	--block-device-mappings "DeviceName=/dev/xvda,Ebs={SnapshotId=$snap,VolumeType=gp3,DeleteOnTermination=true}" \
	--tag-specifications "ResourceType=image,Tags=[$tags]" \
	--query ImageId --output text
