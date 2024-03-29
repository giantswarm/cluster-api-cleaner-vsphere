# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2024-03-04

### Added

- Add `global.podSecurityStandards.enforced` flag to disable PSPs by default.

## [0.1.2] - 2024-01-04

### Changed

- Configure `gsoci.azurecr.io` as the default container image registry.
- Fix volume clean-up issue because of attached VMs.

## [0.1.1] - 2023-08-24

### Changed

- Ignore CVE-2023-3978 & CVE-2023-29401.
- Fix security issues reported by kyverno policies.

## [0.1.0] - 2023-05-09

### Added

- Init repository by mimicking cluster-api-cleaner-cloud-director.

[Unreleased]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/releases/tag/v0.1.0
