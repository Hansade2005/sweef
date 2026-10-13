#!/bin/bash
# usage: crash.sh <engine> <binary> <rounds>
E=$1
B=$2
R=${3:-20}
D=${TMPDIR:-/tmp}/kvb-crash-$E
rm -rf $D
torn_total=0
START=0
lost_total=0
for i in $(seq 1 $R); do
  $B -e $E -d $D -p crashw -b 200 -v 2000 -n $START > ${TMPDIR:-/tmp}/kvb-crash-out.$E 2>/dev/null &
  PID=$!
  sleep 0.$((RANDOM % 9 + 2))
  kill -9 $PID 2>/dev/null
  wait $PID 2>/dev/null
  ACK=$(tail -n 2 ${TMPDIR:-/tmp}/kvb-crash-out.$E | head -n 1)
  [ -z "$ACK" ] && ACK=-1
  V=$($B -e $E -d $D -p crashv -b 200 2>&1 | grep verify)
  MAX=$(echo "$V" | sed 's/.*max=\([0-9]*\).*/\1/')
  TORN=$(echo "$V" | sed 's/.*torn=\([0-9]*\).*/\1/')
  [ "$TORN" != "0" ] && torn_total=$((torn_total+1))
  if [ "$MAX" -lt "$ACK" ]; then lost_total=$((lost_total+1)); fi
  START=$((MAX+1))
  echo "round $i: acked(last complete line)=$ACK  $V"
done
echo "SUMMARY $E: rounds=$R rounds_with_torn_batches=$torn_total rounds_with_acked_loss=$lost_total"
rm -rf $D ${TMPDIR:-/tmp}/kvb-crash-out.$E
