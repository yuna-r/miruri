# ADR-0003: MPL-2.0 core, Apache-2.0 generated support code, and DCO contributions

- Status: Accepted
- Date: 2026-08-21

## Decision

- Miruri Core is licensed under Mozilla Public License 2.0 (MPL-2.0).
- Miruri-authored runtime and adapter-template files intended for embedding in generated artifacts may be licensed under Apache License 2.0 when explicitly marked as such.
- Contributions require Developer Certificate of Origin sign-off.
- Transformed source retains the source project's license.
- Generated output does not become MPL-2.0 or Apache-2.0 solely because Miruri generated it.
- Third-party components retain their own copyright notices, licenses, patent terms, trademarks and SDK conditions.

## Rationale

MPL-2.0 provides file-level copyleft for Miruri Core while allowing Miruri to participate in larger proprietary or differently licensed works. Apache-2.0 is used only for explicitly designated support code intended to be embedded in generated artifacts, reducing unnecessary licensing friction for Miruri users while retaining an explicit patent grant.

## Change warning

Relicensing after accepting external contributions can require consent from relevant copyright holders. Keep license boundaries explicit at file and directory level.
