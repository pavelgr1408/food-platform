#!/bin/bash
# Создаёт топики из topics.txt (идемпотентно). Файл генерируется из contracts/asyncapi: make topics.
set -euo pipefail
BIN=/opt/kafka/bin
while IFS='|' read -r name partitions config; do
  [[ -z "$name" || "$name" == \#* ]] && continue
  args=(--bootstrap-server "$BOOTSTRAP" --create --if-not-exists --topic "$name" --partitions "$partitions" --replication-factor 1)
  IFS=',' read -ra cfgs <<< "${config:-}"
  for c in "${cfgs[@]}"; do [[ -n "$c" ]] && args+=(--config "$c"); done
  $BIN/kafka-topics.sh "${args[@]}" </dev/null
done < /init/topics.txt
echo "topics ready:"; $BIN/kafka-topics.sh --bootstrap-server "$BOOTSTRAP" --list
