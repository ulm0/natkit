#!/bin/sh
# Makes natkit's GPT disk from linuxkit's kernel+squashfs output: an ESP with systemd-boot and the kernel, and the
# read-only squashfs root on partition 2, which the kernel mounts by PARTUUID (no initramfs: an initramfs root needs
# about three times its size while booting and then lives in RAM, too much for a 0.5 GiB instance).
# The ESP is FAT16: EDK2 rejects a FAT32 with under 65525 clusters (FatPkg/EnhancedFatDxe/Init.c) -> "No bootable
# device found".
# Runs in alpine with dosfstools, mtools, sgdisk, systemd-boot. stdin: tar of <name>-kernel, <name>-cmdline,
# <name>-squashfs.img; stdout: the disk. Usage: disk.sh <name>
set -eu
N=$1
UUID=6e61746b-6974-4000-8000-000000000002 # the root partition's PARTUUID: the same in every image
exec 3>&1 1>&2
cd /tmp && tar -x
case $(uname -m) in
aarch64) BOOT=systemd-bootaa64.efi DST=BOOTAA64.EFI ;;
x86_64) BOOT=systemd-bootx64.efi DST=BOOTX64.EFI ;;
esac
mib() { echo $((($(stat -c %s "$1") + 1048575) / 1048576)); }
esp=$(($(mib "$N-kernel") + 3)) root=$(mib "$N-squashfs.img")

mkfs.vfat -F 16 -C esp.img $((esp * 1024)) >/dev/null
mmd -i esp.img ::/EFI ::/EFI/BOOT ::/loader ::/loader/entries
echo "timeout 0" >loader.conf
printf 'title natkit\nlinux /kernel.efi\noptions %s root=PARTUUID=%s ro rootwait\n' "$(cat "$N-cmdline")" "$UUID" >natkit.conf
mcopy -i esp.img "/usr/lib/systemd/boot/efi/$BOOT" "::/EFI/BOOT/$DST"
mcopy -i esp.img "$N-kernel" ::/kernel.efi
mcopy -i esp.img loader.conf ::/loader/
mcopy -i esp.img natkit.conf ::/loader/entries/
fsck.fat -n esp.img >/dev/null

truncate -s $((esp + root + 2))M disk.img
sgdisk --clear --new "1:2048:+${esp}M" --typecode=1:ef00 --change-name=1:'EFI System' \
	--new "2:0:+${root}M" --typecode=2:8300 --change-name=2:root --partition-guid="2:$UUID" disk.img >/dev/null
dd if=esp.img of=disk.img bs=1M seek=1 conv=notrunc status=none
dd if="$N-squashfs.img" of=disk.img bs=1M seek=$((1 + esp)) conv=notrunc status=none
cat disk.img >&3
