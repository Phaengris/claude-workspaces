# M0 deferred items — carried forward from the final whole-branch review

*2026-07-30. Source: SDD ledger triage, final review of feat/m0-foundation
(cf2696d..4dacf0b). Each item names the milestone whose plan should absorb it.*

## M1 (read-only commands / next config touch)
- AST-level template expansion (or position re-mapping) — the real fix for
  error positions in templated configs; current mitigation: original-bytes
  fast path for non-templated configs + "template-expanded" note otherwise.
- Expansion errors report one problem at a time; validate joins all — make
  expansion accumulate with errors.Join for consistency.
- `workspace: config: <detail>` double-prefix stderr style — polish when the
  first real commands land.
- goccy coerces scalars (`depends: 123` → "123") — revisit when depends
  drives ordering.
- `usesTemplates` treats a present-but-empty `templates:` key as templated
  (safe direction; polish only).
- Kahn cycle report includes downstream-of-cycle nodes; untested.

## M2 (first real writer / spawn sites)
- alloc.Save may error after a durable rename (dir-fsync failure) — doc note;
  Save(root, nil) marshals `null` (out of contract).
- env_allow: global + per-project merge happens at spawn-site construction —
  wire Curated's extraAllow parameter there.
- ComputeValues silently drops PerWorkspace<=0 names — unreachable via Load;
  add a direct-call guard or test when `new` starts calling it.

## M5 (doctor full / install)
- stale-allocation branch txtar coverage; os.Stat any-error currently reads
  as "dir missing"; registry-error exit path untested.

## No action (recorded decisions)
- Allowlist is exact names, not LC_*/XDG_* globs (v1 contract; user may
  overrule — one-line change). env_allow exact-name outranks prefix drop;
  PATH always sanitized even if in env_allow.
- go.mod floor: consider `go 1.26.0` instead of pinning patch release
  (final-review Minor; adjust in any go.mod touch).
