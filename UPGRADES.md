# Upgrade Backlog

Deferred improvements that are worth doing but require more invasive changes or external preconditions.

---

## Code Quality / Modernization

### `strings.SplitSeq` / `strings.CutPrefix` in `diffutil`
`diffutil.SplitSections` and `PathFromSection` use `strings.Split` + index loops. Go 1.24 added `strings.SplitSeq` (iterator-based, avoids allocating the full slice) and `strings.CutPrefix` (cleaner than `TrimPrefix` + check). Worth adopting once the minimum Go version is confirmed to be ≥ 1.24.
