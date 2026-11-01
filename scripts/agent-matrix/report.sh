#!/bin/sh
# Renders the matrix's results (JSON lines) as a Markdown table, then the
# detail of every check that failed or is worth reading.
set -eu
results=$1
jq -rs '
  def mark: if . == "pass" then "ok" elif . == "fail" then "FAIL" elif . == "skip" then "skip" else "info" end;
  (map(.check) | unique) as $checks
  | (["agent", "version", "support"] + $checks) as $head
  | ($head | "| " + join(" | ") + " |"),
    ($head | "|" + (map("---") | join("|")) + "|"),
    (group_by([.agent, .version, .support]) | sort_by(.[0].agent, .[0].version) | reverse | .[]
     | . as $rows
     | [$rows[0].agent, $rows[0].version, $rows[0].support]
       + ($checks | map(. as $c | ($rows | map(select(.check == $c)) | first | (.status // "-") | mark)))
     | "| " + join(" | ") + " |"),
    "",
    "Details:",
    (sort_by(.agent, .version) | .[] | "- \(.agent) \(.version) \(.check): \(.status): \(.detail)")
' "$results"
