#!/usr/bin/env python3

import argparse
import csv
import statistics
import sys
from collections import defaultdict


DEFAULT_GROUPS = (
    "mode",
    "congestion",
    "protocol_policy",
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
    if "congestion" not in rows[0]:
        for row in rows:
            row["congestion"] = "legacy"
    if "protocol_policy" not in rows[0]:
        for row in rows:
            row["protocol_policy"] = "none"
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
        "sender_outer_median_mbit",
        "udp_loss_median_pct",
        "queue_drops_median",
        "fec_current_parity_median",
        "fec_loss_estimate_median_pct",
        "quic_acked_median_mbit",
        "quic_loss_median_pct",
        "quic_path_rtt_median_ms",
        "quic_smoothed_rtt_median_ms",
        "quic_cwnd_median_bytes",
        "quic_bandwidth_median_mbit",
        "quic_pacing_median_mbit",
        "quic_queue_delay_median_ms",
        "quic_fec_recoverable_loss_median_pct",
        "quic_fec_residual_loss_median_pct",
        "cpu_median_s",
        "goodput_to_wire_median",
        "goodput_to_outer_median",
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
                "sender_outer_median_mbit": statistics.median(
                    number(row, "outer_tx_bps_a") / 1_000_000
                    for row in measured
                ),
                "udp_loss_median_pct": statistics.median(
                    number(row, "udp_lost_pct") for row in measured
                ),
                "queue_drops_median": statistics.median(
                    number(row, "queue_drops_a") for row in measured
                ),
                "fec_current_parity_median": statistics.median(
                    number(row, "fec_current_parity_a") for row in measured
                ),
                "fec_loss_estimate_median_pct": statistics.median(
                    number(row, "fec_loss_estimate_ppm_a") / 10_000
                    for row in measured
                ),
                "quic_acked_median_mbit": statistics.median(
                    number(row, "quic_acked_bps_a") / 1_000_000
                    for row in measured
                ),
                "quic_loss_median_pct": statistics.median(
                    (
                        100
                        * number(row, "quic_bytes_lost_a")
                        / (
                            number(row, "quic_bytes_acked_a")
                            + number(row, "quic_bytes_lost_a")
                        )
                    )
                    if (
                        number(row, "quic_bytes_acked_a")
                        + number(row, "quic_bytes_lost_a")
                    )
                    > 0
                    else 0
                    for row in measured
                ),
                "quic_smoothed_rtt_median_ms": statistics.median(
                    number(row, "quic_smoothed_rtt_us_a") / 1000
                    for row in measured
                ),
                "quic_path_rtt_median_ms": statistics.median(
                    number(row, "quic_path_rtt_us_a") / 1000
                    for row in measured
                ),
                "quic_cwnd_median_bytes": statistics.median(
                    number(row, "quic_cwnd_bytes_a") for row in measured
                ),
                "quic_bandwidth_median_mbit": statistics.median(
                    number(row, "quic_bandwidth_estimate_bps_a") / 1_000_000
                    for row in measured
                ),
                "quic_pacing_median_mbit": statistics.median(
                    number(row, "quic_pacing_rate_bps_a") / 1_000_000
                    for row in measured
                ),
                "quic_queue_delay_median_ms": statistics.median(
                    number(row, "quic_queue_delay_us_a") / 1000
                    for row in measured
                ),
                "quic_fec_recoverable_loss_median_pct": statistics.median(
                    number(row, "quic_fec_recoverable_loss_ppm_a") / 10_000
                    for row in measured
                ),
                "quic_fec_residual_loss_median_pct": statistics.median(
                    number(row, "quic_fec_residual_loss_ppm_a") / 10_000
                    for row in measured
                ),
                "cpu_median_s": statistics.median(
                    number(row, "core_cpu_a_s") + number(row, "core_cpu_b_s")
                    for row in measured
                ),
                "goodput_to_wire_median": statistics.median(
                    number(row, "goodput_to_wire_ratio") for row in measured
                ),
                "goodput_to_outer_median": statistics.median(
                    number(row, "goodput_to_outer_ratio") for row in measured
                ),
            }
        )
        writer.writerow(result)


if __name__ == "__main__":
    main()
