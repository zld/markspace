package model

import "sort"

func stableSort(errs []*Error, less func(a, b *Error) bool) {
	sort.SliceStable(errs, func(i, j int) bool { return less(errs[i], errs[j]) })
}
