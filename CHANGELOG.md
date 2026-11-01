# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

- Initial independent library: Flyway-compatible history table and checksums,
  versioned and repeatable SQL migrations, registered Go migrations,
  placeholders, callbacks, validate/baseline/repair/info/clean, MySQL,
  PostgreSQL, ClickHouse and TDengine stores, and the `gmigrate` CLI.
- Default/ordered schema support, dry-run, Undo (`U` files and Go `Down`) and cherry-pick.
- Testcontainers integration suite for MySQL, PostgreSQL, ClickHouse and TDengine.
