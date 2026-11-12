#!/bin/sh
# Reformats the ESP of a linuxkit raw-efi image so EC2's UEFI can read it. linuxkit makes it FAT32 with
# 8 sectors/cluster: below ~256 MiB that is under 65525 clusters, which EDK2 rejects as corrupt
# (FatPkg/EnhancedFatDxe/Init.c) -> "No bootable device found". linuxkit sizes the partition with ~1 MiB
# free, too little for FAT32 with smaller clusters, so this makes it FAT16 (valid up to 2 GiB), same files.
# Runs in alpine with dosfstools, mtools, sfdisk. Usage: esp.sh <image>
set -eu
IMG=$1
# shellcheck disable=SC2046 # split on purpose: start, size
set -- $(sfdisk -d "$IMG" | sed -n 's/.*1 : start= *\([0-9]*\), size= *\([0-9]*\),.*/\1 \2/p')
off=$1 size=$2
dd if="$IMG" of=/tmp/esp.old bs=512 skip="$off" count="$size" status=none
mkdir /tmp/esp
mcopy -s -i /tmp/esp.old '::/*' /tmp/esp/
mkfs.vfat -F 16 -C /tmp/esp.new $((size / 2)) >/dev/null
(cd /tmp/esp && mcopy -s -i /tmp/esp.new ./* ::/)
fsck.fat -n /tmp/esp.new >/dev/null
dd if=/tmp/esp.new of="$IMG" bs=512 seek="$off" conv=notrunc status=none
