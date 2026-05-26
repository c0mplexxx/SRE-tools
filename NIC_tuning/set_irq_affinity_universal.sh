#!/bin/bash
#
# Universal IRQ affinity script for Intel (ixgbe/ice/i40e) and Mellanox (mlx5/mlx4)
#
# Modes:
#   Manual  — explicit CPU list
#   --numa  — auto: pin to NUMA node where the NIC lives, set queue count
#             to min(NUMA CPU count, NIC max queues)
#
# Usage:
#   set_irq_affinity_universal.sh <interface> <cpu_list>
#   set_irq_affinity_universal.sh --numa <interface>
#
# cpu_list formats:
#   0,1,2,3   — comma-separated
#   0-7       — range
#   0,2-4,8   — mixed
#
# Examples:
#   ./set_irq_affinity_universal.sh enp129s0f0np0 0-7
#   ./set_irq_affinity_universal.sh enp59s0f0 0,2-4,8
#   ./set_irq_affinity_universal.sh --numa enp129s0f0np0
#
# Fixes vs original Mellanox set_irq_affinity_cpulist.sh:
#   1. mlx5 IRQs found via PCI address (mlx5_comp* only), not interface name
#      — original regex never matched mlx5 interrupt names
#   2. Mixed cpu lists like "0,2-4" no longer silently drop plain numbers
#   3. smp_affinity_list used directly — avoids bash int overflow for cores >= 63

set -uo pipefail

# ─── usage ───────────────────────────────────────────────────────────────────

usage() {
    echo "Usage:"
    echo "  $0 <interface> <cpu_list>     manual mode"
    echo "  $0 --numa <interface>         auto NUMA mode"
    echo "  $0 --show <interface>         show current IRQ affinity (read-only)"
    echo ""
    echo "  cpu_list: comma-separated CPUs or ranges, e.g.: 0,1,2  or  0-7  or  0,2-4"
    echo ""
    echo "  --numa  Detects the NUMA node of the NIC, reads its CPU list,"
    echo "          sets queue count to min(NUMA CPUs, NIC max queues),"
    echo "          then pins each queue IRQ to a NUMA CPU."
    echo ""
    echo "  --show  Read-only. Shows per-queue IRQ, current CPU affinity,"
    echo "          and interrupt counters. No changes are made."
    echo ""
    echo "Examples:"
    echo "  $0 enp129s0f0np0 0-7"
    echo "  $0 enp59s0f0     0,2-4,8"
    echo "  $0 --numa        enp129s0f0np0"
    echo "  $0 --show        enp129s0f0np0"
    exit 1
}

# ─── argument parsing ────────────────────────────────────────────────────────

NUMA_MODE=0
SHOW_MODE=0
IFACE=""
CPULIST_RAW=""

if [ $# -lt 2 ]; then
    usage
fi

if [ "$1" == "--numa" ]; then
    NUMA_MODE=1
    IFACE="$2"
elif [ "$1" == "--show" ]; then
    SHOW_MODE=1
    IFACE="$2"
else
    IFACE="$1"
    CPULIST_RAW="$2"
fi

# ─── helpers ─────────────────────────────────────────────────────────────────

# Expand "0,2-4,7" → space-separated: "0 2 3 4 7"
# Fix for Bug 2: original mlnx script silently dropped plain numbers when mixed with ranges
expand_cpulist() {
    local input="$1"
    local result=()
    IFS=',' read -ra parts <<< "$input"
    for part in "${parts[@]}"; do
        if [[ "$part" == *-* ]]; then
            local start="${part%-*}"
            local end="${part#*-}"
            for (( c=start; c<=end; c++ )); do
                result+=( "$c" )
            done
        else
            result+=( "$part" )
        fi
    done
    echo "${result[@]}"
}

# Get PCI address for a network interface (e.g. "0000:81:00.0")
get_pci_addr() {
    basename "$(readlink /sys/class/net/$1/device 2>/dev/null)" 2>/dev/null || echo ""
}

# Get driver name via ethtool
get_driver() {
    ethtool -i "$1" 2>/dev/null | awk '/^driver:/{print $2}'
}

# Get NUMA node index for a NIC (returns 0 if unknown or non-NUMA)
get_numa_node() {
    local iface="$1"
    local numa_path="/sys/class/net/${iface}/device/numa_node"
    local node
    node=$(cat "$numa_path" 2>/dev/null || echo "-1")
    # -1 means non-NUMA or unknown — treat as node 0
    [ "$node" -lt 0 ] && node=0
    echo "$node"
}

# Get CPU list for a NUMA node as expanded array
get_numa_cpus() {
    local node="$1"
    local cpulist_path="/sys/devices/system/node/node${node}/cpulist"
    if [ ! -f "$cpulist_path" ]; then
        echo "Error: NUMA node $node not found at $cpulist_path" >&2
        exit 1
    fi
    expand_cpulist "$(cat "$cpulist_path")"
}

# Get max supported combined queues from ethtool -l
get_max_queues() {
    local iface="$1"
    local max
    max=$(ethtool -l "$iface" 2>/dev/null \
          | awk '/Pre-set maximums/,/Current hardware/' \
          | awk '/Combined:/{print $2; exit}')
    # Fallback: if no combined, try RX
    [ -z "$max" ] && max=$(ethtool -l "$iface" 2>/dev/null \
          | awk '/Pre-set maximums/,/Current hardware/' \
          | awk '/RX:/{print $2; exit}')
    echo "${max:-1}"
}

# ─── IRQ discovery ───────────────────────────────────────────────────────────

# mlx5: match only mlx5_comp* by PCI address — skip mlx5_async (management IRQ)
# Fix for Bug 1: original regex "$interface[^0-9,a-z,A-Z]" never matched mlx5 names
# which look like "mlx5_comp0@pci:0000:81:00.0" — not the interface name
get_irqs_mlx5() {
    local iface="$1"
    local pci_addr
    pci_addr=$(get_pci_addr "$iface")
    if [ -z "$pci_addr" ]; then
        echo "Error: cannot resolve PCI address for $iface" >&2
        exit 1
    fi
    echo "PCI address : $pci_addr" >&2
    grep -P "mlx5_comp\d+@pci:${pci_addr}\b" /proc/interrupts \
        | awk -F: '{gsub(/[[:space:]]/, "", $1); print $1}'
}

# mlx4: "mlx4-N@pci_addr"
get_irqs_mlx4() {
    local iface="$1"
    local pci_addr
    pci_addr=$(get_pci_addr "$iface")
    grep -P "mlx4-\d+@${pci_addr}\b" /proc/interrupts \
        | awk -F: '{gsub(/[[:space:]]/, "", $1); print $1}'
}

# Intel ixgbe/ice/i40e: IRQ names embed interface name + TxRx
# e.g. "ixgbe-enp59s0f0-TxRx-0" or "ice-enp59s0f0-TxRx-0"
get_irqs_intel() {
    local iface="$1"
    local irqs
    irqs=$(grep -E "${iface}[^a-zA-Z0-9].*TxRx" /proc/interrupts \
           | awk -F: '{gsub(/[[:space:]]/, "", $1); print $1}')
    # Fallback: any line containing the interface name
    [ -z "$irqs" ] && irqs=$(grep -w "$iface" /proc/interrupts \
           | awk -F: '{gsub(/[[:space:]]/, "", $1); print $1}')
    echo "$irqs"
}

get_irqs_for_driver() {
    local iface="$1"
    local driver="$2"
    case "$driver" in
        mlx5_core)              get_irqs_mlx5 "$iface" ;;
        mlx4_en|mlx4_core)     get_irqs_mlx4 "$iface" ;;
        ixgbe|ice|i40e|igb|igc|e1000e) get_irqs_intel "$iface" ;;
        *)
            echo "Warning: unknown driver '$driver', falling back to interface name match" >&2
            grep "$iface" /proc/interrupts \
                | awk -F: '{gsub(/[[:space:]]/, "", $1); print $1}'
            ;;
    esac
}

# ─── show ────────────────────────────────────────────────────────────────────

show_irq_affinity() {
    local iface="$1"
    local driver="$2"
    local irqs
    irqs=$(get_irqs_for_driver "$iface" "$driver")

    if [ -z "$irqs" ]; then
        echo "No IRQs found for $iface (driver: $driver)"
        exit 1
    fi

    readarray -t irqs_arr <<< "$irqs"
    local total_cpus
    total_cpus=$(nproc)

    echo ""
    printf "%-8s %-6s %-55s %s\n" "IRQ" "CPU" "Queue" "Interrupts (per-CPU, non-zero)"
    echo "────────────────────────────────────────────────────────────────────────────────────────"

    # Track per-CPU IRQ counts for summary
    declare -A cpu_irq_count

    for irq in "${irqs_arr[@]}"; do
        local cpu aff_list queue_name irq_counts cpu_counts

        aff_list="/proc/irq/${irq}/smp_affinity_list"
        cpu=$(cat "$aff_list" 2>/dev/null || echo "?")

        queue_name=$(grep -P "^\s*${irq}:" /proc/interrupts | awk '{print $NF}')

        # Extract non-zero per-CPU counters with their CPU index
        irq_counts=$(awk -v irq="$irq" -F: '
            $1 ~ "^[[:space:]]*"irq"$" {
                n = split($2, fields, /[[:space:]]+/)
                out = ""
                cpu = 0
                for (i = 1; i <= n; i++) {
                    if (fields[i] ~ /^[0-9]+$/) {
                        if (fields[i]+0 > 0) out = out "cpu" cpu "=" fields[i] " "
                        cpu++
                    }
                }
                print out
            }' /proc/interrupts)

        printf "%-8s %-6s %-55s %s\n" "$irq" "$cpu" "$queue_name" "$irq_counts"

        # Accumulate for summary
        cpu_irq_count[$cpu]=$(( ${cpu_irq_count[$cpu]:-0} + 1 ))
    done

    echo "────────────────────────────────────────────────────────────────────────────────────────"
    echo "Total IRQs: ${#irqs_arr[@]}"
    echo ""
    echo "IRQ distribution per CPU:"
    for cpu in $(echo "${!cpu_irq_count[@]}" | tr ' ' '\n' | sort -n); do
        printf "  CPU %-4s : %d IRQ(s)\n" "$cpu" "${cpu_irq_count[$cpu]}"
    done
}

# ─── pinning ─────────────────────────────────────────────────────────────────

pin_irqs() {
    local -n _irqs=$1   # nameref to array
    local -n _cpus=$2   # nameref to array

    local ncpus=${#_cpus[@]}
    local idx=0

    printf "%-8s %-6s %-6s %s\n" "IRQ" "CPU" "Status" "Queue name"
    echo "────────────────────────────────────────────────────────"

    for irq in "${_irqs[@]}"; do
        local cpu="${_cpus[$((idx % ncpus))]}"
        local aff_list="/proc/irq/${irq}/smp_affinity_list"
        local queue_name
        queue_name=$(grep -P "^\s*${irq}:" /proc/interrupts | awk '{print $NF}')

        if [ ! -f "$aff_list" ]; then
            printf "%-8s %-6s %-6s %s\n" "$irq" "$cpu" "SKIP" "smp_affinity_list not found"
            (( idx++ )) || true
            continue
        fi

        local status
        if echo "$cpu" > "$aff_list" 2>/dev/null; then
            status="OK"
        else
            status="FAIL"
        fi

        printf "%-8s %-6s %-6s %s\n" "$irq" "$cpu" "$status" "$queue_name"
        (( idx++ )) || true
    done

    echo "────────────────────────────────────────────────────────"
}

# ─── main ────────────────────────────────────────────────────────────────────

# Validate interface
if [ ! -d "/sys/class/net/$IFACE" ]; then
    echo "Error: interface '$IFACE' not found"
    exit 1
fi

DRIVER=$(get_driver "$IFACE")
if [ -z "$DRIVER" ]; then
    echo "Error: could not determine driver for $IFACE (is ethtool installed?)"
    exit 1
fi

echo "Interface : $IFACE"
echo "Driver    : $DRIVER"

# ── Show mode ────────────────────────────────────────────────────────────────
if [ "$SHOW_MODE" -eq 1 ]; then
    show_irq_affinity "$IFACE" "$DRIVER"
    exit 0
fi

# ── NUMA mode ────────────────────────────────────────────────────────────────
if [ "$NUMA_MODE" -eq 1 ]; then
    NUMA_NODE=$(get_numa_node "$IFACE")
    read -ra NUMA_CPUS <<< "$(get_numa_cpus "$NUMA_NODE")"
    NUMA_CPU_COUNT=${#NUMA_CPUS[@]}

    MAX_QUEUES=$(get_max_queues "$IFACE")

    # Active queues = min(NUMA CPUs, max NIC queues)
    if [ "$NUMA_CPU_COUNT" -le "$MAX_QUEUES" ]; then
        ACTIVE_QUEUES=$NUMA_CPU_COUNT
    else
        ACTIVE_QUEUES=$MAX_QUEUES
    fi

    echo "NUMA node : $NUMA_NODE"
    echo "NUMA CPUs : ${NUMA_CPUS[*]} (${NUMA_CPU_COUNT} total)"
    echo "Max queues: $MAX_QUEUES"
    echo "Setting   : $ACTIVE_QUEUES queues (min of the two)"
    echo ""

    # Set queue count on the NIC
    if ethtool -L "$IFACE" combined "$ACTIVE_QUEUES" 2>/dev/null; then
        echo "ethtool -L $IFACE combined $ACTIVE_QUEUES — OK"
    else
        # Some NICs don't support combined, try rx/tx separately
        ethtool -L "$IFACE" rx "$ACTIVE_QUEUES" tx "$ACTIVE_QUEUES" 2>/dev/null \
            && echo "ethtool -L $IFACE rx/tx $ACTIVE_QUEUES — OK" \
            || echo "Warning: could not set queue count (driver may not support it)"
    fi
    echo ""

    # Slice NUMA CPU list to ACTIVE_QUEUES length for 1:1 pinning
    CPUS=( "${NUMA_CPUS[@]:0:$ACTIVE_QUEUES}" )

# ── Manual mode ──────────────────────────────────────────────────────────────
else
    read -ra CPUS <<< "$(expand_cpulist "$CPULIST_RAW")"
    if [ ${#CPUS[@]} -eq 0 ]; then
        echo "Error: empty CPU list after parsing '$CPULIST_RAW'"
        exit 1
    fi
fi

echo "CPU list  : ${CPUS[*]}"
echo ""

# Warn if irqbalance is active
if systemctl is-active --quiet irqbalance 2>/dev/null; then
    echo "WARNING: irqbalance is running and will override these settings."
    echo "         Stop it: systemctl stop irqbalance && systemctl disable irqbalance"
    echo ""
fi

# Discover IRQs (after queue count change in NUMA mode)
IRQS=$(get_irqs_for_driver "$IFACE" "$DRIVER")

if [ -z "$IRQS" ]; then
    echo "Error: no IRQs found for $IFACE (driver: $DRIVER)"
    echo "       Check: grep '$IFACE' /proc/interrupts"
    exit 1
fi

readarray -t IRQS_ARR <<< "$IRQS"
echo "IRQs found: ${#IRQS_ARR[@]} → ${IRQS_ARR[*]}"
echo ""
echo "Pinning:"

pin_irqs IRQS_ARR CPUS

echo "Done."
echo ""
echo "Verify:"
echo "  for irq in ${IRQS_ARR[*]}; do"
echo "    printf 'IRQ %s -> CPU %s\n' \$irq \$(cat /proc/irq/\$irq/smp_affinity_list)"
echo "  done"
