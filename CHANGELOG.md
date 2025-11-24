# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Attempt to ensure credentials secret isn't deleted until the cleaner has finished consuming it.

## [0.3.1] - 2024-08-01

### Changed

- Add more logs to improve debugging.
- Set `EnableKeepAlive=false` to avoid deadlock.

## [0.3.0] - 2024-07-24

### Changed

- Update renovate to json5 config.
- Upgrade `k8s.io/api`, `k8s.io/client-go` and `k8s.io/apimachinery` from `0.25.0` to `0.29.3`
- Upgrade `sigs.k8s.io/cluster-api` from `1.3.3` to `1.6.5`
- Upgrade `sigs.k8s.io/cluster-api-provider-vsphere` from `1.6.0` to `1.9.3`
- Upgrade `sigs.k8s.io/controller-runtime` from `0.13.1` to `0.17.3`
- Upgrade `github.com/vmware/govmomi` from `0.34.2` to `0.36.1`

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

[Unreleased]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/giantswarm/cluster-api-cleaner-vsphere/releases/tag/v0.1.0
