#!/usr/bin/env python3

import argparse
import csv
import statistics
import sys
from collections import defaultdict


DEFAULT_GROUPS = (
    "mode",
    "link_profile",
    "link_schedule",
    "workload",
    "fwd_rate_mbit",
    "rev_rate_mbit",
    "fwd_delay_ms",
    "rev_delay_ms",
    "fwd_loss_pct",
    "rev_loss_pct",
    "parallel",
    "offered_mbit",
)


def number(row, field):
    try:
        return float(row.get(field, 0) or 0)
    except ValueError:
        return 0.0


def percentile(values, quantile):
    ordered = sorted(values)
    if not ordered:
        return 0.0
    if len(ordered) == 1:
        return ordered[0]
    position = (len(ordered) - 1) * quantile
    lower = int(position)
    upper = min(lower + 1, len(ordered) - 1)
    fraction = position - lower
    return ordered[lower] * (1 - fraction) + ordered[upper] * fraction


def main():
    parser = argparse.ArgumentParser(
        description="Aggregate repeated wg-quic benchmark trials."
    )
    parser.add_argument("summary", help="path to benchmark summary.csv")
    parser.add_argument(
        "--group-by",
        default=",".join(DEFAULT_GROUPS),
        help="comma-separated grouping columns",
    )
    args = parser.parse_args()
    groups = tuple(field for field in args.group_by.split(",") if field)

    with open(args.summary, newline="", encoding="utf-8") as source:
        rows = list(csv.DictReader(source))
    if not rows:
        return
    missing = [field for field in groups if field not in rows[0]]
    if missing:
        parser.error("unknown grouping columns: " + ", ".join(missing))

    grouped = defaultdict(list)
    for row in rows:
        grouped[tuple(row.get(field, "") for field in groups)].append(row)

    fields = list(groups) + [
        "samples",
        "ok",
        "failed",
        "outer_median_mbit",
        "goodput_median_mbit",
        "goodput_p10_mbit",
        "goodput_p90_mbit",
        "udp_loss_median_pct",
        "queue_drops_median",
        "cpu_median_s",
        "goodput_to_wire_median",
    ]
    writer = csv.DictWriter(sys.stdout, fieldnames=fields)
    writer.writeheader()
    for key in sorted(grouped):
        samples = grouped[key]
        good = [row for row in samples if row.get("status", "ok") == "ok"]
        measured = good or samples
        goodputs = [number(row, "goodput_bps") / 1_000_000 for row in measured]
        result = dict(zip(groups, key))
        result.update(
            {
                "samples": len(samples),
                "ok": len(good),
                "failed": len(samples) - len(good),
                "outer_median_mbit": statistics.median(
                    number(row, "outer_baseline_bps") / 1_000_000
                    for row in measured
                ),
                "goodput_median_mbit": statistics.median(goodputs),
                "goodput_p10_mbit": percentile(goodputs, 0.10),
                "goodput_p90_mbit": percentile(goodputs, 0.90),
                "udp_loss_median_pct": statistics.median(
                    number(row, "udp_lost_pct") for row in measured
                ),
                "queue_drops_median": statistics.median(
                    number(row, "queue_drops_a") for row in measured
                ),
                "cpu_median_s": statistics.median(
                    number(row, "core_cpu_a_s") + number(row, "core_cpu_b_s")
                    for row in measured
                ),
                "goodput_to_wire_median": statistics.median(
                    number(row, "goodput_to_wire_ratio") for row in measured
                ),
            }
        )
        writer.writerow(result)


if __name__ == "__main__":
    main()
