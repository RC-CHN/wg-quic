package platform

import (
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
)

// freeBSDRouteOperations is kept platform-neutral so its route semantics can
// be unit-tested on Linux without executing any FreeBSD command.
func freeBSDRouteOperations(name string, cfg *config.Config) []hostOperation {
	if strings.EqualFold(cfg.Interface.Table, "off") {
		return nil
	}
	table := cfg.Interface.Table
	auto := table == "" || strings.EqualFold(table, "auto")
	var operations []hostOperation
	add := func(family, prefix string, extra ...string) {
		apply := []string{"-q", "-n", "add", family}
		apply = append(apply, extra...)
		apply = append(apply, prefix, "-interface", name)
		undo := []string{"-q", "-n", "delete", family}
		undo = append(undo, extra...)
		undo = append(undo, prefix)
		operations = append(operations, hostOperation{
			apply: hostCommand{name: "route", args: apply},
			undo:  hostCommand{name: "route", args: undo},
		})
	}
	for _, prefix := range uniqueAllowedPrefixes(cfg) {
		family := "-inet"
		if prefix.Addr().Is6() {
			family = "-inet6"
		}
		if auto && prefix.Bits() == 0 {
			if prefix.Addr().Is4() {
				add(family, "0.0.0.0/1")
				add(family, "128.0.0.0/1")
			} else {
				add(family, "::/1")
				add(family, "8000::/1")
			}
			continue
		}
		if auto {
			add(family, prefix.String())
		} else {
			add(family, prefix.String(), "-fib", table)
		}
	}
	return operations
}
