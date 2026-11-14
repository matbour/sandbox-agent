#!/bin/sh
# Boot the agent rootfs in Cloud Hypervisor, snapshot the running guest, restore it in a new
# process and time each step (docs/microvm.md, Option A2). The guest prints a tick every second,
# so the serial log shows it running on after the restore.
#
#   cloud-hypervisor/snapshot-check.sh [copy|copyonwrite|ondemand]
#
# Needs the CI kernel and the rootfs in build/<arch>/ (firecracker/lib.sh, scripts/build-rootfs.sh)
# and the kvm group. ondemand uses userfaultfd, which needs vm.unprivileged_userfaultfd=1 or
# CAP_SYS_PTRACE. Run directories go to build/cloud-hypervisor/; the API sockets are relative
# because Unix socket paths are limited to 108 bytes.
set -u

mode=${1:-copyonwrite}
root=$(cd "$(dirname "$0")/.." && pwd)
img=$root/build/$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
dir=$root/build/cloud-hypervisor/snapshot-check

ms() { echo $(( ($(date +%s%N) - $1) / 1000000 )); }
stop() { for p in $(pgrep -f "^cloud-hypervisor --api-socket path=$1"); do kill "$p"; done; sleep 0.5; }

rm -rf "$dir"
mkdir -p "$dir/snap"
cd "$dir" || exit 1

t=$(date +%s%N)
cloud-hypervisor --api-socket path=api.sock --kernel "$img/vmlinux" \
	--cmdline 'console=ttyS0 reboot=k panic=1 ro quiet root=/dev/vda init=/bin/sh -- -c "echo guest-ready; while true; do date +tick-%s; sleep 1; done"' \
	--disk path="$img/rootfs.ext4",readonly=on,image_type=raw --cpus boot=2 --memory size=1024M \
	--serial file=serial.log --console off >vmm.log 2>&1 &
for _ in $(seq 2000); do grep -q guest-ready serial.log 2>/dev/null && break; sleep 0.002; done
echo "cold boot to the guest's first line: $(ms "$t") ms"
sleep 1.5

t=$(date +%s%N)
ch-remote --api-socket api.sock pause
ch-remote --api-socket api.sock snapshot "file://$dir/snap"
echo "pause and snapshot: $(ms "$t") ms"
stop api.sock

t=$(date +%s%N)
cloud-hypervisor --api-socket path=api2.sock \
	--restore source_url="file://$dir/snap",memory_restore_mode="$mode" >vmm-restore.log 2>&1 &
for _ in $(seq 2000); do ch-remote --api-socket api2.sock info >/dev/null 2>&1 && break; sleep 0.002; done
echo "restore ($mode) until the API answers: $(ms "$t") ms"

t=$(date +%s%N)
ch-remote --api-socket api2.sock resume
echo "resume: $(ms "$t") ms"

sleep 2.5
echo "serial after the restore:"
tail -3 serial.log
stop api2.sock
