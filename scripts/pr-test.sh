#!/bin/sh
# The EC2 tests of a pull request, from a laptop: what build.yml's ami job runs on a tag. CI can't run them for a
# branch: its role only takes main and release tags (tofu/ci), so that a branch's workflow can't use it.
# The pull request's `build` run has the disk images (its image jobs' artifacts). They become AMIs named
# natkit-pr<n>-<sha>-<arch>; each is boot-tested (scripts/boot-test.sh), and the arm64 one module-tested
# (scripts/module-test.sh). The AMIs and their snapshots are removed on exit, pass or fail.
#
#   scripts/pr-test.sh <pull request number>
#
# Run it from a checkout of the pull request's head: the test scripts and test/module are this tree's.
# Needs gh, and what the three scripts need: the aws CLI with credentials + AWS_REGION, tofu (or TF=terraform),
# NATKIT_BUCKET, NATKIT_TEST_PUBLIC_SUBNET, NATKIT_TEST_PRIVATE_SUBNET and NATKIT_TEST_INSTANCE_PROFILE.
# One test at a time routes the private subnet: this waits while a CI run or another laptop has it.
set -eu
cd "$(dirname "$0")/.."
PR=$1
: "${NATKIT_BUCKET:?}" "${NATKIT_TEST_PUBLIC_SUBNET:?}" "${NATKIT_TEST_PRIVATE_SUBNET:?}" "${NATKIT_TEST_INSTANCE_PROFILE:?}"

sha=$(gh pr view "$PR" --json headRefOid --jq .headRefOid)
[ "$(git rev-parse HEAD)" = "$sha" ] || { echo "check out pull request $PR's head ($sha) first" >&2 && exit 1; }
run=$(gh run list --workflow build --event pull_request --commit "$sha" --status success --limit 1 --json databaseId --jq '.[0].databaseId')
[ -n "$run" ] || { echo "no build run of $sha has passed yet" >&2 && exit 1; }

D=$(mktemp -d)
amis=''
cleanup() {
	set +e
	for ami in $amis; do
		echo "deregistering $ami and its snapshots" >&2
		aws ec2 deregister-image --image-id "$ami" --delete-associated-snapshots >/dev/null
	done
	rm -rf "$D"
}
trap cleanup EXIT

# one artifact per architecture (natkit-<arch>-<the run's merge commit>): the image and its checksum
gh run download "$run" --pattern 'natkit-*' --dir "$D"
short=$(git rev-parse --short=7 HEAD)
for arch in arm64 amd64; do
	img=$(echo "$D"/natkit-"$arch"-*/natkit-"$arch"-efi.img)
	(cd "$(dirname "$img")" && shasum -a 256 -c "SHA256SUMS-$arch") >&2
	# not natkit.io/release: CI's boot-cleanup deregisters what has that tag and isn't a release
	ami=$(scripts/ami.sh "$img" "$arch" "natkit-pr$PR-$short-$arch" "natkit.io/pr=$PR")
	amis="$amis $ami"
	echo "== $arch: $ami, boot test" >&2
	NATKIT_BOOT_TEST_TAG="pr$PR-$short-$arch" scripts/boot-test.sh "$ami" "$arch"
	if [ "$arch" = arm64 ]; then
		echo "== $arch: module test" >&2
		NATKIT_MODULE_TEST_TAG="pr$PR-$short" scripts/module-test.sh "$ami" "$arch"
	fi
done
echo "pull request $PR ($short): the EC2 tests passed"
