# Changelog

## [2.0.0-alpha.5](https://github.com/The-Vibe-Company/quivr/compare/v2.0.0-alpha.4...v2.0.0-alpha.5) (2026-10-06)


### Features

* **connectors:** import object-storage archives resumably ([#3782](https://github.com/The-Vibe-Company/quivr/issues/3782)) ([a77071a](https://github.com/The-Vibe-Company/quivr/commit/a77071a4254008b46af460d3a11b53177cd12188))
* **demo:** explore each corpus with facets and span corpora in the feed ([#3787](https://github.com/The-Vibe-Company/quivr/issues/3787)) ([86216ab](https://github.com/The-Vibe-Company/quivr/commit/86216ab920e50251880017fa3f334e8b188db8cf))
* **quivr-search:** move page actions into the top bar and smooth page switches ([#3751](https://github.com/The-Vibe-Company/quivr/issues/3751)) ([d176389](https://github.com/The-Vibe-Company/quivr/commit/d176389ea84048106d8eb9b5f0094fc976e18602))
* **retrieval:** filter search and catalogs by document metadata ([#3780](https://github.com/The-Vibe-Company/quivr/issues/3780)) ([719f612](https://github.com/The-Vibe-Company/quivr/commit/719f612694b8e707460cb7f158059d92f8a7c8ff))


### Bug Fixes

* **newsml-g2:** emit filterable common metadata ([#3786](https://github.com/The-Vibe-Company/quivr/issues/3786)) ([c884053](https://github.com/The-Vibe-Company/quivr/commit/c8840530e962ea2161d6f5d020073b19d8324188))
* **railway:** run the NewsML-G2 normalizer in the demo ([#3788](https://github.com/The-Vibe-Company/quivr/issues/3788)) ([1c4af0f](https://github.com/The-Vibe-Company/quivr/commit/1c4af0fa77a42150d1fe515ee539674628441a1c))

## [2.0.0-alpha.4](https://github.com/The-Vibe-Company/quivr/compare/v2.0.0-alpha.3...v2.0.0-alpha.4) (2026-10-06)


### Features

* **newsml-g2:** normalize IPTC text news items ([#3778](https://github.com/The-Vibe-Company/quivr/issues/3778)) ([7f3f669](https://github.com/The-Vibe-Company/quivr/commit/7f3f6695a06ae3e06b4a8ce62bf98b7742be27f7))


### Bug Fixes

* **images:** bound signing and verify large SBOMs without log stalls ([#3783](https://github.com/The-Vibe-Company/quivr/issues/3783)) ([4e4b921](https://github.com/The-Vibe-Company/quivr/commit/4e4b92161ae58b21eede6154f6d3e59f454469ce))
* **ops:** restore safe configuration diagnostics and UTC logs ([#3779](https://github.com/The-Vibe-Company/quivr/issues/3779)) ([daa2087](https://github.com/The-Vibe-Company/quivr/commit/daa2087f7ed8450b819693bdd659e9413229f643))

## [2.0.0-alpha.3](https://github.com/The-Vibe-Company/quivr/compare/v2.0.0-alpha.2...v2.0.0-alpha.3) (2026-10-06)


### Bug Fixes

* **images:** update Python runtimes and scan release images before merge ([#3776](https://github.com/The-Vibe-Company/quivr/issues/3776)) ([ae52859](https://github.com/The-Vibe-Company/quivr/commit/ae52859093109ca2fccf87aa16d6e023400c1cfa))

## [2.0.0-alpha.2](https://github.com/The-Vibe-Company/quivr/compare/v2.0.0-alpha.1...v2.0.0-alpha.2) (2026-10-06)


### Features

* **migrations:** preserve rollback during schema upgrades ([#3766](https://github.com/The-Vibe-Company/quivr/issues/3766)) ([9f09a43](https://github.com/The-Vibe-Company/quivr/commit/9f09a433ae52cc041652324ff55c81d45f6eeecf))
* **security:** attest release SBOMs and scan vulnerabilities ([#3767](https://github.com/The-Vibe-Company/quivr/issues/3767)) ([b6e0dd0](https://github.com/The-Vibe-Company/quivr/commit/b6e0dd0f418ea450315a7ece29d212d170e8f7c7))


### Bug Fixes

* **eval:** preserve campaign results through compute cleanup ([#3775](https://github.com/The-Vibe-Company/quivr/issues/3775)) ([3da2945](https://github.com/The-Vibe-Company/quivr/commit/3da29458f5d75f6c76d46bf17972bb81c29788a2))


### Performance Improvements

* **ci:** return quick checks in parallel ([#3773](https://github.com/The-Vibe-Company/quivr/issues/3773)) ([d67da0e](https://github.com/The-Vibe-Company/quivr/commit/d67da0e935badc81d1f36ccb8f141559dd1c9887))

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
