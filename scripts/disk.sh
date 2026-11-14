#!/bin/sh
# Makes natkit's GPT disk from linuxkit's kernel+squashfs output: an ESP with systemd-boot, the kernel and a small
# initramfs, and the read-only squashfs root on partition 2 (named natkit-root). The initramfs (scripts/initramfs-init
# and a static busybox) copies that partition into memory at boot and mounts the root from there.
# Not the root itself as the initramfs, as linuxkit's raw-efi does: booting that needs about three copies of it at once
# (the firmware's, systemd-stub's and the kernel stub's), too much for a 0.5 GiB instance, and it then lives in memory
# unpacked (about 190 MB) instead of as a squashfs (about 65 MB).
# The ESP is FAT16: EDK2 rejects a FAT32 with under 65525 clusters (FatPkg/EnhancedFatDxe/Init.c) -> "No bootable
# device found".
# Runs in alpine with dosfstools, mtools, sgdisk, systemd-boot, busybox-static and cpio, with initramfs-init at
# /initramfs-init. stdin: tar of <name>-kernel, <name>-cmdline, <name>-squashfs.img; stdout: the disk.
# Usage: disk.sh <name>
set -eu
N=$1
UUID=6e61746b-6974-4000-8000-000000000002 # the root partition's GUID: the same in every image
exec 3>&1 1>&2
cd /tmp && tar -x
case $(uname -m) in
aarch64) BOOT=systemd-bootaa64.efi DST=BOOTAA64.EFI ;;
x86_64) BOOT=systemd-bootx64.efi DST=BOOTX64.EFI ;;
esac

mkdir -p ird/bin ird/dev ird/proc ird/sys
cp /bin/busybox.static ird/bin/busybox
cp /initramfs-init ird/init
chmod 0755 ird/init ird/bin/busybox
mknod -m 0600 ird/dev/console c 5 1
(cd ird && find . | sort | cpio -o -H newc -R 0:0 --quiet | gzip -9n) >initrd.img

mib() { echo $((($(stat -c %s "$1") + 1048575) / 1048576)); }
esp=$(($(mib "$N-kernel") + $(mib initrd.img) + 3)) root=$(mib "$N-squashfs.img")

mkfs.vfat -F 16 -C esp.img $((esp * 1024)) >/dev/null
mmd -i esp.img ::/EFI ::/EFI/BOOT ::/loader ::/loader/entries
echo "timeout 0" >loader.conf
printf 'title natkit\nlinux /kernel.efi\ninitrd /initrd.img\noptions %s\n' "$(cat "$N-cmdline")" >natkit.conf
mcopy -i esp.img "/usr/lib/systemd/boot/efi/$BOOT" "::/EFI/BOOT/$DST"
mcopy -i esp.img "$N-kernel" ::/kernel.efi
mcopy -i esp.img initrd.img ::/initrd.img
mcopy -i esp.img loader.conf ::/loader/
mcopy -i esp.img natkit.conf ::/loader/entries/
fsck.fat -n esp.img >/dev/null

truncate -s $((esp + root + 2))M disk.img
sgdisk --clear --new "1:2048:+${esp}M" --typecode=1:ef00 --change-name=1:'EFI System' \
	--new "2:0:+${root}M" --typecode=2:8300 --change-name=2:natkit-root --partition-guid="2:$UUID" disk.img >/dev/null
dd if=esp.img of=disk.img bs=1M seek=1 conv=notrunc status=none
dd if="$N-squashfs.img" of=disk.img bs=1M seek=$((1 + esp)) conv=notrunc status=none
cat disk.img >&3
