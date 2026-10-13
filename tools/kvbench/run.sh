#!/bin/bash
# usage: run.sh <engine> <fillmode>
E=$1
M=${2:-fill}
D=${TMPDIR:-/tmp}/kvb-$E
B=${3:-./kvb}
rm -rf $D
echo "=== $E $M"
$B -e $E -d $D -p $M
sync
echo 3 > /proc/sys/vm/drop_caches 2>/dev/null && echo "(page cache dropped)" || echo "(could not drop caches)"
$B -e $E -d $D -p open
$B -e $E -d $D -p read
echo 3 > /proc/sys/vm/drop_caches 2>/dev/null
$B -e $E -d $D -p scan
$B -e $E -d $D -p durable -n 300 -b 10
rm -rf $D
