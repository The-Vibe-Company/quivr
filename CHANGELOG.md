# Changelog

## [2.0.0-alpha.1](https://github.com/The-Vibe-Company/quivr/compare/v2.0.0-alpha.0...v2.0.0-alpha.1) (2026-10-06)


### Features

* **audit:** record sensitive actions atomically ([#3756](https://github.com/The-Vibe-Company/quivr/issues/3756)) ([45d6fce](https://github.com/The-Vibe-Company/quivr/commit/45d6fce17a2d1add79c2b54f23c1f6ffcdc3b547))
* **conformance:** prove requirements with declarative cases ([#3748](https://github.com/The-Vibe-Company/quivr/issues/3748)) ([e2e6230](https://github.com/The-Vibe-Company/quivr/commit/e2e623067b823f5774a28be932e0f0f54dd0b51b))
* **demo:** count the demo's stats over every article, not the latest 300 ([#3743](https://github.com/The-Vibe-Company/quivr/issues/3743)) ([860dff8](https://github.com/The-Vibe-Company/quivr/commit/860dff85d2c5b6d7575a736dca6e8a0308a63b59))
* **load:** measure local load with free model stand-ins ([#3747](https://github.com/The-Vibe-Company/quivr/issues/3747)) ([85a8a74](https://github.com/The-Vibe-Company/quivr/commit/85a8a74c3a32463ff351365d73c96009a9e8fcf7))
* **observability:** trace requests end to end with OpenTelemetry ([#3758](https://github.com/The-Vibe-Company/quivr/issues/3758)) ([93c165e](https://github.com/The-Vibe-Company/quivr/commit/93c165e817dbba2995f084aedf495db6279fa61d))
* **ops:** add configurable structured logs and graceful drain ([#3752](https://github.com/The-Vibe-Company/quivr/issues/3752)) ([e5220d0](https://github.com/The-Vibe-Company/quivr/commit/e5220d07ed121c43f7487df4e6e8bfae46015e03))
* **plugins:** sign engine requests to plugins ([#3749](https://github.com/The-Vibe-Company/quivr/issues/3749)) ([7043fcc](https://github.com/The-Vibe-Company/quivr/commit/7043fccc931a45867c883fab497fcf02860d09d5))
* **release:** publish signed alpha images with release-please ([#3746](https://github.com/The-Vibe-Company/quivr/issues/3746)) ([c28e7b6](https://github.com/The-Vibe-Company/quivr/commit/c28e7b6a904827b526dc1f7efa2ead5e4b89f7ae))
* **tls:** secure outgoing dependency connections ([#3745](https://github.com/The-Vibe-Company/quivr/issues/3745)) ([8f2b092](https://github.com/The-Vibe-Company/quivr/commit/8f2b092508d6a995084951055a923ad19b4800cd))


### Bug Fixes

* **eval:** overlap campaign quality and isolate latency ([#3763](https://github.com/The-Vibe-Company/quivr/issues/3763)) ([f786b80](https://github.com/The-Vibe-Company/quivr/commit/f786b80b41a6f051755aa563494feb37ee4132e3))
* **eval:** pair and isolate campaign latency measurements ([#3755](https://github.com/The-Vibe-Company/quivr/issues/3755)) ([e646f0d](https://github.com/The-Vibe-Company/quivr/commit/e646f0dd7b76528291d84b82cd3f9267a0c60325))
* **monitoring:** keep alert previews within their deadline ([#3742](https://github.com/The-Vibe-Company/quivr/issues/3742)) ([4f57f71](https://github.com/The-Vibe-Company/quivr/commit/4f57f7164597e2697b0bd5b587e67eef42d0059c))
* **sdk:** send plugin replies promptly during article bursts ([#3750](https://github.com/The-Vibe-Company/quivr/issues/3750)) ([f12e22c](https://github.com/The-Vibe-Company/quivr/commit/f12e22c16cc4f9899cebb4babeed9dc229c833fa))
* **search:** reuse canonical storage connections under load ([#3753](https://github.com/The-Vibe-Company/quivr/issues/3753)) ([af22816](https://github.com/The-Vibe-Company/quivr/commit/af22816cf6cf4967114b45c8cbb3c8a35af5cceb))


### Performance Improvements

* **demo:** make the demo fast and keep it fast ([#3744](https://github.com/The-Vibe-Company/quivr/issues/3744)) ([6787871](https://github.com/The-Vibe-Company/quivr/commit/6787871e1feb22880003b2ea8f8390a17c38d9ac))
* **demo:** open an article, filter and switch tabs within 200 ms ([#3754](https://github.com/The-Vibe-Company/quivr/issues/3754)) ([b15342b](https://github.com/The-Vibe-Company/quivr/commit/b15342bf4b798b1b3b146de5f2510967633272ed))
* **ingestion:** reduce burst indexing and alert latency ([#3764](https://github.com/The-Vibe-Company/quivr/issues/3764)) ([eb76cca](https://github.com/The-Vibe-Company/quivr/commit/eb76cca3b82237703aff00470f1d541da2647962))

## Changelog
