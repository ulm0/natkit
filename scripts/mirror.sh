#!/bin/sh
# scripts/mirror.sh <tag> [remote]: tofu/nat at <tag>, as the root of the mirror ulm0/terraform-aws-natkit, tagged
# <tag>. The OpenTofu registry wants a repository named {owner}/terraform-{target}-{name} with the module at its root.
# Each mirror commit holds only the module, a README and the LICENSE, on top of the mirror's main. A tag the mirror
# already has is left as it is (a rerun of the release job). The release job runs it with a deploy key that can write
# the mirror only; the earlier releases were seeded by hand with it.
set -eu
tag=$1
remote=${2:-git@github.com:ulm0/terraform-aws-natkit.git}

if git ls-remote --exit-code --tags "$remote" "refs/tags/$tag" >/dev/null; then
  echo "$tag is already on the mirror"
  exit 0
fi
rev=$(git rev-parse --verify "$tag^{commit}")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git -C "$work" init -q -b main
# the mirror's main as the parent, its files as the index; none yet on the first push
if git -C "$work" fetch -q --depth=1 "$remote" main 2>/dev/null; then git -C "$work" reset -q --soft FETCH_HEAD; fi
git archive "$rev:tofu/nat" | tar -x -C "$work"
rm -f "$work/.terraform-docs.yml" # writes natkit's docs page, outside the module
git show "$rev:LICENSE" >"$work/LICENSE"
cat >"$work/README.md" <<EOF
# terraform-aws-natkit

The OpenTofu module of [natkit](https://github.com/ulm0/natkit): a NAT instance in an Auto Scaling group of one, with
a static ENI the private route tables point at, and optionally an EIP.

This repository is a copy of [\`tofu/nat\`](https://github.com/ulm0/natkit/tree/$tag/tofu/nat) at natkit $tag, made by
natkit's release job for the OpenTofu registry. Send issues and pull requests to
[ulm0/natkit](https://github.com/ulm0/natkit).

Inputs, outputs and an example: [natkit's docs](https://natkit.ulm0.com/docs/deploy/opentofu/).
EOF

date=$(git log -1 --format=%cI "$rev")
cd "$work"
git add -A
GIT_AUTHOR_DATE=$date GIT_COMMITTER_DATE=$date git -c user.name="natkit release" -c user.email="natkit-release@users.noreply.github.com" \
  commit -q --allow-empty -m "natkit $tag: tofu/nat at ulm0/natkit@$rev"
git tag "$tag"
git push -q "$remote" HEAD:refs/heads/main "refs/tags/$tag"
echo "$tag: $(git rev-parse HEAD) on the mirror"
