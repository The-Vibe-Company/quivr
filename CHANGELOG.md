# Changelog

## [2.0.0-alpha.6](https://github.com/The-Vibe-Company/quivr/compare/v2.0.0-alpha.5...v2.0.0-alpha.6) (2026-10-08)


### Features

* **autoscaling:** scale bulk workers from queue backlog ([#3818](https://github.com/The-Vibe-Company/quivr/issues/3818)) ([b7267a7](https://github.com/The-Vibe-Company/quivr/commit/b7267a7f52d9c0c275372ddf87db299fd87a0dda))
* **connectors:** pause and resume continuing imports ([#3844](https://github.com/The-Vibe-Company/quivr/issues/3844)) ([fa06fe4](https://github.com/The-Vibe-Company/quivr/commit/fa06fe4cf274a798147929733b6c5da26c0133b6))
* **content:** count metadata values for facets ([#3789](https://github.com/The-Vibe-Company/quivr/issues/3789)) ([63d76a9](https://github.com/The-Vibe-Company/quivr/commit/63d76a953fb3e42beb6fb771e86eec0f0d0fa009))
* **corpora:** add archive, restore and rename ([#3817](https://github.com/The-Vibe-Company/quivr/issues/3817)) ([939e273](https://github.com/The-Vibe-Company/quivr/commit/939e273e61a2bcd28fc809886b18d4c57542d5d8))
* **deploy:** let the Railway demo drop core.ingest when hosted embeddings serve ([#3834](https://github.com/The-Vibe-Company/quivr/issues/3834)) ([782d9d2](https://github.com/The-Vibe-Company/quivr/commit/782d9d270753fd34da0267cde61b80cb2a8aef33))
* **deploy:** serve EmbeddingGemma 2 on Modal ([#3811](https://github.com/The-Vibe-Company/quivr/issues/3811)) ([1bbc9a1](https://github.com/The-Vibe-Company/quivr/commit/1bbc9a18adf70e711dc3fe42d060c7cefe0ae72d))
* **deploy:** set rebuild concurrency from a Railway variable ([#3823](https://github.com/The-Vibe-Company/quivr/issues/3823)) ([7ad49b5](https://github.com/The-Vibe-Company/quivr/commit/7ad49b5e3cbdc034e79bb2353233ea472f22d54f))
* **embeddings:** encode queries beside the API on CPU ([#3830](https://github.com/The-Vibe-Company/quivr/issues/3830)) ([8a9d155](https://github.com/The-Vibe-Company/quivr/commit/8a9d1557db1271c30108d4d277ef68cfdf72fb90))
* **eval:** add text-only EmbeddingGemma 2 bake-off ([#3802](https://github.com/The-Vibe-Company/quivr/issues/3802)) ([9a06b2b](https://github.com/The-Vibe-Company/quivr/commit/9a06b2bd9d9869662abd009d2ffe68ee6bc41e33))
* **ingestion:** pack paragraphs into hosted embedding passages ([#3827](https://github.com/The-Vibe-Company/quivr/issues/3827)) ([1ff2e28](https://github.com/The-Vibe-Company/quivr/commit/1ff2e28673013c0e9249e74b6552bf202385046d))
* **ingestion:** preserve recipes when tuning provider execution ([#3845](https://github.com/The-Vibe-Company/quivr/issues/3845)) ([92de9e8](https://github.com/The-Vibe-Company/quivr/commit/92de9e81d16cf30b44456a5a98059f2ebd593fd3))
* **quarantine:** restart reprocessing from normalization ([#3796](https://github.com/The-Vibe-Company/quivr/issues/3796)) ([f51bfe3](https://github.com/The-Vibe-Company/quivr/commit/f51bfe3147a96424ee245b15cf678eca286ae596))
* **quivr-search:** find sharper source logos, SVG included ([#3785](https://github.com/The-Vibe-Company/quivr/issues/3785)) ([c990d71](https://github.com/The-Vibe-Company/quivr/commit/c990d7130e9325b44444b6b7814a3d8e1d59a998))
* **quivr-search:** open the Explorer from its address only ([#3820](https://github.com/The-Vibe-Company/quivr/issues/3820)) ([7eca7a0](https://github.com/The-Vibe-Company/quivr/commit/7eca7a0f1927e2150eb36fd5ed0d57be8d773a20))
* **quivr-search:** rebuild the Explorer to match the approved layout ([#3804](https://github.com/The-Vibe-Company/quivr/issues/3804)) ([52b939a](https://github.com/The-Vibe-Company/quivr/commit/52b939aa11a93288bb96e8390beddcf17d899ba5))
* **quivr-search:** redesign the Explorer around a timeline and a preview panel ([#3797](https://github.com/The-Vibe-Company/quivr/issues/3797)) ([d753c3c](https://github.com/The-Vibe-Company/quivr/commit/d753c3c884be77efb75fb2f2cbcb9a31fdd708d9))
* **quivr-search:** show counts next to each filter value in the Explorer ([#3793](https://github.com/The-Vibe-Company/quivr/issues/3793)) ([0a0ca1d](https://github.com/The-Vibe-Company/quivr/commit/0a0ca1d1b66ffb7d03cd5c38a59a91dda05a2bf5))
* **retrieval:** rank configurable keywords once per item ([#3829](https://github.com/The-Vibe-Company/quivr/issues/3829)) ([08a63e0](https://github.com/The-Vibe-Company/quivr/commit/08a63e0a3962f13f50134cfc1e33ab846509c095))
* **storage:** compact durable import data ([#3840](https://github.com/The-Vibe-Company/quivr/issues/3840)) ([951edd2](https://github.com/The-Vibe-Company/quivr/commit/951edd274938e34ece4408f8d575d6efe4ddb29f))
* **workers:** isolate live and bulk queues ([#3814](https://github.com/The-Vibe-Company/quivr/issues/3814)) ([5a288ea](https://github.com/The-Vibe-Company/quivr/commit/5a288eaf05f601b8f2d8e8a8f7b65e5cbfe2654f))


### Bug Fixes

* **acceptance:** await collected record retrieval readiness ([#3816](https://github.com/The-Vibe-Company/quivr/issues/3816)) ([254e437](https://github.com/The-Vibe-Company/quivr/commit/254e437941433d314c67a9483e07e85f63c7d295))
* **autoscaling:** deploy Railway regional replica changes ([#3826](https://github.com/The-Vibe-Company/quivr/issues/3826)) ([9e4180e](https://github.com/The-Vibe-Company/quivr/commit/9e4180e32db49e5859b891740c47dcea68af22c3))
* **deploy:** never cap passages per item on the Railway demo ([#3833](https://github.com/The-Vibe-Company/quivr/issues/3833)) ([456f8a1](https://github.com/The-Vibe-Company/quivr/commit/456f8a1384063b3aa08faa7c565de1c473c6626d))
* **deploy:** read the pinned EmbeddingGemma model card by revision on Modal ([#3812](https://github.com/The-Vibe-Company/quivr/issues/3812)) ([c2330d9](https://github.com/The-Vibe-Company/quivr/commit/c2330d9025222f01d6a308a5754f08493550ca13))
* **eval:** bound campaign compute and retain trial evidence ([#3794](https://github.com/The-Vibe-Company/quivr/issues/3794)) ([d75e4fd](https://github.com/The-Vibe-Company/quivr/commit/d75e4fd6a21cd772968ddad1c54486985ec833af))
* **eval:** load EmbeddingGemma 2's model card and processor on Modal ([#3807](https://github.com/The-Vibe-Company/quivr/issues/3807)) ([a990de4](https://github.com/The-Vibe-Company/quivr/commit/a990de409d7a13802f383b9b3a9f6ff8a7de3455))
* **ingestion:** distinguish model settings in derivation identity ([#3813](https://github.com/The-Vibe-Company/quivr/issues/3813)) ([150842e](https://github.com/The-Vibe-Company/quivr/commit/150842ef10571825e0bae27e89e8908f7f565a98))
* **ingestion:** embed complete items and isolate rebuild failures ([#3837](https://github.com/The-Vibe-Company/quivr/issues/3837)) ([d717be3](https://github.com/The-Vibe-Company/quivr/commit/d717be3af155f9da4400fa82c4017b9794412648))
* **modal:** accept packed embedding passages ([#3832](https://github.com/The-Vibe-Company/quivr/issues/3832)) ([116f618](https://github.com/The-Vibe-Company/quivr/commit/116f618517be879b145034367a6500f66f655cf0))
* **newsml-g2:** import messages within extension limits ([#3792](https://github.com/The-Vibe-Company/quivr/issues/3792)) ([6b37e84](https://github.com/The-Vibe-Company/quivr/commit/6b37e84532e3fba8ed257813364ab092845f51bb))
* **postgres:** index segments by segmentation during upgrades ([#3825](https://github.com/The-Vibe-Company/quivr/issues/3825)) ([9e84e3f](https://github.com/The-Vibe-Company/quivr/commit/9e84e3f9665fe8fea29d04611905b67a53f564db))
* **postgres:** keep index builds off the startup path ([#3836](https://github.com/The-Vibe-Company/quivr/issues/3836)) ([564e53b](https://github.com/The-Vibe-Company/quivr/commit/564e53bc2103f1fc187821c86862ccf9beaca84e))
* **processing:** adopt a vector another derivation stored first ([#3824](https://github.com/The-Vibe-Company/quivr/issues/3824)) ([e2805fa](https://github.com/The-Vibe-Company/quivr/commit/e2805faf8953d85990d9a8035af4b4af50c91743))
* **processing:** keep imports searchable during recipe changes ([#3839](https://github.com/The-Vibe-Company/quivr/issues/3839)) ([18c678a](https://github.com/The-Vibe-Company/quivr/commit/18c678a21bcabff36eb97b2677514b02f170dc90))
* **quivr-search:** keep a chart tooltip on its bar when the page moves ([#3790](https://github.com/The-Vibe-Company/quivr/issues/3790)) ([994a317](https://github.com/The-Vibe-Company/quivr/commit/994a31778c33c1a3f981192d6fb5da19e8a936dc))
* **quivr-search:** keep the feed's controls in place while searching or filtering ([#3822](https://github.com/The-Vibe-Company/quivr/issues/3822)) ([f31dd5e](https://github.com/The-Vibe-Company/quivr/commit/f31dd5e013cfd70d27c3f96f65b8cfbdee5d0677))
* **railway:** let the demo operator key archive and rename corpora ([#3821](https://github.com/The-Vibe-Company/quivr/issues/3821)) ([650905e](https://github.com/The-Vibe-Company/quivr/commit/650905e947bf15c80bb2556aa0899864f777bf0c))
* **railway:** make hosted embeddings the ingestion default ([#3798](https://github.com/The-Vibe-Company/quivr/issues/3798)) ([e312794](https://github.com/The-Vibe-Company/quivr/commit/e312794eb2429f3f1691e852e56fc04fd1f74254))
* **rebuild:** keep candidate lookup steady and batch writes ([#3831](https://github.com/The-Vibe-Company/quivr/issues/3831)) ([a8ec70e](https://github.com/The-Vibe-Company/quivr/commit/a8ec70e24f098c050ea90b5256364401e4ca43ee))
* **retrieval:** serve coverage snapshots without blocking search ([#3819](https://github.com/The-Vibe-Company/quivr/issues/3819)) ([1cbd569](https://github.com/The-Vibe-Company/quivr/commit/1cbd5693ddea9b4b4065cf8f2b1a7363dc05b1cf))
* **retrieval:** stop rebuilds that leave coverage unchanged ([#3803](https://github.com/The-Vibe-Company/quivr/issues/3803)) ([c94f1b8](https://github.com/The-Vibe-Company/quivr/commit/c94f1b8a0b8dbecf8ab74378353ce2c54ccfdd91))


### Performance Improvements

* **connectors:** acquire archive pages without interval gaps ([#3838](https://github.com/The-Vibe-Company/quivr/issues/3838)) ([568de15](https://github.com/The-Vibe-Company/quivr/commit/568de1535cb7c9d04b86d5c22d2ec2e470f86172))
* **ingestion:** batch archive embeddings across versions ([#3795](https://github.com/The-Vibe-Company/quivr/issues/3795)) ([62e7c86](https://github.com/The-Vibe-Company/quivr/commit/62e7c865385d55df07192471d099c4576f1b16ac))
* **postgres:** bound queue observations and version purge discovery ([#3841](https://github.com/The-Vibe-Company/quivr/issues/3841)) ([a9e92fb](https://github.com/The-Vibe-Company/quivr/commit/a9e92fb193e6c1f9b4f3673c9abb231014e71adf))
* **postgres:** move writes ahead of journal locks ([#3828](https://github.com/The-Vibe-Company/quivr/issues/3828)) ([03a0c38](https://github.com/The-Vibe-Company/quivr/commit/03a0c388edaf1d7ad393808444af9c593bfdf5fc))
* **queues:** throttle backlog refreshes and use operation counters ([#3843](https://github.com/The-Vibe-Company/quivr/issues/3843)) ([98918e2](https://github.com/The-Vibe-Company/quivr/commit/98918e27356c7fd5a62cd4fb0529064c281e0a15))
* **railway:** increase hosted embedding throughput ([#3799](https://github.com/The-Vibe-Company/quivr/issues/3799)) ([2a3e96d](https://github.com/The-Vibe-Company/quivr/commit/2a3e96def837f1f882a12ed92371663de57bba0d))
* **railway:** send 16 concurrent Gemma embedding requests ([#3847](https://github.com/The-Vibe-Company/quivr/issues/3847)) ([cab7ebd](https://github.com/The-Vibe-Company/quivr/commit/cab7ebdd32f61128f1088059d2efa1aefe7db27f))
* **rebuild:** allow up to 256 versions per rebuild step ([#3842](https://github.com/The-Vibe-Company/quivr/issues/3842)) ([e5dea49](https://github.com/The-Vibe-Company/quivr/commit/e5dea4921f8c8940514e8fc9209c8c0969d77567))
* **retrieval:** re-embed rebuild candidates concurrently ([#3806](https://github.com/The-Vibe-Company/quivr/issues/3806)) ([c3d4340](https://github.com/The-Vibe-Company/quivr/commit/c3d43402cacc767986b5e453d94504acc2a62e86))

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
