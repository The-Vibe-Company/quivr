---
language:
- multilingual
- af
- am
- ar
- as
- az
- be
- bg
- bn
- br
- bs
- ca
- cs
- cy
- da
- de
- el
- en
- eo
- es
- et
- eu
- fa
- fi
- fr
- fy
- ga
- gd
- gl
- gu
- ha
- he
- hi
- hr
- hu
- hy
- id
- is
- it
- ja
- jv
- ka
- kk
- km
- kn
- ko
- ku
- ky
- la
- lo
- lt
- lv
- mg
- mk
- ml
- mn
- mr
- ms
- my
- ne
- nl
- 'no'
- om
- or
- pa
- pl
- ps
- pt
- ro
- ru
- sa
- sd
- si
- sk
- sl
- so
- sq
- sr
- su
- sv
- sw
- ta
- te
- th
- tl
- tr
- ug
- uk
- ur
- uz
- vi
- xh
- yi
- zh
license: mit
model-index:
- name: intfloat/multilingual-e5-small
  results:
  - dataset:
      config: en
      name: MTEB AmazonCounterfactualClassification (en)
      revision: e8379541af4e31359cca9fbcf4b00f2671dba205
      split: test
      type: mteb/amazon_counterfactual
    metrics:
    - type: accuracy
      value: 73.79104477611939
    - type: ap
      value: 36.9996434842022
    - type: f1
      value: 67.95453679103099
    task:
      type: Classification
  - dataset:
      config: de
      name: MTEB AmazonCounterfactualClassification (de)
      revision: e8379541af4e31359cca9fbcf4b00f2671dba205
      split: test
      type: mteb/amazon_counterfactual
    metrics:
    - type: accuracy
      value: 71.64882226980728
    - type: ap
      value: 82.11942130026586
    - type: f1
      value: 69.87963421606715
    task:
      type: Classification
  - dataset:
      config: en-ext
      name: MTEB AmazonCounterfactualClassification (en-ext)
      revision: e8379541af4e31359cca9fbcf4b00f2671dba205
      split: test
      type: mteb/amazon_counterfactual
    metrics:
    - type: accuracy
      value: 75.8095952023988
    - type: ap
      value: 24.46869495579561
    - type: f1
      value: 63.00108480037597
    task:
      type: Classification
  - dataset:
      config: ja
      name: MTEB AmazonCounterfactualClassification (ja)
      revision: e8379541af4e31359cca9fbcf4b00f2671dba205
      split: test
      type: mteb/amazon_counterfactual
    metrics:
    - type: accuracy
      value: 64.186295503212
    - type: ap
      value: 15.496804690197042
    - type: f1
      value: 52.07153895475031
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB AmazonPolarityClassification
      revision: e2d317d38cd51312af73b3d32a06d1a08b442046
      split: test
      type: mteb/amazon_polarity
    metrics:
    - type: accuracy
      value: 88.699325
    - type: ap
      value: 85.27039559917269
    - type: f1
      value: 88.65556295032513
    task:
      type: Classification
  - dataset:
      config: en
      name: MTEB AmazonReviewsClassification (en)
      revision: 1399c76144fd37290681b995c656ef9b2e06e26d
      split: test
      type: mteb/amazon_reviews_multi
    metrics:
    - type: accuracy
      value: 44.69799999999999
    - type: f1
      value: 43.73187348654165
    task:
      type: Classification
  - dataset:
      config: de
      name: MTEB AmazonReviewsClassification (de)
      revision: 1399c76144fd37290681b995c656ef9b2e06e26d
      split: test
      type: mteb/amazon_reviews_multi
    metrics:
    - type: accuracy
      value: 40.245999999999995
    - type: f1
      value: 39.3863530637684
    task:
      type: Classification
  - dataset:
      config: es
      name: MTEB AmazonReviewsClassification (es)
      revision: 1399c76144fd37290681b995c656ef9b2e06e26d
      split: test
      type: mteb/amazon_reviews_multi
    metrics:
    - type: accuracy
      value: 40.394
    - type: f1
      value: 39.301223469483446
    task:
      type: Classification
  - dataset:
      config: fr
      name: MTEB AmazonReviewsClassification (fr)
      revision: 1399c76144fd37290681b995c656ef9b2e06e26d
      split: test
      type: mteb/amazon_reviews_multi
    metrics:
    - type: accuracy
      value: 38.864
    - type: f1
      value: 37.97974261868003
    task:
      type: Classification
  - dataset:
      config: ja
      name: MTEB AmazonReviewsClassification (ja)
      revision: 1399c76144fd37290681b995c656ef9b2e06e26d
      split: test
      type: mteb/amazon_reviews_multi
    metrics:
    - type: accuracy
      value: 37.682
    - type: f1
      value: 37.07399369768313
    task:
      type: Classification
  - dataset:
      config: zh
      name: MTEB AmazonReviewsClassification (zh)
      revision: 1399c76144fd37290681b995c656ef9b2e06e26d
      split: test
      type: mteb/amazon_reviews_multi
    metrics:
    - type: accuracy
      value: 37.504
    - type: f1
      value: 36.62317273874278
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB ArguAna
      revision: None
      split: test
      type: arguana
    metrics:
    - type: map_at_1
      value: 19.061
    - type: map_at_10
      value: 31.703
    - type: map_at_100
      value: 32.967
    - type: map_at_1000
      value: 33.001000000000005
    - type: map_at_3
      value: 27.466
    - type: map_at_5
      value: 29.564
    - type: mrr_at_1
      value: 19.559
    - type: mrr_at_10
      value: 31.874999999999996
    - type: mrr_at_100
      value: 33.146
    - type: mrr_at_1000
      value: 33.18
    - type: mrr_at_3
      value: 27.667
    - type: mrr_at_5
      value: 29.74
    - type: ndcg_at_1
      value: 19.061
    - type: ndcg_at_10
      value: 39.062999999999995
    - type: ndcg_at_100
      value: 45.184000000000005
    - type: ndcg_at_1000
      value: 46.115
    - type: ndcg_at_3
      value: 30.203000000000003
    - type: ndcg_at_5
      value: 33.953
    - type: precision_at_1
      value: 19.061
    - type: precision_at_10
      value: 6.279999999999999
    - type: precision_at_100
      value: 0.9129999999999999
    - type: precision_at_1000
      value: 0.099
    - type: precision_at_3
      value: 12.706999999999999
    - type: precision_at_5
      value: 9.431000000000001
    - type: recall_at_1
      value: 19.061
    - type: recall_at_10
      value: 62.802
    - type: recall_at_100
      value: 91.323
    - type: recall_at_1000
      value: 98.72
    - type: recall_at_3
      value: 38.122
    - type: recall_at_5
      value: 47.155
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB ArxivClusteringP2P
      revision: a122ad7f3f0291bf49cc6f4d32aa80929df69d5d
      split: test
      type: mteb/arxiv-clustering-p2p
    metrics:
    - type: v_measure
      value: 39.22266660528253
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB ArxivClusteringS2S
      revision: f910caf1a6075f7329cdf8c1a6135696f37dbd53
      split: test
      type: mteb/arxiv-clustering-s2s
    metrics:
    - type: v_measure
      value: 30.79980849482483
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB AskUbuntuDupQuestions
      revision: 2000358ca161889fa9c082cb41daa8dcfb161a54
      split: test
      type: mteb/askubuntudupquestions-reranking
    metrics:
    - type: map
      value: 57.8790068352054
    - type: mrr
      value: 71.78791276436706
    task:
      type: Reranking
  - dataset:
      config: default
      name: MTEB BIOSSES
      revision: d3fb88f8f02e40887cd149695127462bbcf29b4a
      split: test
      type: mteb/biosses-sts
    metrics:
    - type: cos_sim_pearson
      value: 82.36328364043163
    - type: cos_sim_spearman
      value: 82.26211536195868
    - type: euclidean_pearson
      value: 80.3183865039173
    - type: euclidean_spearman
      value: 79.88495276296132
    - type: manhattan_pearson
      value: 80.14484480692127
    - type: manhattan_spearman
      value: 80.39279565980743
    task:
      type: STS
  - dataset:
      config: de-en
      name: MTEB BUCC (de-en)
      revision: d51519689f32196a32af33b075a01d0e7c51e252
      split: test
      type: mteb/bucc-bitext-mining
    metrics:
    - type: accuracy
      value: 98.0375782881002
    - type: f1
      value: 97.86012526096033
    - type: precision
      value: 97.77139874739039
    - type: recall
      value: 98.0375782881002
    task:
      type: BitextMining
  - dataset:
      config: fr-en
      name: MTEB BUCC (fr-en)
      revision: d51519689f32196a32af33b075a01d0e7c51e252
      split: test
      type: mteb/bucc-bitext-mining
    metrics:
    - type: accuracy
      value: 93.35241030156286
    - type: f1
      value: 92.66050333846944
    - type: precision
      value: 92.3306919069631
    - type: recall
      value: 93.35241030156286
    task:
      type: BitextMining
  - dataset:
      config: ru-en
      name: MTEB BUCC (ru-en)
      revision: d51519689f32196a32af33b075a01d0e7c51e252
      split: test
      type: mteb/bucc-bitext-mining
    metrics:
    - type: accuracy
      value: 94.0699688257707
    - type: f1
      value: 93.50236693222492
    - type: precision
      value: 93.22791825424315
    - type: recall
      value: 94.0699688257707
    task:
      type: BitextMining
  - dataset:
      config: zh-en
      name: MTEB BUCC (zh-en)
      revision: d51519689f32196a32af33b075a01d0e7c51e252
      split: test
      type: mteb/bucc-bitext-mining
    metrics:
    - type: accuracy
      value: 89.25750394944708
    - type: f1
      value: 88.79234684921889
    - type: precision
      value: 88.57293312269616
    - type: recall
      value: 89.25750394944708
    task:
      type: BitextMining
  - dataset:
      config: default
      name: MTEB Banking77Classification
      revision: 0fd18e25b25c072e09e0d92ab615fda904d66300
      split: test
      type: mteb/banking77
    metrics:
    - type: accuracy
      value: 79.41558441558442
    - type: f1
      value: 79.25886487487219
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB BiorxivClusteringP2P
      revision: 65b79d1d13f80053f67aca9498d9402c2d9f1f40
      split: test
      type: mteb/biorxiv-clustering-p2p
    metrics:
    - type: v_measure
      value: 35.747820820329736
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB BiorxivClusteringS2S
      revision: 258694dd0231531bc1fd9de6ceb52a0853c6d908
      split: test
      type: mteb/biorxiv-clustering-s2s
    metrics:
    - type: v_measure
      value: 27.045143830596146
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB CQADupstackRetrieval
      revision: None
      split: test
      type: BeIR/cqadupstack
    metrics:
    - type: map_at_1
      value: 24.252999999999997
    - type: map_at_10
      value: 31.655916666666666
    - type: map_at_100
      value: 32.680749999999996
    - type: map_at_1000
      value: 32.79483333333334
    - type: map_at_3
      value: 29.43691666666666
    - type: map_at_5
      value: 30.717416666666665
    - type: mrr_at_1
      value: 28.602750000000004
    - type: mrr_at_10
      value: 35.56875
    - type: mrr_at_100
      value: 36.3595
    - type: mrr_at_1000
      value: 36.427749999999996
    - type: mrr_at_3
      value: 33.586166666666664
    - type: mrr_at_5
      value: 34.73641666666666
    - type: ndcg_at_1
      value: 28.602750000000004
    - type: ndcg_at_10
      value: 36.06933333333334
    - type: ndcg_at_100
      value: 40.70141666666667
    - type: ndcg_at_1000
      value: 43.24341666666667
    - type: ndcg_at_3
      value: 32.307916666666664
    - type: ndcg_at_5
      value: 34.129999999999995
    - type: precision_at_1
      value: 28.602750000000004
    - type: precision_at_10
      value: 6.097666666666667
    - type: precision_at_100
      value: 0.9809166666666668
    - type: precision_at_1000
      value: 0.13766666666666663
    - type: precision_at_3
      value: 14.628166666666667
    - type: precision_at_5
      value: 10.266916666666667
    - type: recall_at_1
      value: 24.252999999999997
    - type: recall_at_10
      value: 45.31916666666667
    - type: recall_at_100
      value: 66.03575000000001
    - type: recall_at_1000
      value: 83.94708333333334
    - type: recall_at_3
      value: 34.71941666666666
    - type: recall_at_5
      value: 39.46358333333333
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB ClimateFEVER
      revision: None
      split: test
      type: climate-fever
    metrics:
    - type: map_at_1
      value: 9.024000000000001
    - type: map_at_10
      value: 15.644
    - type: map_at_100
      value: 17.154
    - type: map_at_1000
      value: 17.345
    - type: map_at_3
      value: 13.028
    - type: map_at_5
      value: 14.251
    - type: mrr_at_1
      value: 19.674
    - type: mrr_at_10
      value: 29.826999999999998
    - type: mrr_at_100
      value: 30.935000000000002
    - type: mrr_at_1000
      value: 30.987
    - type: mrr_at_3
      value: 26.645000000000003
    - type: mrr_at_5
      value: 28.29
    - type: ndcg_at_1
      value: 19.674
    - type: ndcg_at_10
      value: 22.545
    - type: ndcg_at_100
      value: 29.207
    - type: ndcg_at_1000
      value: 32.912
    - type: ndcg_at_3
      value: 17.952
    - type: ndcg_at_5
      value: 19.363
    - type: precision_at_1
      value: 19.674
    - type: precision_at_10
      value: 7.212000000000001
    - type: precision_at_100
      value: 1.435
    - type: precision_at_1000
      value: 0.212
    - type: precision_at_3
      value: 13.507
    - type: precision_at_5
      value: 10.397
    - type: recall_at_1
      value: 9.024000000000001
    - type: recall_at_10
      value: 28.077999999999996
    - type: recall_at_100
      value: 51.403
    - type: recall_at_1000
      value: 72.406
    - type: recall_at_3
      value: 16.768
    - type: recall_at_5
      value: 20.737
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB DBPedia
      revision: None
      split: test
      type: dbpedia-entity
    metrics:
    - type: map_at_1
      value: 8.012
    - type: map_at_10
      value: 17.138
    - type: map_at_100
      value: 24.146
    - type: map_at_1000
      value: 25.622
    - type: map_at_3
      value: 12.552
    - type: map_at_5
      value: 14.435
    - type: mrr_at_1
      value: 62.25000000000001
    - type: mrr_at_10
      value: 71.186
    - type: mrr_at_100
      value: 71.504
    - type: mrr_at_1000
      value: 71.514
    - type: mrr_at_3
      value: 69.333
    - type: mrr_at_5
      value: 70.408
    - type: ndcg_at_1
      value: 49.75
    - type: ndcg_at_10
      value: 37.76
    - type: ndcg_at_100
      value: 42.071
    - type: ndcg_at_1000
      value: 49.309
    - type: ndcg_at_3
      value: 41.644
    - type: ndcg_at_5
      value: 39.812999999999995
    - type: precision_at_1
      value: 62.25000000000001
    - type: precision_at_10
      value: 30.15
    - type: precision_at_100
      value: 9.753
    - type: precision_at_1000
      value: 1.9189999999999998
    - type: precision_at_3
      value: 45.667
    - type: precision_at_5
      value: 39.15
    - type: recall_at_1
      value: 8.012
    - type: recall_at_10
      value: 22.599
    - type: recall_at_100
      value: 48.068
    - type: recall_at_1000
      value: 71.328
    - type: recall_at_3
      value: 14.043
    - type: recall_at_5
      value: 17.124
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB EmotionClassification
      revision: 4f58c6b202a23cf9a4da393831edf4f9183cad37
      split: test
      type: mteb/emotion
    metrics:
    - type: accuracy
      value: 42.455
    - type: f1
      value: 37.59462649781862
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB FEVER
      revision: None
      split: test
      type: fever
    metrics:
    - type: map_at_1
      value: 58.092
    - type: map_at_10
      value: 69.586
    - type: map_at_100
      value: 69.968
    - type: map_at_1000
      value: 69.982
    - type: map_at_3
      value: 67.48100000000001
    - type: map_at_5
      value: 68.915
    - type: mrr_at_1
      value: 62.166
    - type: mrr_at_10
      value: 73.588
    - type: mrr_at_100
      value: 73.86399999999999
    - type: mrr_at_1000
      value: 73.868
    - type: mrr_at_3
      value: 71.6
    - type: mrr_at_5
      value: 72.99
    - type: ndcg_at_1
      value: 62.166
    - type: ndcg_at_10
      value: 75.27199999999999
    - type: ndcg_at_100
      value: 76.816
    - type: ndcg_at_1000
      value: 77.09700000000001
    - type: ndcg_at_3
      value: 71.36
    - type: ndcg_at_5
      value: 73.785
    - type: precision_at_1
      value: 62.166
    - type: precision_at_10
      value: 9.716
    - type: precision_at_100
      value: 1.065
    - type: precision_at_1000
      value: 0.11
    - type: precision_at_3
      value: 28.278
    - type: precision_at_5
      value: 18.343999999999998
    - type: recall_at_1
      value: 58.092
    - type: recall_at_10
      value: 88.73400000000001
    - type: recall_at_100
      value: 95.195
    - type: recall_at_1000
      value: 97.04599999999999
    - type: recall_at_3
      value: 78.45
    - type: recall_at_5
      value: 84.316
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB FiQA2018
      revision: None
      split: test
      type: fiqa
    metrics:
    - type: map_at_1
      value: 16.649
    - type: map_at_10
      value: 26.457000000000004
    - type: map_at_100
      value: 28.169
    - type: map_at_1000
      value: 28.352
    - type: map_at_3
      value: 23.305
    - type: map_at_5
      value: 25.169000000000004
    - type: mrr_at_1
      value: 32.407000000000004
    - type: mrr_at_10
      value: 40.922
    - type: mrr_at_100
      value: 41.931000000000004
    - type: mrr_at_1000
      value: 41.983
    - type: mrr_at_3
      value: 38.786
    - type: mrr_at_5
      value: 40.205999999999996
    - type: ndcg_at_1
      value: 32.407000000000004
    - type: ndcg_at_10
      value: 33.314
    - type: ndcg_at_100
      value: 40.312
    - type: ndcg_at_1000
      value: 43.685
    - type: ndcg_at_3
      value: 30.391000000000002
    - type: ndcg_at_5
      value: 31.525
    - type: precision_at_1
      value: 32.407000000000004
    - type: precision_at_10
      value: 8.966000000000001
    - type: precision_at_100
      value: 1.6019999999999999
    - type: precision_at_1000
      value: 0.22200000000000003
    - type: precision_at_3
      value: 20.165
    - type: precision_at_5
      value: 14.722
    - type: recall_at_1
      value: 16.649
    - type: recall_at_10
      value: 39.117000000000004
    - type: recall_at_100
      value: 65.726
    - type: recall_at_1000
      value: 85.784
    - type: recall_at_3
      value: 27.914
    - type: recall_at_5
      value: 33.289
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB HotpotQA
      revision: None
      split: test
      type: hotpotqa
    metrics:
    - type: map_at_1
      value: 36.253
    - type: map_at_10
      value: 56.16799999999999
    - type: map_at_100
      value: 57.06099999999999
    - type: map_at_1000
      value: 57.126
    - type: map_at_3
      value: 52.644999999999996
    - type: map_at_5
      value: 54.909
    - type: mrr_at_1
      value: 72.505
    - type: mrr_at_10
      value: 79.66
    - type: mrr_at_100
      value: 79.869
    - type: mrr_at_1000
      value: 79.88
    - type: mrr_at_3
      value: 78.411
    - type: mrr_at_5
      value: 79.19800000000001
    - type: ndcg_at_1
      value: 72.505
    - type: ndcg_at_10
      value: 65.094
    - type: ndcg_at_100
      value: 68.219
    - type: ndcg_at_1000
      value: 69.515
    - type: ndcg_at_3
      value: 59.99
    - type: ndcg_at_5
      value: 62.909000000000006
    - type: precision_at_1
      value: 72.505
    - type: precision_at_10
      value: 13.749
    - type: precision_at_100
      value: 1.619
    - type: precision_at_1000
      value: 0.179
    - type: precision_at_3
      value: 38.357
    - type: precision_at_5
      value: 25.313000000000002
    - type: recall_at_1
      value: 36.253
    - type: recall_at_10
      value: 68.744
    - type: recall_at_100
      value: 80.925
    - type: recall_at_1000
      value: 89.534
    - type: recall_at_3
      value: 57.535000000000004
    - type: recall_at_5
      value: 63.282000000000004
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB ImdbClassification
      revision: 3d86128a09e091d6018b6d26cad27f2739fc2db7
      split: test
      type: mteb/imdb
    metrics:
    - type: accuracy
      value: 80.82239999999999
    - type: ap
      value: 75.65895781725314
    - type: f1
      value: 80.75880969095746
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB MSMARCO
      revision: None
      split: dev
      type: msmarco
    metrics:
    - type: map_at_1
      value: 21.624
    - type: map_at_10
      value: 34.075
    - type: map_at_100
      value: 35.229
    - type: map_at_1000
      value: 35.276999999999994
    - type: map_at_3
      value: 30.245
    - type: map_at_5
      value: 32.42
    - type: mrr_at_1
      value: 22.264
    - type: mrr_at_10
      value: 34.638000000000005
    - type: mrr_at_100
      value: 35.744
    - type: mrr_at_1000
      value: 35.787
    - type: mrr_at_3
      value: 30.891000000000002
    - type: mrr_at_5
      value: 33.042
    - type: ndcg_at_1
      value: 22.264
    - type: ndcg_at_10
      value: 40.991
    - type: ndcg_at_100
      value: 46.563
    - type: ndcg_at_1000
      value: 47.743
    - type: ndcg_at_3
      value: 33.198
    - type: ndcg_at_5
      value: 37.069
    - type: precision_at_1
      value: 22.264
    - type: precision_at_10
      value: 6.5089999999999995
    - type: precision_at_100
      value: 0.9299999999999999
    - type: precision_at_1000
      value: 0.10300000000000001
    - type: precision_at_3
      value: 14.216999999999999
    - type: precision_at_5
      value: 10.487
    - type: recall_at_1
      value: 21.624
    - type: recall_at_10
      value: 62.303
    - type: recall_at_100
      value: 88.124
    - type: recall_at_1000
      value: 97.08
    - type: recall_at_3
      value: 41.099999999999994
    - type: recall_at_5
      value: 50.381
    task:
      type: Retrieval
  - dataset:
      config: en
      name: MTEB MTOPDomainClassification (en)
      revision: d80d48c1eb48d3562165c59d59d0034df9fff0bf
      split: test
      type: mteb/mtop_domain
    metrics:
    - type: accuracy
      value: 91.06703146374831
    - type: f1
      value: 90.86867815863172
    task:
      type: Classification
  - dataset:
      config: de
      name: MTEB MTOPDomainClassification (de)
      revision: d80d48c1eb48d3562165c59d59d0034df9fff0bf
      split: test
      type: mteb/mtop_domain
    metrics:
    - type: accuracy
      value: 87.46970977740209
    - type: f1
      value: 86.36832872036588
    task:
      type: Classification
  - dataset:
      config: es
      name: MTEB MTOPDomainClassification (es)
      revision: d80d48c1eb48d3562165c59d59d0034df9fff0bf
      split: test
      type: mteb/mtop_domain
    metrics:
    - type: accuracy
      value: 89.26951300867245
    - type: f1
      value: 88.93561193959502
    task:
      type: Classification
  - dataset:
      config: fr
      name: MTEB MTOPDomainClassification (fr)
      revision: d80d48c1eb48d3562165c59d59d0034df9fff0bf
      split: test
      type: mteb/mtop_domain
    metrics:
    - type: accuracy
      value: 84.22799874725963
    - type: f1
      value: 84.30490069236556
    task:
      type: Classification
  - dataset:
      config: hi
      name: MTEB MTOPDomainClassification (hi)
      revision: d80d48c1eb48d3562165c59d59d0034df9fff0bf
      split: test
      type: mteb/mtop_domain
    metrics:
    - type: accuracy
      value: 86.02007888131948
    - type: f1
      value: 85.39376041027991
    task:
      type: Classification
  - dataset:
      config: th
      name: MTEB MTOPDomainClassification (th)
      revision: d80d48c1eb48d3562165c59d59d0034df9fff0bf
      split: test
      type: mteb/mtop_domain
    metrics:
    - type: accuracy
      value: 85.34900542495481
    - type: f1
      value: 85.39859673336713
    task:
      type: Classification
  - dataset:
      config: en
      name: MTEB MTOPIntentClassification (en)
      revision: ae001d0e6b1228650b7bd1c2c65fb50ad11a8aba
      split: test
      type: mteb/mtop_intent
    metrics:
    - type: accuracy
      value: 71.078431372549
    - type: f1
      value: 53.45071102002276
    task:
      type: Classification
  - dataset:
      config: de
      name: MTEB MTOPIntentClassification (de)
      revision: ae001d0e6b1228650b7bd1c2c65fb50ad11a8aba
      split: test
      type: mteb/mtop_intent
    metrics:
    - type: accuracy
      value: 65.85798816568047
    - type: f1
      value: 46.53112748993529
    task:
      type: Classification
  - dataset:
      config: es
      name: MTEB MTOPIntentClassification (es)
      revision: ae001d0e6b1228650b7bd1c2c65fb50ad11a8aba
      split: test
      type: mteb/mtop_intent
    metrics:
    - type: accuracy
      value: 67.96864576384256
    - type: f1
      value: 45.966703022829506
    task:
      type: Classification
  - dataset:
      config: fr
      name: MTEB MTOPIntentClassification (fr)
      revision: ae001d0e6b1228650b7bd1c2c65fb50ad11a8aba
      split: test
      type: mteb/mtop_intent
    metrics:
    - type: accuracy
      value: 61.31537738803633
    - type: f1
      value: 45.52601712835461
    task:
      type: Classification
  - dataset:
      config: hi
      name: MTEB MTOPIntentClassification (hi)
      revision: ae001d0e6b1228650b7bd1c2c65fb50ad11a8aba
      split: test
      type: mteb/mtop_intent
    metrics:
    - type: accuracy
      value: 66.29616349946218
    - type: f1
      value: 47.24166485726613
    task:
      type: Classification
  - dataset:
      config: th
      name: MTEB MTOPIntentClassification (th)
      revision: ae001d0e6b1228650b7bd1c2c65fb50ad11a8aba
      split: test
      type: mteb/mtop_intent
    metrics:
    - type: accuracy
      value: 67.51537070524412
    - type: f1
      value: 49.463476319014276
    task:
      type: Classification
  - dataset:
      config: af
      name: MTEB MassiveIntentClassification (af)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 57.06792199058508
    - type: f1
      value: 54.094921857502285
    task:
      type: Classification
  - dataset:
      config: am
      name: MTEB MassiveIntentClassification (am)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 51.960322797579025
    - type: f1
      value: 48.547371223370945
    task:
      type: Classification
  - dataset:
      config: ar
      name: MTEB MassiveIntentClassification (ar)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 54.425016812373904
    - type: f1
      value: 50.47069202054312
    task:
      type: Classification
  - dataset:
      config: az
      name: MTEB MassiveIntentClassification (az)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 59.798251513113655
    - type: f1
      value: 57.05013069086648
    task:
      type: Classification
  - dataset:
      config: bn
      name: MTEB MassiveIntentClassification (bn)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 59.37794216543376
    - type: f1
      value: 56.3607992649805
    task:
      type: Classification
  - dataset:
      config: cy
      name: MTEB MassiveIntentClassification (cy)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 46.56018829858777
    - type: f1
      value: 43.87319715715134
    task:
      type: Classification
  - dataset:
      config: da
      name: MTEB MassiveIntentClassification (da)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 62.9724277067922
    - type: f1
      value: 59.36480066245562
    task:
      type: Classification
  - dataset:
      config: de
      name: MTEB MassiveIntentClassification (de)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 62.72696704774715
    - type: f1
      value: 59.143595966615855
    task:
      type: Classification
  - dataset:
      config: el
      name: MTEB MassiveIntentClassification (el)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 61.5971755211836
    - type: f1
      value: 59.169445724946726
    task:
      type: Classification
  - dataset:
      config: en
      name: MTEB MassiveIntentClassification (en)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 70.29589778076665
    - type: f1
      value: 67.7577001808977
    task:
      type: Classification
  - dataset:
      config: es
      name: MTEB MassiveIntentClassification (es)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 66.31136516476126
    - type: f1
      value: 64.52032955983242
    task:
      type: Classification
  - dataset:
      config: fa
      name: MTEB MassiveIntentClassification (fa)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 65.54472091459314
    - type: f1
      value: 61.47903120066317
    task:
      type: Classification
  - dataset:
      config: fi
      name: MTEB MassiveIntentClassification (fi)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 61.45595158036314
    - type: f1
      value: 58.0891846024637
    task:
      type: Classification
  - dataset:
      config: fr
      name: MTEB MassiveIntentClassification (fr)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 65.47074646940149
    - type: f1
      value: 62.84830858877575
    task:
      type: Classification
  - dataset:
      config: he
      name: MTEB MassiveIntentClassification (he)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 58.046402151983855
    - type: f1
      value: 55.269074430533195
    task:
      type: Classification
  - dataset:
      config: hi
      name: MTEB MassiveIntentClassification (hi)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 64.06523201075991
    - type: f1
      value: 61.35339643021369
    task:
      type: Classification
  - dataset:
      config: hu
      name: MTEB MassiveIntentClassification (hu)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 60.954942837928726
    - type: f1
      value: 57.07035922704846
    task:
      type: Classification
  - dataset:
      config: hy
      name: MTEB MassiveIntentClassification (hy)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 57.404169468728995
    - type: f1
      value: 53.94259011839138
    task:
      type: Classification
  - dataset:
      config: id
      name: MTEB MassiveIntentClassification (id)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 64.16610625420309
    - type: f1
      value: 61.337103431499365
    task:
      type: Classification
  - dataset:
      config: is
      name: MTEB MassiveIntentClassification (is)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 52.262945527908535
    - type: f1
      value: 49.7610691598921
    task:
      type: Classification
  - dataset:
      config: it
      name: MTEB MassiveIntentClassification (it)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 65.54472091459314
    - type: f1
      value: 63.469099018440154
    task:
      type: Classification
  - dataset:
      config: ja
      name: MTEB MassiveIntentClassification (ja)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 68.22797579018157
    - type: f1
      value: 64.89098471083001
    task:
      type: Classification
  - dataset:
      config: jv
      name: MTEB MassiveIntentClassification (jv)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 50.847343644922674
    - type: f1
      value: 47.8536963168393
    task:
      type: Classification
  - dataset:
      config: ka
      name: MTEB MassiveIntentClassification (ka)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 48.45326160053799
    - type: f1
      value: 46.370078045805556
    task:
      type: Classification
  - dataset:
      config: km
      name: MTEB MassiveIntentClassification (km)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 42.83120376597175
    - type: f1
      value: 39.68948521599982
    task:
      type: Classification
  - dataset:
      config: kn
      name: MTEB MassiveIntentClassification (kn)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 57.5084061869536
    - type: f1
      value: 53.961876160401545
    task:
      type: Classification
  - dataset:
      config: ko
      name: MTEB MassiveIntentClassification (ko)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 63.7895090786819
    - type: f1
      value: 61.134223684676
    task:
      type: Classification
  - dataset:
      config: lv
      name: MTEB MassiveIntentClassification (lv)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 54.98991257565569
    - type: f1
      value: 52.579862862826296
    task:
      type: Classification
  - dataset:
      config: ml
      name: MTEB MassiveIntentClassification (ml)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 61.90316072629456
    - type: f1
      value: 58.203024538290336
    task:
      type: Classification
  - dataset:
      config: mn
      name: MTEB MassiveIntentClassification (mn)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 57.09818426361802
    - type: f1
      value: 54.22718458445455
    task:
      type: Classification
  - dataset:
      config: ms
      name: MTEB MassiveIntentClassification (ms)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 58.991257565568255
    - type: f1
      value: 55.84892781767421
    task:
      type: Classification
  - dataset:
      config: my
      name: MTEB MassiveIntentClassification (my)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 55.901143241425686
    - type: f1
      value: 52.25264332199797
    task:
      type: Classification
  - dataset:
      config: nb
      name: MTEB MassiveIntentClassification (nb)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 61.96368527236047
    - type: f1
      value: 58.927243876153454
    task:
      type: Classification
  - dataset:
      config: nl
      name: MTEB MassiveIntentClassification (nl)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 65.64223268325489
    - type: f1
      value: 62.340453718379706
    task:
      type: Classification
  - dataset:
      config: pl
      name: MTEB MassiveIntentClassification (pl)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 64.52589105581708
    - type: f1
      value: 61.661113187022174
    task:
      type: Classification
  - dataset:
      config: pt
      name: MTEB MassiveIntentClassification (pt)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 66.84599865501009
    - type: f1
      value: 64.59342572873005
    task:
      type: Classification
  - dataset:
      config: ro
      name: MTEB MassiveIntentClassification (ro)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 60.81035642232684
    - type: f1
      value: 57.5169089806797
    task:
      type: Classification
  - dataset:
      config: ru
      name: MTEB MassiveIntentClassification (ru)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 58.652238071815056
    - type: f1
      value: 53.22732406426353
    - type: f1_weighted
      value: 57.585586737209546
    - type: main_score
      value: 58.652238071815056
    task:
      type: Classification
  - dataset:
      config: sl
      name: MTEB MassiveIntentClassification (sl)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 56.51647612642906
    - type: f1
      value: 54.33154780100043
    task:
      type: Classification
  - dataset:
      config: sq
      name: MTEB MassiveIntentClassification (sq)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 57.985877605917956
    - type: f1
      value: 54.46187524463802
    task:
      type: Classification
  - dataset:
      config: sv
      name: MTEB MassiveIntentClassification (sv)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 65.03026227303296
    - type: f1
      value: 62.34377392877748
    task:
      type: Classification
  - dataset:
      config: sw
      name: MTEB MassiveIntentClassification (sw)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 53.567585743106925
    - type: f1
      value: 50.73770655983206
    task:
      type: Classification
  - dataset:
      config: ta
      name: MTEB MassiveIntentClassification (ta)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 57.2595830531271
    - type: f1
      value: 53.657327291708626
    task:
      type: Classification
  - dataset:
      config: te
      name: MTEB MassiveIntentClassification (te)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 57.82784129119032
    - type: f1
      value: 54.82518072665301
    task:
      type: Classification
  - dataset:
      config: th
      name: MTEB MassiveIntentClassification (th)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 64.06859448554137
    - type: f1
      value: 63.00185280500495
    task:
      type: Classification
  - dataset:
      config: tl
      name: MTEB MassiveIntentClassification (tl)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 58.91055817081371
    - type: f1
      value: 55.54116301224262
    task:
      type: Classification
  - dataset:
      config: tr
      name: MTEB MassiveIntentClassification (tr)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 63.54404841963686
    - type: f1
      value: 59.57650946030184
    task:
      type: Classification
  - dataset:
      config: ur
      name: MTEB MassiveIntentClassification (ur)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 59.27706792199059
    - type: f1
      value: 56.50010066083435
    task:
      type: Classification
  - dataset:
      config: vi
      name: MTEB MassiveIntentClassification (vi)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 64.0719569603228
    - type: f1
      value: 61.817075925647956
    task:
      type: Classification
  - dataset:
      config: zh-CN
      name: MTEB MassiveIntentClassification (zh-CN)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 68.23806321452591
    - type: f1
      value: 65.24917026029749
    task:
      type: Classification
  - dataset:
      config: zh-TW
      name: MTEB MassiveIntentClassification (zh-TW)
      revision: 31efe3c427b0bae9c22cbb560b8f15491cc6bed7
      split: test
      type: mteb/amazon_massive_intent
    metrics:
    - type: accuracy
      value: 62.53530598520511
    - type: f1
      value: 61.71131132295768
    task:
      type: Classification
  - dataset:
      config: af
      name: MTEB MassiveScenarioClassification (af)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 63.04303967720243
    - type: f1
      value: 60.3950085685985
    task:
      type: Classification
  - dataset:
      config: am
      name: MTEB MassiveScenarioClassification (am)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 56.83591123066578
    - type: f1
      value: 54.95059828830849
    task:
      type: Classification
  - dataset:
      config: ar
      name: MTEB MassiveScenarioClassification (ar)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 59.62340282447881
    - type: f1
      value: 59.525159996498225
    task:
      type: Classification
  - dataset:
      config: az
      name: MTEB MassiveScenarioClassification (az)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 60.85406859448555
    - type: f1
      value: 59.129299095681276
    task:
      type: Classification
  - dataset:
      config: bn
      name: MTEB MassiveScenarioClassification (bn)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 62.76731674512441
    - type: f1
      value: 61.159560612627715
    task:
      type: Classification
  - dataset:
      config: cy
      name: MTEB MassiveScenarioClassification (cy)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 50.181573638197705
    - type: f1
      value: 46.98422176289957
    task:
      type: Classification
  - dataset:
      config: da
      name: MTEB MassiveScenarioClassification (da)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 68.92737054472092
    - type: f1
      value: 67.69135611952979
    task:
      type: Classification
  - dataset:
      config: de
      name: MTEB MassiveScenarioClassification (de)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 69.18964357767318
    - type: f1
      value: 68.46106138186214
    task:
      type: Classification
  - dataset:
      config: el
      name: MTEB MassiveScenarioClassification (el)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 67.0712844653665
    - type: f1
      value: 66.75545422473901
    task:
      type: Classification
  - dataset:
      config: en
      name: MTEB MassiveScenarioClassification (en)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 74.4754539340955
    - type: f1
      value: 74.38427146553252
    task:
      type: Classification
  - dataset:
      config: es
      name: MTEB MassiveScenarioClassification (es)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 69.82515131136518
    - type: f1
      value: 69.63516462173847
    task:
      type: Classification
  - dataset:
      config: fa
      name: MTEB MassiveScenarioClassification (fa)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 68.70880968392737
    - type: f1
      value: 67.45420662567926
    task:
      type: Classification
  - dataset:
      config: fi
      name: MTEB MassiveScenarioClassification (fi)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 65.95494283792871
    - type: f1
      value: 65.06191009049222
    task:
      type: Classification
  - dataset:
      config: fr
      name: MTEB MassiveScenarioClassification (fr)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 68.75924680564896
    - type: f1
      value: 68.30833379585945
    task:
      type: Classification
  - dataset:
      config: he
      name: MTEB MassiveScenarioClassification (he)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 63.806321452589096
    - type: f1
      value: 63.273048243765054
    task:
      type: Classification
  - dataset:
      config: hi
      name: MTEB MassiveScenarioClassification (hi)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 67.68997982515133
    - type: f1
      value: 66.54703855381324
    task:
      type: Classification
  - dataset:
      config: hu
      name: MTEB MassiveScenarioClassification (hu)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 66.46940147948891
    - type: f1
      value: 65.91017343463396
    task:
      type: Classification
  - dataset:
      config: hy
      name: MTEB MassiveScenarioClassification (hy)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 59.49899125756556
    - type: f1
      value: 57.90333469917769
    task:
      type: Classification
  - dataset:
      config: id
      name: MTEB MassiveScenarioClassification (id)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 67.9219905850706
    - type: f1
      value: 67.23169403762938
    task:
      type: Classification
  - dataset:
      config: is
      name: MTEB MassiveScenarioClassification (is)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 56.486213853396094
    - type: f1
      value: 54.85282355583758
    task:
      type: Classification
  - dataset:
      config: it
      name: MTEB MassiveScenarioClassification (it)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 69.04169468728985
    - type: f1
      value: 68.83833333320462
    task:
      type: Classification
  - dataset:
      config: ja
      name: MTEB MassiveScenarioClassification (ja)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 73.88702084734365
    - type: f1
      value: 74.04474735232299
    task:
      type: Classification
  - dataset:
      config: jv
      name: MTEB MassiveScenarioClassification (jv)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 56.63416274377943
    - type: f1
      value: 55.11332211687954
    task:
      type: Classification
  - dataset:
      config: ka
      name: MTEB MassiveScenarioClassification (ka)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 52.23604572965702
    - type: f1
      value: 50.86529813991055
    task:
      type: Classification
  - dataset:
      config: km
      name: MTEB MassiveScenarioClassification (km)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 46.62407531943511
    - type: f1
      value: 43.63485467164535
    task:
      type: Classification
  - dataset:
      config: kn
      name: MTEB MassiveScenarioClassification (kn)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 59.15601882985878
    - type: f1
      value: 57.522837510959924
    task:
      type: Classification
  - dataset:
      config: ko
      name: MTEB MassiveScenarioClassification (ko)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 69.84532616005382
    - type: f1
      value: 69.60021127179697
    task:
      type: Classification
  - dataset:
      config: lv
      name: MTEB MassiveScenarioClassification (lv)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 56.65770006724949
    - type: f1
      value: 55.84219135523227
    task:
      type: Classification
  - dataset:
      config: ml
      name: MTEB MassiveScenarioClassification (ml)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 66.53665097511768
    - type: f1
      value: 65.09087787792639
    task:
      type: Classification
  - dataset:
      config: mn
      name: MTEB MassiveScenarioClassification (mn)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 59.31405514458642
    - type: f1
      value: 58.06135303831491
    task:
      type: Classification
  - dataset:
      config: ms
      name: MTEB MassiveScenarioClassification (ms)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 64.88231338264964
    - type: f1
      value: 62.751099407787926
    task:
      type: Classification
  - dataset:
      config: my
      name: MTEB MassiveScenarioClassification (my)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 58.86012104909213
    - type: f1
      value: 56.29118323058282
    task:
      type: Classification
  - dataset:
      config: nb
      name: MTEB MassiveScenarioClassification (nb)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 67.37390719569602
    - type: f1
      value: 66.27922244885102
    task:
      type: Classification
  - dataset:
      config: nl
      name: MTEB MassiveScenarioClassification (nl)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 70.8675184936113
    - type: f1
      value: 70.22146529932019
    task:
      type: Classification
  - dataset:
      config: pl
      name: MTEB MassiveScenarioClassification (pl)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 68.2212508406187
    - type: f1
      value: 67.77454802056282
    task:
      type: Classification
  - dataset:
      config: pt
      name: MTEB MassiveScenarioClassification (pt)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 68.18090114324143
    - type: f1
      value: 68.03737625431621
    task:
      type: Classification
  - dataset:
      config: ro
      name: MTEB MassiveScenarioClassification (ro)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 64.65030262273034
    - type: f1
      value: 63.792945486912856
    task:
      type: Classification
  - dataset:
      config: ru
      name: MTEB MassiveScenarioClassification (ru)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 63.772749631087066
    - type: f1
      value: 63.4539101720024
    - type: f1_weighted
      value: 62.778603897469566
    - type: main_score
      value: 63.772749631087066
    task:
      type: Classification
  - dataset:
      config: sl
      name: MTEB MassiveScenarioClassification (sl)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 60.17821116341627
    - type: f1
      value: 59.3935969827171
    task:
      type: Classification
  - dataset:
      config: sq
      name: MTEB MassiveScenarioClassification (sq)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 62.86146603900471
    - type: f1
      value: 60.133692735032376
    task:
      type: Classification
  - dataset:
      config: sv
      name: MTEB MassiveScenarioClassification (sv)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 70.89441829186282
    - type: f1
      value: 70.03064076194089
    task:
      type: Classification
  - dataset:
      config: sw
      name: MTEB MassiveScenarioClassification (sw)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 58.15063887020847
    - type: f1
      value: 56.23326278499678
    task:
      type: Classification
  - dataset:
      config: ta
      name: MTEB MassiveScenarioClassification (ta)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 59.43846671149966
    - type: f1
      value: 57.70440450281974
    task:
      type: Classification
  - dataset:
      config: te
      name: MTEB MassiveScenarioClassification (te)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 60.8507061197041
    - type: f1
      value: 59.22916396061171
    task:
      type: Classification
  - dataset:
      config: th
      name: MTEB MassiveScenarioClassification (th)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 70.65568258238063
    - type: f1
      value: 69.90736239440633
    task:
      type: Classification
  - dataset:
      config: tl
      name: MTEB MassiveScenarioClassification (tl)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 60.8843308675185
    - type: f1
      value: 59.30332663713599
    task:
      type: Classification
  - dataset:
      config: tr
      name: MTEB MassiveScenarioClassification (tr)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 68.05312710154674
    - type: f1
      value: 67.44024062594775
    task:
      type: Classification
  - dataset:
      config: ur
      name: MTEB MassiveScenarioClassification (ur)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 62.111634162743776
    - type: f1
      value: 60.89083013084519
    task:
      type: Classification
  - dataset:
      config: vi
      name: MTEB MassiveScenarioClassification (vi)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 67.44115669132482
    - type: f1
      value: 67.92227541674552
    task:
      type: Classification
  - dataset:
      config: zh-CN
      name: MTEB MassiveScenarioClassification (zh-CN)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 74.4687289845326
    - type: f1
      value: 74.16376793486025
    task:
      type: Classification
  - dataset:
      config: zh-TW
      name: MTEB MassiveScenarioClassification (zh-TW)
      revision: 7d571f92784cd94a019292a1f45445077d0ef634
      split: test
      type: mteb/amazon_massive_scenario
    metrics:
    - type: accuracy
      value: 68.31876260928043
    - type: f1
      value: 68.5246745215607
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB MedrxivClusteringP2P
      revision: e7a26af6f3ae46b30dde8737f02c07b1505bcc73
      split: test
      type: mteb/medrxiv-clustering-p2p
    metrics:
    - type: v_measure
      value: 30.90431696479766
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB MedrxivClusteringS2S
      revision: 35191c8c0dca72d8ff3efcd72aa802307d469663
      split: test
      type: mteb/medrxiv-clustering-s2s
    metrics:
    - type: v_measure
      value: 27.259158476693774
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB MindSmallReranking
      revision: 3bdac13927fdc888b903db93b2ffdbd90b295a69
      split: test
      type: mteb/mind_small
    metrics:
    - type: map
      value: 30.28445330838555
    - type: mrr
      value: 31.15758529581164
    task:
      type: Reranking
  - dataset:
      config: default
      name: MTEB NFCorpus
      revision: None
      split: test
      type: nfcorpus
    metrics:
    - type: map_at_1
      value: 5.353
    - type: map_at_10
      value: 11.565
    - type: map_at_100
      value: 14.097000000000001
    - type: map_at_1000
      value: 15.354999999999999
    - type: map_at_3
      value: 8.749
    - type: map_at_5
      value: 9.974
    - type: mrr_at_1
      value: 42.105
    - type: mrr_at_10
      value: 50.589
    - type: mrr_at_100
      value: 51.187000000000005
    - type: mrr_at_1000
      value: 51.233
    - type: mrr_at_3
      value: 48.246
    - type: mrr_at_5
      value: 49.546
    - type: ndcg_at_1
      value: 40.402
    - type: ndcg_at_10
      value: 31.009999999999998
    - type: ndcg_at_100
      value: 28.026
    - type: ndcg_at_1000
      value: 36.905
    - type: ndcg_at_3
      value: 35.983
    - type: ndcg_at_5
      value: 33.764
    - type: precision_at_1
      value: 42.105
    - type: precision_at_10
      value: 22.786
    - type: precision_at_100
      value: 6.916
    - type: precision_at_1000
      value: 1.981
    - type: precision_at_3
      value: 33.333
    - type: precision_at_5
      value: 28.731
    - type: recall_at_1
      value: 5.353
    - type: recall_at_10
      value: 15.039
    - type: recall_at_100
      value: 27.348
    - type: recall_at_1000
      value: 59.453
    - type: recall_at_3
      value: 9.792
    - type: recall_at_5
      value: 11.882
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB NQ
      revision: None
      split: test
      type: nq
    metrics:
    - type: map_at_1
      value: 33.852
    - type: map_at_10
      value: 48.924
    - type: map_at_100
      value: 49.854
    - type: map_at_1000
      value: 49.886
    - type: map_at_3
      value: 44.9
    - type: map_at_5
      value: 47.387
    - type: mrr_at_1
      value: 38.035999999999994
    - type: mrr_at_10
      value: 51.644
    - type: mrr_at_100
      value: 52.339
    - type: mrr_at_1000
      value: 52.35999999999999
    - type: mrr_at_3
      value: 48.421
    - type: mrr_at_5
      value: 50.468999999999994
    - type: ndcg_at_1
      value: 38.007000000000005
    - type: ndcg_at_10
      value: 56.293000000000006
    - type: ndcg_at_100
      value: 60.167
    - type: ndcg_at_1000
      value: 60.916000000000004
    - type: ndcg_at_3
      value: 48.903999999999996
    - type: ndcg_at_5
      value: 52.978
    - type: precision_at_1
      value: 38.007000000000005
    - type: precision_at_10
      value: 9.041
    - type: precision_at_100
      value: 1.1199999999999999
    - type: precision_at_1000
      value: 0.11900000000000001
    - type: precision_at_3
      value: 22.084
    - type: precision_at_5
      value: 15.608
    - type: recall_at_1
      value: 33.852
    - type: recall_at_10
      value: 75.893
    - type: recall_at_100
      value: 92.589
    - type: recall_at_1000
      value: 98.153
    - type: recall_at_3
      value: 56.969
    - type: recall_at_5
      value: 66.283
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB QuoraRetrieval
      revision: None
      split: test
      type: quora
    metrics:
    - type: map_at_1
      value: 69.174
    - type: map_at_10
      value: 82.891
    - type: map_at_100
      value: 83.545
    - type: map_at_1000
      value: 83.56700000000001
    - type: map_at_3
      value: 79.944
    - type: map_at_5
      value: 81.812
    - type: mrr_at_1
      value: 79.67999999999999
    - type: mrr_at_10
      value: 86.279
    - type: mrr_at_100
      value: 86.39
    - type: mrr_at_1000
      value: 86.392
    - type: mrr_at_3
      value: 85.21
    - type: mrr_at_5
      value: 85.92999999999999
    - type: ndcg_at_1
      value: 79.69000000000001
    - type: ndcg_at_10
      value: 86.929
    - type: ndcg_at_100
      value: 88.266
    - type: ndcg_at_1000
      value: 88.428
    - type: ndcg_at_3
      value: 83.899
    - type: ndcg_at_5
      value: 85.56700000000001
    - type: precision_at_1
      value: 79.69000000000001
    - type: precision_at_10
      value: 13.161000000000001
    - type: precision_at_100
      value: 1.513
    - type: precision_at_1000
      value: 0.156
    - type: precision_at_3
      value: 36.603
    - type: precision_at_5
      value: 24.138
    - type: recall_at_1
      value: 69.174
    - type: recall_at_10
      value: 94.529
    - type: recall_at_100
      value: 99.15
    - type: recall_at_1000
      value: 99.925
    - type: recall_at_3
      value: 85.86200000000001
    - type: recall_at_5
      value: 90.501
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB RedditClustering
      revision: 24640382cdbf8abc73003fb0fa6d111a705499eb
      split: test
      type: mteb/reddit-clustering
    metrics:
    - type: v_measure
      value: 39.13064340585255
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB RedditClusteringP2P
      revision: 282350215ef01743dc01b456c7f5241fa8937f16
      split: test
      type: mteb/reddit-clustering-p2p
    metrics:
    - type: v_measure
      value: 58.97884249325877
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB SCIDOCS
      revision: None
      split: test
      type: scidocs
    metrics:
    - type: map_at_1
      value: 3.4680000000000004
    - type: map_at_10
      value: 7.865
    - type: map_at_100
      value: 9.332
    - type: map_at_1000
      value: 9.587
    - type: map_at_3
      value: 5.800000000000001
    - type: map_at_5
      value: 6.8790000000000004
    - type: mrr_at_1
      value: 17.0
    - type: mrr_at_10
      value: 25.629
    - type: mrr_at_100
      value: 26.806
    - type: mrr_at_1000
      value: 26.889000000000003
    - type: mrr_at_3
      value: 22.8
    - type: mrr_at_5
      value: 24.26
    - type: ndcg_at_1
      value: 17.0
    - type: ndcg_at_10
      value: 13.895
    - type: ndcg_at_100
      value: 20.491999999999997
    - type: ndcg_at_1000
      value: 25.759999999999998
    - type: ndcg_at_3
      value: 13.347999999999999
    - type: ndcg_at_5
      value: 11.61
    - type: precision_at_1
      value: 17.0
    - type: precision_at_10
      value: 7.090000000000001
    - type: precision_at_100
      value: 1.669
    - type: precision_at_1000
      value: 0.294
    - type: precision_at_3
      value: 12.3
    - type: precision_at_5
      value: 10.02
    - type: recall_at_1
      value: 3.4680000000000004
    - type: recall_at_10
      value: 14.363000000000001
    - type: recall_at_100
      value: 33.875
    - type: recall_at_1000
      value: 59.711999999999996
    - type: recall_at_3
      value: 7.483
    - type: recall_at_5
      value: 10.173
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB SICK-R
      revision: a6ea5a8cab320b040a23452cc28066d9beae2cee
      split: test
      type: mteb/sickr-sts
    metrics:
    - type: cos_sim_pearson
      value: 83.04084311714061
    - type: cos_sim_spearman
      value: 77.51342467443078
    - type: euclidean_pearson
      value: 80.0321166028479
    - type: euclidean_spearman
      value: 77.29249114733226
    - type: manhattan_pearson
      value: 80.03105964262431
    - type: manhattan_spearman
      value: 77.22373689514794
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB STS12
      revision: a0d554a64d88156834ff5ae9920b964011b16384
      split: test
      type: mteb/sts12-sts
    metrics:
    - type: cos_sim_pearson
      value: 84.1680158034387
    - type: cos_sim_spearman
      value: 76.55983344071117
    - type: euclidean_pearson
      value: 79.75266678300143
    - type: euclidean_spearman
      value: 75.34516823467025
    - type: manhattan_pearson
      value: 79.75959151517357
    - type: manhattan_spearman
      value: 75.42330344141912
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB STS13
      revision: 7e90230a92c190f1bf69ae9002b8cea547a64cca
      split: test
      type: mteb/sts13-sts
    metrics:
    - type: cos_sim_pearson
      value: 76.48898993209346
    - type: cos_sim_spearman
      value: 76.96954120323366
    - type: euclidean_pearson
      value: 76.94139109279668
    - type: euclidean_spearman
      value: 76.85860283201711
    - type: manhattan_pearson
      value: 76.6944095091912
    - type: manhattan_spearman
      value: 76.61096912972553
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB STS14
      revision: 6031580fec1f6af667f0bd2da0a551cf4f0b2375
      split: test
      type: mteb/sts14-sts
    metrics:
    - type: cos_sim_pearson
      value: 77.85082366246944
    - type: cos_sim_spearman
      value: 75.52053350101731
    - type: euclidean_pearson
      value: 77.1165845070926
    - type: euclidean_spearman
      value: 75.31216065884388
    - type: manhattan_pearson
      value: 77.06193941833494
    - type: manhattan_spearman
      value: 75.31003701700112
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB STS15
      revision: ae752c7c21bf194d8b67fd573edf7ae58183cbe3
      split: test
      type: mteb/sts15-sts
    metrics:
    - type: cos_sim_pearson
      value: 86.36305246526497
    - type: cos_sim_spearman
      value: 87.11704613927415
    - type: euclidean_pearson
      value: 86.04199125810939
    - type: euclidean_spearman
      value: 86.51117572414263
    - type: manhattan_pearson
      value: 86.0805106816633
    - type: manhattan_spearman
      value: 86.52798366512229
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB STS16
      revision: 4d8694f8f0e0100860b497b999b3dbed754a0513
      split: test
      type: mteb/sts16-sts
    metrics:
    - type: cos_sim_pearson
      value: 82.18536255599724
    - type: cos_sim_spearman
      value: 83.63377151025418
    - type: euclidean_pearson
      value: 83.24657467993141
    - type: euclidean_spearman
      value: 84.02751481993825
    - type: manhattan_pearson
      value: 83.11941806582371
    - type: manhattan_spearman
      value: 83.84251281019304
    task:
      type: STS
  - dataset:
      config: ko-ko
      name: MTEB STS17 (ko-ko)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 78.95816528475514
    - type: cos_sim_spearman
      value: 78.86607380120462
    - type: euclidean_pearson
      value: 78.51268699230545
    - type: euclidean_spearman
      value: 79.11649316502229
    - type: manhattan_pearson
      value: 78.32367302808157
    - type: manhattan_spearman
      value: 78.90277699624637
    task:
      type: STS
  - dataset:
      config: ar-ar
      name: MTEB STS17 (ar-ar)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 72.89126914997624
    - type: cos_sim_spearman
      value: 73.0296921832678
    - type: euclidean_pearson
      value: 71.50385903677738
    - type: euclidean_spearman
      value: 73.13368899716289
    - type: manhattan_pearson
      value: 71.47421463379519
    - type: manhattan_spearman
      value: 73.03383242946575
    task:
      type: STS
  - dataset:
      config: en-ar
      name: MTEB STS17 (en-ar)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 59.22923684492637
    - type: cos_sim_spearman
      value: 57.41013211368396
    - type: euclidean_pearson
      value: 61.21107388080905
    - type: euclidean_spearman
      value: 60.07620768697254
    - type: manhattan_pearson
      value: 59.60157142786555
    - type: manhattan_spearman
      value: 59.14069604103739
    task:
      type: STS
  - dataset:
      config: en-de
      name: MTEB STS17 (en-de)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 76.24345978774299
    - type: cos_sim_spearman
      value: 77.24225743830719
    - type: euclidean_pearson
      value: 76.66226095469165
    - type: euclidean_spearman
      value: 77.60708820493146
    - type: manhattan_pearson
      value: 76.05303324760429
    - type: manhattan_spearman
      value: 76.96353149912348
    task:
      type: STS
  - dataset:
      config: en-en
      name: MTEB STS17 (en-en)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 85.50879160160852
    - type: cos_sim_spearman
      value: 86.43594662965224
    - type: euclidean_pearson
      value: 86.06846012826577
    - type: euclidean_spearman
      value: 86.02041395794136
    - type: manhattan_pearson
      value: 86.10916255616904
    - type: manhattan_spearman
      value: 86.07346068198953
    task:
      type: STS
  - dataset:
      config: en-tr
      name: MTEB STS17 (en-tr)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 58.39803698977196
    - type: cos_sim_spearman
      value: 55.96910950423142
    - type: euclidean_pearson
      value: 58.17941175613059
    - type: euclidean_spearman
      value: 55.03019330522745
    - type: manhattan_pearson
      value: 57.333358138183286
    - type: manhattan_spearman
      value: 54.04614023149965
    task:
      type: STS
  - dataset:
      config: es-en
      name: MTEB STS17 (es-en)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 70.98304089637197
    - type: cos_sim_spearman
      value: 72.44071656215888
    - type: euclidean_pearson
      value: 72.19224359033983
    - type: euclidean_spearman
      value: 73.89871188913025
    - type: manhattan_pearson
      value: 71.21098311547406
    - type: manhattan_spearman
      value: 72.93405764824821
    task:
      type: STS
  - dataset:
      config: es-es
      name: MTEB STS17 (es-es)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 85.99792397466308
    - type: cos_sim_spearman
      value: 84.83824377879495
    - type: euclidean_pearson
      value: 85.70043288694438
    - type: euclidean_spearman
      value: 84.70627558703686
    - type: manhattan_pearson
      value: 85.89570850150801
    - type: manhattan_spearman
      value: 84.95806105313007
    task:
      type: STS
  - dataset:
      config: fr-en
      name: MTEB STS17 (fr-en)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 72.21850322994712
    - type: cos_sim_spearman
      value: 72.28669398117248
    - type: euclidean_pearson
      value: 73.40082510412948
    - type: euclidean_spearman
      value: 73.0326539281865
    - type: manhattan_pearson
      value: 71.8659633964841
    - type: manhattan_spearman
      value: 71.57817425823303
    task:
      type: STS
  - dataset:
      config: it-en
      name: MTEB STS17 (it-en)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 75.80921368595645
    - type: cos_sim_spearman
      value: 77.33209091229315
    - type: euclidean_pearson
      value: 76.53159540154829
    - type: euclidean_spearman
      value: 78.17960842810093
    - type: manhattan_pearson
      value: 76.13530186637601
    - type: manhattan_spearman
      value: 78.00701437666875
    task:
      type: STS
  - dataset:
      config: nl-en
      name: MTEB STS17 (nl-en)
      revision: af5e6fb845001ecf41f4c1e033ce921939a2a68d
      split: test
      type: mteb/sts17-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 74.74980608267349
    - type: cos_sim_spearman
      value: 75.37597374318821
    - type: euclidean_pearson
      value: 74.90506081911661
    - type: euclidean_spearman
      value: 75.30151613124521
    - type: manhattan_pearson
      value: 74.62642745918002
    - type: manhattan_spearman
      value: 75.18619716592303
    task:
      type: STS
  - dataset:
      config: en
      name: MTEB STS22 (en)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 59.632662289205584
    - type: cos_sim_spearman
      value: 60.938543391610914
    - type: euclidean_pearson
      value: 62.113200529767056
    - type: euclidean_spearman
      value: 61.410312633261164
    - type: manhattan_pearson
      value: 61.75494698945686
    - type: manhattan_spearman
      value: 60.92726195322362
    task:
      type: STS
  - dataset:
      config: de
      name: MTEB STS22 (de)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 45.283470551557244
    - type: cos_sim_spearman
      value: 53.44833015864201
    - type: euclidean_pearson
      value: 41.17892011120893
    - type: euclidean_spearman
      value: 53.81441383126767
    - type: manhattan_pearson
      value: 41.17482200420659
    - type: manhattan_spearman
      value: 53.82180269276363
    task:
      type: STS
  - dataset:
      config: es
      name: MTEB STS22 (es)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 60.5069165306236
    - type: cos_sim_spearman
      value: 66.87803259033826
    - type: euclidean_pearson
      value: 63.5428979418236
    - type: euclidean_spearman
      value: 66.9293576586897
    - type: manhattan_pearson
      value: 63.59789526178922
    - type: manhattan_spearman
      value: 66.86555009875066
    task:
      type: STS
  - dataset:
      config: pl
      name: MTEB STS22 (pl)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 28.23026196280264
    - type: cos_sim_spearman
      value: 35.79397812652861
    - type: euclidean_pearson
      value: 17.828102102767353
    - type: euclidean_spearman
      value: 35.721501145568894
    - type: manhattan_pearson
      value: 17.77134274219677
    - type: manhattan_spearman
      value: 35.98107902846267
    task:
      type: STS
  - dataset:
      config: tr
      name: MTEB STS22 (tr)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 56.51946541393812
    - type: cos_sim_spearman
      value: 63.714686006214485
    - type: euclidean_pearson
      value: 58.32104651305898
    - type: euclidean_spearman
      value: 62.237110895702216
    - type: manhattan_pearson
      value: 58.579416468759185
    - type: manhattan_spearman
      value: 62.459738981727
    task:
      type: STS
  - dataset:
      config: ar
      name: MTEB STS22 (ar)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 48.76009839569795
    - type: cos_sim_spearman
      value: 56.65188431953149
    - type: euclidean_pearson
      value: 50.997682160915595
    - type: euclidean_spearman
      value: 55.99910008818135
    - type: manhattan_pearson
      value: 50.76220659606342
    - type: manhattan_spearman
      value: 55.517347595391456
    task:
      type: STS
  - dataset:
      config: ru
      name: MTEB STS22 (ru)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cosine_pearson
      value: 50.724322379215934
    - type: cosine_spearman
      value: 59.90449732164651
    - type: euclidean_pearson
      value: 50.227545226784024
    - type: euclidean_spearman
      value: 59.898906527601085
    - type: main_score
      value: 59.90449732164651
    - type: manhattan_pearson
      value: 50.21762139819405
    - type: manhattan_spearman
      value: 59.761039813759
    - type: pearson
      value: 50.724322379215934
    - type: spearman
      value: 59.90449732164651
    task:
      type: STS
  - dataset:
      config: zh
      name: MTEB STS22 (zh)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 54.717524559088005
    - type: cos_sim_spearman
      value: 66.83570886252286
    - type: euclidean_pearson
      value: 58.41338625505467
    - type: euclidean_spearman
      value: 66.68991427704938
    - type: manhattan_pearson
      value: 58.78638572916807
    - type: manhattan_spearman
      value: 66.58684161046335
    task:
      type: STS
  - dataset:
      config: fr
      name: MTEB STS22 (fr)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 73.2962042954962
    - type: cos_sim_spearman
      value: 76.58255504852025
    - type: euclidean_pearson
      value: 75.70983192778257
    - type: euclidean_spearman
      value: 77.4547684870542
    - type: manhattan_pearson
      value: 75.75565853870485
    - type: manhattan_spearman
      value: 76.90208974949428
    task:
      type: STS
  - dataset:
      config: de-en
      name: MTEB STS22 (de-en)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 54.47396266924846
    - type: cos_sim_spearman
      value: 56.492267162048606
    - type: euclidean_pearson
      value: 55.998505203070195
    - type: euclidean_spearman
      value: 56.46447012960222
    - type: manhattan_pearson
      value: 54.873172394430995
    - type: manhattan_spearman
      value: 56.58111534551218
    task:
      type: STS
  - dataset:
      config: es-en
      name: MTEB STS22 (es-en)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 69.87177267688686
    - type: cos_sim_spearman
      value: 74.57160943395763
    - type: euclidean_pearson
      value: 70.88330406826788
    - type: euclidean_spearman
      value: 74.29767636038422
    - type: manhattan_pearson
      value: 71.38245248369536
    - type: manhattan_spearman
      value: 74.53102232732175
    task:
      type: STS
  - dataset:
      config: it
      name: MTEB STS22 (it)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 72.80225656959544
    - type: cos_sim_spearman
      value: 76.52646173725735
    - type: euclidean_pearson
      value: 73.95710720200799
    - type: euclidean_spearman
      value: 76.54040031984111
    - type: manhattan_pearson
      value: 73.89679971946774
    - type: manhattan_spearman
      value: 76.60886958161574
    task:
      type: STS
  - dataset:
      config: pl-en
      name: MTEB STS22 (pl-en)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 70.70844249898789
    - type: cos_sim_spearman
      value: 72.68571783670241
    - type: euclidean_pearson
      value: 72.38800772441031
    - type: euclidean_spearman
      value: 72.86804422703312
    - type: manhattan_pearson
      value: 71.29840508203515
    - type: manhattan_spearman
      value: 71.86264441749513
    task:
      type: STS
  - dataset:
      config: zh-en
      name: MTEB STS22 (zh-en)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 58.647478923935694
    - type: cos_sim_spearman
      value: 63.74453623540931
    - type: euclidean_pearson
      value: 59.60138032437505
    - type: euclidean_spearman
      value: 63.947930832166065
    - type: manhattan_pearson
      value: 58.59735509491861
    - type: manhattan_spearman
      value: 62.082503844627404
    task:
      type: STS
  - dataset:
      config: es-it
      name: MTEB STS22 (es-it)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 65.8722516867162
    - type: cos_sim_spearman
      value: 71.81208592523012
    - type: euclidean_pearson
      value: 67.95315252165956
    - type: euclidean_spearman
      value: 73.00749822046009
    - type: manhattan_pearson
      value: 68.07884688638924
    - type: manhattan_spearman
      value: 72.34210325803069
    task:
      type: STS
  - dataset:
      config: de-fr
      name: MTEB STS22 (de-fr)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 54.5405814240949
    - type: cos_sim_spearman
      value: 60.56838649023775
    - type: euclidean_pearson
      value: 53.011731611314104
    - type: euclidean_spearman
      value: 58.533194841668426
    - type: manhattan_pearson
      value: 53.623067729338494
    - type: manhattan_spearman
      value: 58.018756154446926
    task:
      type: STS
  - dataset:
      config: de-pl
      name: MTEB STS22 (de-pl)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 13.611046866216112
    - type: cos_sim_spearman
      value: 28.238192909158492
    - type: euclidean_pearson
      value: 22.16189199885129
    - type: euclidean_spearman
      value: 35.012895679076564
    - type: manhattan_pearson
      value: 21.969771178698387
    - type: manhattan_spearman
      value: 32.456985088607475
    task:
      type: STS
  - dataset:
      config: fr-pl
      name: MTEB STS22 (fr-pl)
      revision: 6d1ba47164174a496b7fa5d3569dae26a6813b80
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cos_sim_pearson
      value: 74.58077407011655
    - type: cos_sim_spearman
      value: 84.51542547285167
    - type: euclidean_pearson
      value: 74.64613843596234
    - type: euclidean_spearman
      value: 84.51542547285167
    - type: manhattan_pearson
      value: 75.15335973101396
    - type: manhattan_spearman
      value: 84.51542547285167
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB STSBenchmark
      revision: b0fddb56ed78048fa8b90373c8a3cfc37b684831
      split: test
      type: mteb/stsbenchmark-sts
    metrics:
    - type: cos_sim_pearson
      value: 82.0739825531578
    - type: cos_sim_spearman
      value: 84.01057479311115
    - type: euclidean_pearson
      value: 83.85453227433344
    - type: euclidean_spearman
      value: 84.01630226898655
    - type: manhattan_pearson
      value: 83.75323603028978
    - type: manhattan_spearman
      value: 83.89677983727685
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB SciDocsRR
      revision: d3c5e1fc0b855ab6097bf1cda04dd73947d7caab
      split: test
      type: mteb/scidocs-reranking
    metrics:
    - type: map
      value: 78.12945623123957
    - type: mrr
      value: 93.87738713719106
    task:
      type: Reranking
  - dataset:
      config: default
      name: MTEB SciFact
      revision: None
      split: test
      type: scifact
    metrics:
    - type: map_at_1
      value: 52.983000000000004
    - type: map_at_10
      value: 62.946000000000005
    - type: map_at_100
      value: 63.514
    - type: map_at_1000
      value: 63.554
    - type: map_at_3
      value: 60.183
    - type: map_at_5
      value: 61.672000000000004
    - type: mrr_at_1
      value: 55.667
    - type: mrr_at_10
      value: 64.522
    - type: mrr_at_100
      value: 64.957
    - type: mrr_at_1000
      value: 64.995
    - type: mrr_at_3
      value: 62.388999999999996
    - type: mrr_at_5
      value: 63.639
    - type: ndcg_at_1
      value: 55.667
    - type: ndcg_at_10
      value: 67.704
    - type: ndcg_at_100
      value: 70.299
    - type: ndcg_at_1000
      value: 71.241
    - type: ndcg_at_3
      value: 62.866
    - type: ndcg_at_5
      value: 65.16999999999999
    - type: precision_at_1
      value: 55.667
    - type: precision_at_10
      value: 9.033
    - type: precision_at_100
      value: 1.053
    - type: precision_at_1000
      value: 0.11299999999999999
    - type: precision_at_3
      value: 24.444
    - type: precision_at_5
      value: 16.133
    - type: recall_at_1
      value: 52.983000000000004
    - type: recall_at_10
      value: 80.656
    - type: recall_at_100
      value: 92.5
    - type: recall_at_1000
      value: 99.667
    - type: recall_at_3
      value: 67.744
    - type: recall_at_5
      value: 73.433
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB SprintDuplicateQuestions
      revision: d66bd1f72af766a5cc4b0ca5e00c162f89e8cc46
      split: test
      type: mteb/sprintduplicatequestions-pairclassification
    metrics:
    - type: cos_sim_accuracy
      value: 99.72772277227723
    - type: cos_sim_ap
      value: 92.17845897992215
    - type: cos_sim_f1
      value: 85.9746835443038
    - type: cos_sim_precision
      value: 87.07692307692308
    - type: cos_sim_recall
      value: 84.89999999999999
    - type: dot_accuracy
      value: 99.3039603960396
    - type: dot_ap
      value: 60.70244020124878
    - type: dot_f1
      value: 59.92742353551063
    - type: dot_precision
      value: 62.21743810548978
    - type: dot_recall
      value: 57.8
    - type: euclidean_accuracy
      value: 99.71683168316832
    - type: euclidean_ap
      value: 91.53997039964659
    - type: euclidean_f1
      value: 84.88372093023257
    - type: euclidean_precision
      value: 90.02242152466367
    - type: euclidean_recall
      value: 80.30000000000001
    - type: manhattan_accuracy
      value: 99.72376237623763
    - type: manhattan_ap
      value: 91.80756777790289
    - type: manhattan_f1
      value: 85.48468106479157
    - type: manhattan_precision
      value: 85.8728557013118
    - type: manhattan_recall
      value: 85.1
    - type: max_accuracy
      value: 99.72772277227723
    - type: max_ap
      value: 92.17845897992215
    - type: max_f1
      value: 85.9746835443038
    task:
      type: PairClassification
  - dataset:
      config: default
      name: MTEB StackExchangeClustering
      revision: 6cbc1f7b2bc0622f2e39d2c77fa502909748c259
      split: test
      type: mteb/stackexchange-clustering
    metrics:
    - type: v_measure
      value: 53.52464042600003
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB StackExchangeClusteringP2P
      revision: 815ca46b2622cec33ccafc3735d572c266efdb44
      split: test
      type: mteb/stackexchange-clustering-p2p
    metrics:
    - type: v_measure
      value: 32.071631948736
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB StackOverflowDupQuestions
      revision: e185fbe320c72810689fc5848eb6114e1ef5ec69
      split: test
      type: mteb/stackoverflowdupquestions-reranking
    metrics:
    - type: map
      value: 49.19552407604654
    - type: mrr
      value: 49.95269130379425
    task:
      type: Reranking
  - dataset:
      config: default
      name: MTEB SummEval
      revision: cda12ad7615edc362dbf25a00fdd61d3b1eaf93c
      split: test
      type: mteb/summeval
    metrics:
    - type: cos_sim_pearson
      value: 29.345293033095427
    - type: cos_sim_spearman
      value: 29.976931423258403
    - type: dot_pearson
      value: 27.047078008958408
    - type: dot_spearman
      value: 27.75894368380218
    task:
      type: Summarization
  - dataset:
      config: default
      name: MTEB TRECCOVID
      revision: None
      split: test
      type: trec-covid
    metrics:
    - type: map_at_1
      value: 0.22
    - type: map_at_10
      value: 1.706
    - type: map_at_100
      value: 9.634
    - type: map_at_1000
      value: 23.665
    - type: map_at_3
      value: 0.5950000000000001
    - type: map_at_5
      value: 0.95
    - type: mrr_at_1
      value: 86.0
    - type: mrr_at_10
      value: 91.8
    - type: mrr_at_100
      value: 91.8
    - type: mrr_at_1000
      value: 91.8
    - type: mrr_at_3
      value: 91.0
    - type: mrr_at_5
      value: 91.8
    - type: ndcg_at_1
      value: 80.0
    - type: ndcg_at_10
      value: 72.573
    - type: ndcg_at_100
      value: 53.954
    - type: ndcg_at_1000
      value: 47.760999999999996
    - type: ndcg_at_3
      value: 76.173
    - type: ndcg_at_5
      value: 75.264
    - type: precision_at_1
      value: 86.0
    - type: precision_at_10
      value: 76.4
    - type: precision_at_100
      value: 55.50000000000001
    - type: precision_at_1000
      value: 21.802
    - type: precision_at_3
      value: 81.333
    - type: precision_at_5
      value: 80.4
    - type: recall_at_1
      value: 0.22
    - type: recall_at_10
      value: 1.925
    - type: recall_at_100
      value: 12.762
    - type: recall_at_1000
      value: 44.946000000000005
    - type: recall_at_3
      value: 0.634
    - type: recall_at_5
      value: 1.051
    task:
      type: Retrieval
  - dataset:
      config: sqi-eng
      name: MTEB Tatoeba (sqi-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 91.0
    - type: f1
      value: 88.55666666666666
    - type: precision
      value: 87.46166666666667
    - type: recall
      value: 91.0
    task:
      type: BitextMining
  - dataset:
      config: fry-eng
      name: MTEB Tatoeba (fry-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 57.22543352601156
    - type: f1
      value: 51.03220478943021
    - type: precision
      value: 48.8150289017341
    - type: recall
      value: 57.22543352601156
    task:
      type: BitextMining
  - dataset:
      config: kur-eng
      name: MTEB Tatoeba (kur-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 46.58536585365854
    - type: f1
      value: 39.66870798578116
    - type: precision
      value: 37.416085946573745
    - type: recall
      value: 46.58536585365854
    task:
      type: BitextMining
  - dataset:
      config: tur-eng
      name: MTEB Tatoeba (tur-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 89.7
    - type: f1
      value: 86.77999999999999
    - type: precision
      value: 85.45333333333332
    - type: recall
      value: 89.7
    task:
      type: BitextMining
  - dataset:
      config: deu-eng
      name: MTEB Tatoeba (deu-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 97.39999999999999
    - type: f1
      value: 96.58333333333331
    - type: precision
      value: 96.2
    - type: recall
      value: 97.39999999999999
    task:
      type: BitextMining
  - dataset:
      config: nld-eng
      name: MTEB Tatoeba (nld-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 92.4
    - type: f1
      value: 90.3
    - type: precision
      value: 89.31666666666668
    - type: recall
      value: 92.4
    task:
      type: BitextMining
  - dataset:
      config: ron-eng
      name: MTEB Tatoeba (ron-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 86.9
    - type: f1
      value: 83.67190476190476
    - type: precision
      value: 82.23333333333332
    - type: recall
      value: 86.9
    task:
      type: BitextMining
  - dataset:
      config: ang-eng
      name: MTEB Tatoeba (ang-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 50.0
    - type: f1
      value: 42.23229092632078
    - type: precision
      value: 39.851634683724235
    - type: recall
      value: 50.0
    task:
      type: BitextMining
  - dataset:
      config: ido-eng
      name: MTEB Tatoeba (ido-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 76.3
    - type: f1
      value: 70.86190476190477
    - type: precision
      value: 68.68777777777777
    - type: recall
      value: 76.3
    task:
      type: BitextMining
  - dataset:
      config: jav-eng
      name: MTEB Tatoeba (jav-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 57.073170731707314
    - type: f1
      value: 50.658958927251604
    - type: precision
      value: 48.26480836236933
    - type: recall
      value: 57.073170731707314
    task:
      type: BitextMining
  - dataset:
      config: isl-eng
      name: MTEB Tatoeba (isl-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 68.2
    - type: f1
      value: 62.156507936507936
    - type: precision
      value: 59.84964285714286
    - type: recall
      value: 68.2
    task:
      type: BitextMining
  - dataset:
      config: slv-eng
      name: MTEB Tatoeba (slv-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 77.52126366950182
    - type: f1
      value: 72.8496210148701
    - type: precision
      value: 70.92171498003819
    - type: recall
      value: 77.52126366950182
    task:
      type: BitextMining
  - dataset:
      config: cym-eng
      name: MTEB Tatoeba (cym-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 70.78260869565217
    - type: f1
      value: 65.32422360248447
    - type: precision
      value: 63.063067367415194
    - type: recall
      value: 70.78260869565217
    task:
      type: BitextMining
  - dataset:
      config: kaz-eng
      name: MTEB Tatoeba (kaz-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 78.43478260869566
    - type: f1
      value: 73.02608695652172
    - type: precision
      value: 70.63768115942028
    - type: recall
      value: 78.43478260869566
    task:
      type: BitextMining
  - dataset:
      config: est-eng
      name: MTEB Tatoeba (est-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 60.9
    - type: f1
      value: 55.309753694581275
    - type: precision
      value: 53.130476190476195
    - type: recall
      value: 60.9
    task:
      type: BitextMining
  - dataset:
      config: heb-eng
      name: MTEB Tatoeba (heb-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 72.89999999999999
    - type: f1
      value: 67.92023809523809
    - type: precision
      value: 65.82595238095237
    - type: recall
      value: 72.89999999999999
    task:
      type: BitextMining
  - dataset:
      config: gla-eng
      name: MTEB Tatoeba (gla-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 46.80337756332931
    - type: f1
      value: 39.42174900558496
    - type: precision
      value: 36.97101116280851
    - type: recall
      value: 46.80337756332931
    task:
      type: BitextMining
  - dataset:
      config: mar-eng
      name: MTEB Tatoeba (mar-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 89.8
    - type: f1
      value: 86.79
    - type: precision
      value: 85.375
    - type: recall
      value: 89.8
    task:
      type: BitextMining
  - dataset:
      config: lat-eng
      name: MTEB Tatoeba (lat-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 47.199999999999996
    - type: f1
      value: 39.95484348984349
    - type: precision
      value: 37.561071428571424
    - type: recall
      value: 47.199999999999996
    task:
      type: BitextMining
  - dataset:
      config: bel-eng
      name: MTEB Tatoeba (bel-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 87.8
    - type: f1
      value: 84.68190476190475
    - type: precision
      value: 83.275
    - type: recall
      value: 87.8
    task:
      type: BitextMining
  - dataset:
      config: pms-eng
      name: MTEB Tatoeba (pms-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 48.76190476190476
    - type: f1
      value: 42.14965986394558
    - type: precision
      value: 39.96743626743626
    - type: recall
      value: 48.76190476190476
    task:
      type: BitextMining
  - dataset:
      config: gle-eng
      name: MTEB Tatoeba (gle-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 66.10000000000001
    - type: f1
      value: 59.58580086580086
    - type: precision
      value: 57.150238095238095
    - type: recall
      value: 66.10000000000001
    task:
      type: BitextMining
  - dataset:
      config: pes-eng
      name: MTEB Tatoeba (pes-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 87.3
    - type: f1
      value: 84.0
    - type: precision
      value: 82.48666666666666
    - type: recall
      value: 87.3
    task:
      type: BitextMining
  - dataset:
      config: nob-eng
      name: MTEB Tatoeba (nob-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 90.4
    - type: f1
      value: 87.79523809523809
    - type: precision
      value: 86.6
    - type: recall
      value: 90.4
    task:
      type: BitextMining
  - dataset:
      config: bul-eng
      name: MTEB Tatoeba (bul-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 87.0
    - type: f1
      value: 83.81
    - type: precision
      value: 82.36666666666666
    - type: recall
      value: 87.0
    task:
      type: BitextMining
  - dataset:
      config: cbk-eng
      name: MTEB Tatoeba (cbk-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 63.9
    - type: f1
      value: 57.76533189033189
    - type: precision
      value: 55.50595238095239
    - type: recall
      value: 63.9
    task:
      type: BitextMining
  - dataset:
      config: hun-eng
      name: MTEB Tatoeba (hun-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 76.1
    - type: f1
      value: 71.83690476190478
    - type: precision
      value: 70.04928571428573
    - type: recall
      value: 76.1
    task:
      type: BitextMining
  - dataset:
      config: uig-eng
      name: MTEB Tatoeba (uig-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 66.3
    - type: f1
      value: 59.32626984126984
    - type: precision
      value: 56.62535714285713
    - type: recall
      value: 66.3
    task:
      type: BitextMining
  - dataset:
      config: rus-eng
      name: MTEB Tatoeba (rus-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 92.10000000000001
    - type: f1
      value: 89.76666666666667
    - type: main_score
      value: 89.76666666666667
    - type: precision
      value: 88.64999999999999
    - type: recall
      value: 92.10000000000001
    task:
      type: BitextMining
  - dataset:
      config: spa-eng
      name: MTEB Tatoeba (spa-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 93.10000000000001
    - type: f1
      value: 91.10000000000001
    - type: precision
      value: 90.16666666666666
    - type: recall
      value: 93.10000000000001
    task:
      type: BitextMining
  - dataset:
      config: hye-eng
      name: MTEB Tatoeba (hye-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 85.71428571428571
    - type: f1
      value: 82.29142600436403
    - type: precision
      value: 80.8076626877166
    - type: recall
      value: 85.71428571428571
    task:
      type: BitextMining
  - dataset:
      config: tel-eng
      name: MTEB Tatoeba (tel-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 88.88888888888889
    - type: f1
      value: 85.7834757834758
    - type: precision
      value: 84.43732193732193
    - type: recall
      value: 88.88888888888889
    task:
      type: BitextMining
  - dataset:
      config: afr-eng
      name: MTEB Tatoeba (afr-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 88.5
    - type: f1
      value: 85.67190476190476
    - type: precision
      value: 84.43333333333332
    - type: recall
      value: 88.5
    task:
      type: BitextMining
  - dataset:
      config: mon-eng
      name: MTEB Tatoeba (mon-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 82.72727272727273
    - type: f1
      value: 78.21969696969695
    - type: precision
      value: 76.18181818181819
    - type: recall
      value: 82.72727272727273
    task:
      type: BitextMining
  - dataset:
      config: arz-eng
      name: MTEB Tatoeba (arz-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 61.0062893081761
    - type: f1
      value: 55.13976240391334
    - type: precision
      value: 52.92112499659669
    - type: recall
      value: 61.0062893081761
    task:
      type: BitextMining
  - dataset:
      config: hrv-eng
      name: MTEB Tatoeba (hrv-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 89.5
    - type: f1
      value: 86.86666666666666
    - type: precision
      value: 85.69166666666668
    - type: recall
      value: 89.5
    task:
      type: BitextMining
  - dataset:
      config: nov-eng
      name: MTEB Tatoeba (nov-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 73.54085603112841
    - type: f1
      value: 68.56031128404669
    - type: precision
      value: 66.53047989623866
    - type: recall
      value: 73.54085603112841
    task:
      type: BitextMining
  - dataset:
      config: gsw-eng
      name: MTEB Tatoeba (gsw-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 43.58974358974359
    - type: f1
      value: 36.45299145299145
    - type: precision
      value: 33.81155881155882
    - type: recall
      value: 43.58974358974359
    task:
      type: BitextMining
  - dataset:
      config: nds-eng
      name: MTEB Tatoeba (nds-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 59.599999999999994
    - type: f1
      value: 53.264689754689755
    - type: precision
      value: 50.869166666666665
    - type: recall
      value: 59.599999999999994
    task:
      type: BitextMining
  - dataset:
      config: ukr-eng
      name: MTEB Tatoeba (ukr-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 85.2
    - type: f1
      value: 81.61666666666665
    - type: precision
      value: 80.02833333333335
    - type: recall
      value: 85.2
    task:
      type: BitextMining
  - dataset:
      config: uzb-eng
      name: MTEB Tatoeba (uzb-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 63.78504672897196
    - type: f1
      value: 58.00029669188548
    - type: precision
      value: 55.815809968847354
    - type: recall
      value: 63.78504672897196
    task:
      type: BitextMining
  - dataset:
      config: lit-eng
      name: MTEB Tatoeba (lit-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 66.5
    - type: f1
      value: 61.518333333333345
    - type: precision
      value: 59.622363699102834
    - type: recall
      value: 66.5
    task:
      type: BitextMining
  - dataset:
      config: ina-eng
      name: MTEB Tatoeba (ina-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 88.6
    - type: f1
      value: 85.60222222222221
    - type: precision
      value: 84.27916666666665
    - type: recall
      value: 88.6
    task:
      type: BitextMining
  - dataset:
      config: lfn-eng
      name: MTEB Tatoeba (lfn-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 58.699999999999996
    - type: f1
      value: 52.732375957375965
    - type: precision
      value: 50.63214035964035
    - type: recall
      value: 58.699999999999996
    task:
      type: BitextMining
  - dataset:
      config: zsm-eng
      name: MTEB Tatoeba (zsm-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 92.10000000000001
    - type: f1
      value: 89.99666666666667
    - type: precision
      value: 89.03333333333333
    - type: recall
      value: 92.10000000000001
    task:
      type: BitextMining
  - dataset:
      config: ita-eng
      name: MTEB Tatoeba (ita-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 90.10000000000001
    - type: f1
      value: 87.55666666666667
    - type: precision
      value: 86.36166666666668
    - type: recall
      value: 90.10000000000001
    task:
      type: BitextMining
  - dataset:
      config: cmn-eng
      name: MTEB Tatoeba (cmn-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 91.4
    - type: f1
      value: 88.89000000000001
    - type: precision
      value: 87.71166666666666
    - type: recall
      value: 91.4
    task:
      type: BitextMining
  - dataset:
      config: lvs-eng
      name: MTEB Tatoeba (lvs-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 65.7
    - type: f1
      value: 60.67427750410509
    - type: precision
      value: 58.71785714285714
    - type: recall
      value: 65.7
    task:
      type: BitextMining
  - dataset:
      config: glg-eng
      name: MTEB Tatoeba (glg-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 85.39999999999999
    - type: f1
      value: 81.93190476190475
    - type: precision
      value: 80.37833333333333
    - type: recall
      value: 85.39999999999999
    task:
      type: BitextMining
  - dataset:
      config: ceb-eng
      name: MTEB Tatoeba (ceb-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 47.833333333333336
    - type: f1
      value: 42.006625781625786
    - type: precision
      value: 40.077380952380956
    - type: recall
      value: 47.833333333333336
    task:
      type: BitextMining
  - dataset:
      config: bre-eng
      name: MTEB Tatoeba (bre-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 10.4
    - type: f1
      value: 8.24465007215007
    - type: precision
      value: 7.664597069597071
    - type: recall
      value: 10.4
    task:
      type: BitextMining
  - dataset:
      config: ben-eng
      name: MTEB Tatoeba (ben-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 82.6
    - type: f1
      value: 77.76333333333334
    - type: precision
      value: 75.57833333333332
    - type: recall
      value: 82.6
    task:
      type: BitextMining
  - dataset:
      config: swg-eng
      name: MTEB Tatoeba (swg-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 52.67857142857143
    - type: f1
      value: 44.302721088435376
    - type: precision
      value: 41.49801587301587
    - type: recall
      value: 52.67857142857143
    task:
      type: BitextMining
  - dataset:
      config: arq-eng
      name: MTEB Tatoeba (arq-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 28.3205268935236
    - type: f1
      value: 22.426666605171157
    - type: precision
      value: 20.685900116470915
    - type: recall
      value: 28.3205268935236
    task:
      type: BitextMining
  - dataset:
      config: kab-eng
      name: MTEB Tatoeba (kab-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 22.7
    - type: f1
      value: 17.833970473970474
    - type: precision
      value: 16.407335164835164
    - type: recall
      value: 22.7
    task:
      type: BitextMining
  - dataset:
      config: fra-eng
      name: MTEB Tatoeba (fra-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 92.2
    - type: f1
      value: 89.92999999999999
    - type: precision
      value: 88.87
    - type: recall
      value: 92.2
    task:
      type: BitextMining
  - dataset:
      config: por-eng
      name: MTEB Tatoeba (por-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 91.4
    - type: f1
      value: 89.25
    - type: precision
      value: 88.21666666666667
    - type: recall
      value: 91.4
    task:
      type: BitextMining
  - dataset:
      config: tat-eng
      name: MTEB Tatoeba (tat-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 69.19999999999999
    - type: f1
      value: 63.38269841269841
    - type: precision
      value: 61.14773809523809
    - type: recall
      value: 69.19999999999999
    task:
      type: BitextMining
  - dataset:
      config: oci-eng
      name: MTEB Tatoeba (oci-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 48.8
    - type: f1
      value: 42.839915639915645
    - type: precision
      value: 40.770287114845935
    - type: recall
      value: 48.8
    task:
      type: BitextMining
  - dataset:
      config: pol-eng
      name: MTEB Tatoeba (pol-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 88.8
    - type: f1
      value: 85.90666666666668
    - type: precision
      value: 84.54166666666666
    - type: recall
      value: 88.8
    task:
      type: BitextMining
  - dataset:
      config: war-eng
      name: MTEB Tatoeba (war-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 46.6
    - type: f1
      value: 40.85892920804686
    - type: precision
      value: 38.838223114604695
    - type: recall
      value: 46.6
    task:
      type: BitextMining
  - dataset:
      config: aze-eng
      name: MTEB Tatoeba (aze-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 84.0
    - type: f1
      value: 80.14190476190475
    - type: precision
      value: 78.45333333333333
    - type: recall
      value: 84.0
    task:
      type: BitextMining
  - dataset:
      config: vie-eng
      name: MTEB Tatoeba (vie-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 90.5
    - type: f1
      value: 87.78333333333333
    - type: precision
      value: 86.5
    - type: recall
      value: 90.5
    task:
      type: BitextMining
  - dataset:
      config: nno-eng
      name: MTEB Tatoeba (nno-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 74.5
    - type: f1
      value: 69.48397546897547
    - type: precision
      value: 67.51869047619049
    - type: recall
      value: 74.5
    task:
      type: BitextMining
  - dataset:
      config: cha-eng
      name: MTEB Tatoeba (cha-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 32.846715328467155
    - type: f1
      value: 27.828177499710343
    - type: precision
      value: 26.63451511991658
    - type: recall
      value: 32.846715328467155
    task:
      type: BitextMining
  - dataset:
      config: mhr-eng
      name: MTEB Tatoeba (mhr-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 8.0
    - type: f1
      value: 6.07664116764988
    - type: precision
      value: 5.544177607179943
    - type: recall
      value: 8.0
    task:
      type: BitextMining
  - dataset:
      config: dan-eng
      name: MTEB Tatoeba (dan-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 87.6
    - type: f1
      value: 84.38555555555554
    - type: precision
      value: 82.91583333333334
    - type: recall
      value: 87.6
    task:
      type: BitextMining
  - dataset:
      config: ell-eng
      name: MTEB Tatoeba (ell-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 87.5
    - type: f1
      value: 84.08333333333331
    - type: precision
      value: 82.47333333333333
    - type: recall
      value: 87.5
    task:
      type: BitextMining
  - dataset:
      config: amh-eng
      name: MTEB Tatoeba (amh-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 80.95238095238095
    - type: f1
      value: 76.13095238095238
    - type: precision
      value: 74.05753968253967
    - type: recall
      value: 80.95238095238095
    task:
      type: BitextMining
  - dataset:
      config: pam-eng
      name: MTEB Tatoeba (pam-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 8.799999999999999
    - type: f1
      value: 6.971422975172975
    - type: precision
      value: 6.557814916172301
    - type: recall
      value: 8.799999999999999
    task:
      type: BitextMining
  - dataset:
      config: hsb-eng
      name: MTEB Tatoeba (hsb-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 44.099378881987576
    - type: f1
      value: 37.01649742022413
    - type: precision
      value: 34.69420618488942
    - type: recall
      value: 44.099378881987576
    task:
      type: BitextMining
  - dataset:
      config: srp-eng
      name: MTEB Tatoeba (srp-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 84.3
    - type: f1
      value: 80.32666666666667
    - type: precision
      value: 78.60666666666665
    - type: recall
      value: 84.3
    task:
      type: BitextMining
  - dataset:
      config: epo-eng
      name: MTEB Tatoeba (epo-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 92.5
    - type: f1
      value: 90.49666666666666
    - type: precision
      value: 89.56666666666668
    - type: recall
      value: 92.5
    task:
      type: BitextMining
  - dataset:
      config: kzj-eng
      name: MTEB Tatoeba (kzj-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 10.0
    - type: f1
      value: 8.268423529875141
    - type: precision
      value: 7.878118605532398
    - type: recall
      value: 10.0
    task:
      type: BitextMining
  - dataset:
      config: awa-eng
      name: MTEB Tatoeba (awa-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 79.22077922077922
    - type: f1
      value: 74.27128427128426
    - type: precision
      value: 72.28715728715729
    - type: recall
      value: 79.22077922077922
    task:
      type: BitextMining
  - dataset:
      config: fao-eng
      name: MTEB Tatoeba (fao-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 65.64885496183206
    - type: f1
      value: 58.87495456197747
    - type: precision
      value: 55.992366412213734
    - type: recall
      value: 65.64885496183206
    task:
      type: BitextMining
  - dataset:
      config: mal-eng
      name: MTEB Tatoeba (mal-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 96.06986899563319
    - type: f1
      value: 94.78408539543909
    - type: precision
      value: 94.15332362930616
    - type: recall
      value: 96.06986899563319
    task:
      type: BitextMining
  - dataset:
      config: ile-eng
      name: MTEB Tatoeba (ile-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 77.2
    - type: f1
      value: 71.72571428571428
    - type: precision
      value: 69.41000000000001
    - type: recall
      value: 77.2
    task:
      type: BitextMining
  - dataset:
      config: bos-eng
      name: MTEB Tatoeba (bos-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 86.4406779661017
    - type: f1
      value: 83.2391713747646
    - type: precision
      value: 81.74199623352166
    - type: recall
      value: 86.4406779661017
    task:
      type: BitextMining
  - dataset:
      config: cor-eng
      name: MTEB Tatoeba (cor-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 8.4
    - type: f1
      value: 6.017828743398003
    - type: precision
      value: 5.4829865484756795
    - type: recall
      value: 8.4
    task:
      type: BitextMining
  - dataset:
      config: cat-eng
      name: MTEB Tatoeba (cat-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 83.5
    - type: f1
      value: 79.74833333333333
    - type: precision
      value: 78.04837662337664
    - type: recall
      value: 83.5
    task:
      type: BitextMining
  - dataset:
      config: eus-eng
      name: MTEB Tatoeba (eus-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 60.4
    - type: f1
      value: 54.467301587301584
    - type: precision
      value: 52.23242424242424
    - type: recall
      value: 60.4
    task:
      type: BitextMining
  - dataset:
      config: yue-eng
      name: MTEB Tatoeba (yue-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 74.9
    - type: f1
      value: 69.68699134199134
    - type: precision
      value: 67.59873015873016
    - type: recall
      value: 74.9
    task:
      type: BitextMining
  - dataset:
      config: swe-eng
      name: MTEB Tatoeba (swe-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 88.0
    - type: f1
      value: 84.9652380952381
    - type: precision
      value: 83.66166666666666
    - type: recall
      value: 88.0
    task:
      type: BitextMining
  - dataset:
      config: dtp-eng
      name: MTEB Tatoeba (dtp-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 9.1
    - type: f1
      value: 7.681244588744588
    - type: precision
      value: 7.370043290043291
    - type: recall
      value: 9.1
    task:
      type: BitextMining
  - dataset:
      config: kat-eng
      name: MTEB Tatoeba (kat-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 80.9651474530831
    - type: f1
      value: 76.84220605132133
    - type: precision
      value: 75.19606398962966
    - type: recall
      value: 80.9651474530831
    task:
      type: BitextMining
  - dataset:
      config: jpn-eng
      name: MTEB Tatoeba (jpn-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 86.9
    - type: f1
      value: 83.705
    - type: precision
      value: 82.3120634920635
    - type: recall
      value: 86.9
    task:
      type: BitextMining
  - dataset:
      config: csb-eng
      name: MTEB Tatoeba (csb-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 29.64426877470356
    - type: f1
      value: 23.98763072676116
    - type: precision
      value: 22.506399397703746
    - type: recall
      value: 29.64426877470356
    task:
      type: BitextMining
  - dataset:
      config: xho-eng
      name: MTEB Tatoeba (xho-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 70.4225352112676
    - type: f1
      value: 62.84037558685445
    - type: precision
      value: 59.56572769953053
    - type: recall
      value: 70.4225352112676
    task:
      type: BitextMining
  - dataset:
      config: orv-eng
      name: MTEB Tatoeba (orv-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 19.64071856287425
    - type: f1
      value: 15.125271011207756
    - type: precision
      value: 13.865019261197494
    - type: recall
      value: 19.64071856287425
    task:
      type: BitextMining
  - dataset:
      config: ind-eng
      name: MTEB Tatoeba (ind-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 90.2
    - type: f1
      value: 87.80666666666666
    - type: precision
      value: 86.70833333333331
    - type: recall
      value: 90.2
    task:
      type: BitextMining
  - dataset:
      config: tuk-eng
      name: MTEB Tatoeba (tuk-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 23.15270935960591
    - type: f1
      value: 18.407224958949097
    - type: precision
      value: 16.982385430661292
    - type: recall
      value: 23.15270935960591
    task:
      type: BitextMining
  - dataset:
      config: max-eng
      name: MTEB Tatoeba (max-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 55.98591549295775
    - type: f1
      value: 49.94718309859154
    - type: precision
      value: 47.77864154624717
    - type: recall
      value: 55.98591549295775
    task:
      type: BitextMining
  - dataset:
      config: swh-eng
      name: MTEB Tatoeba (swh-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 73.07692307692307
    - type: f1
      value: 66.74358974358974
    - type: precision
      value: 64.06837606837607
    - type: recall
      value: 73.07692307692307
    task:
      type: BitextMining
  - dataset:
      config: hin-eng
      name: MTEB Tatoeba (hin-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 94.89999999999999
    - type: f1
      value: 93.25
    - type: precision
      value: 92.43333333333332
    - type: recall
      value: 94.89999999999999
    task:
      type: BitextMining
  - dataset:
      config: dsb-eng
      name: MTEB Tatoeba (dsb-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 37.78705636743215
    - type: f1
      value: 31.63899658680452
    - type: precision
      value: 29.72264397629742
    - type: recall
      value: 37.78705636743215
    task:
      type: BitextMining
  - dataset:
      config: ber-eng
      name: MTEB Tatoeba (ber-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 21.6
    - type: f1
      value: 16.91697302697303
    - type: precision
      value: 15.71225147075147
    - type: recall
      value: 21.6
    task:
      type: BitextMining
  - dataset:
      config: tam-eng
      name: MTEB Tatoeba (tam-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 85.01628664495115
    - type: f1
      value: 81.38514037536838
    - type: precision
      value: 79.83170466883823
    - type: recall
      value: 85.01628664495115
    task:
      type: BitextMining
  - dataset:
      config: slk-eng
      name: MTEB Tatoeba (slk-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 83.39999999999999
    - type: f1
      value: 79.96380952380952
    - type: precision
      value: 78.48333333333333
    - type: recall
      value: 83.39999999999999
    task:
      type: BitextMining
  - dataset:
      config: tgl-eng
      name: MTEB Tatoeba (tgl-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 83.2
    - type: f1
      value: 79.26190476190476
    - type: precision
      value: 77.58833333333334
    - type: recall
      value: 83.2
    task:
      type: BitextMining
  - dataset:
      config: ast-eng
      name: MTEB Tatoeba (ast-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 75.59055118110236
    - type: f1
      value: 71.66854143232096
    - type: precision
      value: 70.30183727034121
    - type: recall
      value: 75.59055118110236
    task:
      type: BitextMining
  - dataset:
      config: mkd-eng
      name: MTEB Tatoeba (mkd-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 65.5
    - type: f1
      value: 59.26095238095238
    - type: precision
      value: 56.81909090909092
    - type: recall
      value: 65.5
    task:
      type: BitextMining
  - dataset:
      config: khm-eng
      name: MTEB Tatoeba (khm-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 55.26315789473685
    - type: f1
      value: 47.986523325858506
    - type: precision
      value: 45.33950006595436
    - type: recall
      value: 55.26315789473685
    task:
      type: BitextMining
  - dataset:
      config: ces-eng
      name: MTEB Tatoeba (ces-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 82.89999999999999
    - type: f1
      value: 78.835
    - type: precision
      value: 77.04761904761905
    - type: recall
      value: 82.89999999999999
    task:
      type: BitextMining
  - dataset:
      config: tzl-eng
      name: MTEB Tatoeba (tzl-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 43.269230769230774
    - type: f1
      value: 36.20421245421245
    - type: precision
      value: 33.57371794871795
    - type: recall
      value: 43.269230769230774
    task:
      type: BitextMining
  - dataset:
      config: urd-eng
      name: MTEB Tatoeba (urd-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 88.0
    - type: f1
      value: 84.70666666666666
    - type: precision
      value: 83.23166666666665
    - type: recall
      value: 88.0
    task:
      type: BitextMining
  - dataset:
      config: ara-eng
      name: MTEB Tatoeba (ara-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 77.4
    - type: f1
      value: 72.54666666666667
    - type: precision
      value: 70.54318181818181
    - type: recall
      value: 77.4
    task:
      type: BitextMining
  - dataset:
      config: kor-eng
      name: MTEB Tatoeba (kor-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 78.60000000000001
    - type: f1
      value: 74.1588888888889
    - type: precision
      value: 72.30250000000001
    - type: recall
      value: 78.60000000000001
    task:
      type: BitextMining
  - dataset:
      config: yid-eng
      name: MTEB Tatoeba (yid-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 72.40566037735849
    - type: f1
      value: 66.82587328813744
    - type: precision
      value: 64.75039308176099
    - type: recall
      value: 72.40566037735849
    task:
      type: BitextMining
  - dataset:
      config: fin-eng
      name: MTEB Tatoeba (fin-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 73.8
    - type: f1
      value: 68.56357142857144
    - type: precision
      value: 66.3178822055138
    - type: recall
      value: 73.8
    task:
      type: BitextMining
  - dataset:
      config: tha-eng
      name: MTEB Tatoeba (tha-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 91.78832116788321
    - type: f1
      value: 89.3552311435523
    - type: precision
      value: 88.20559610705597
    - type: recall
      value: 91.78832116788321
    task:
      type: BitextMining
  - dataset:
      config: wuu-eng
      name: MTEB Tatoeba (wuu-eng)
      revision: 9080400076fbadbb4c4dcb136ff4eddc40b42553
      split: test
      type: mteb/tatoeba-bitext-mining
    metrics:
    - type: accuracy
      value: 74.3
    - type: f1
      value: 69.05085581085581
    - type: precision
      value: 66.955
    - type: recall
      value: 74.3
    task:
      type: BitextMining
  - dataset:
      config: default
      name: MTEB Touche2020
      revision: None
      split: test
      type: webis-touche2020
    metrics:
    - type: map_at_1
      value: 2.896
    - type: map_at_10
      value: 8.993
    - type: map_at_100
      value: 14.133999999999999
    - type: map_at_1000
      value: 15.668000000000001
    - type: map_at_3
      value: 5.862
    - type: map_at_5
      value: 7.17
    - type: mrr_at_1
      value: 34.694
    - type: mrr_at_10
      value: 42.931000000000004
    - type: mrr_at_100
      value: 44.81
    - type: mrr_at_1000
      value: 44.81
    - type: mrr_at_3
      value: 38.435
    - type: mrr_at_5
      value: 41.701
    - type: ndcg_at_1
      value: 31.633
    - type: ndcg_at_10
      value: 21.163
    - type: ndcg_at_100
      value: 33.306000000000004
    - type: ndcg_at_1000
      value: 45.275999999999996
    - type: ndcg_at_3
      value: 25.685999999999996
    - type: ndcg_at_5
      value: 23.732
    - type: precision_at_1
      value: 34.694
    - type: precision_at_10
      value: 17.755000000000003
    - type: precision_at_100
      value: 6.938999999999999
    - type: precision_at_1000
      value: 1.48
    - type: precision_at_3
      value: 25.85
    - type: precision_at_5
      value: 23.265
    - type: recall_at_1
      value: 2.896
    - type: recall_at_10
      value: 13.333999999999998
    - type: recall_at_100
      value: 43.517
    - type: recall_at_1000
      value: 79.836
    - type: recall_at_3
      value: 6.306000000000001
    - type: recall_at_5
      value: 8.825
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB ToxicConversationsClassification
      revision: d7c0de2777da35d6aae2200a62c6e0e5af397c4c
      split: test
      type: mteb/toxic_conversations_50k
    metrics:
    - type: accuracy
      value: 69.3874
    - type: ap
      value: 13.829909072469423
    - type: f1
      value: 53.54534203543492
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB TweetSentimentExtractionClassification
      revision: d604517c81ca91fe16a244d1248fc021f9ecee7a
      split: test
      type: mteb/tweet_sentiment_extraction
    metrics:
    - type: accuracy
      value: 62.62026032823995
    - type: f1
      value: 62.85251350485221
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB TwentyNewsgroupsClustering
      revision: 6125ec4e24fa026cec8a478383ee943acfbd5449
      split: test
      type: mteb/twentynewsgroups-clustering
    metrics:
    - type: v_measure
      value: 33.21527881409797
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB TwitterSemEval2015
      revision: 70970daeab8776df92f5ea462b6173c0b46fd2d1
      split: test
      type: mteb/twittersemeval2015-pairclassification
    metrics:
    - type: cos_sim_accuracy
      value: 84.97943613280086
    - type: cos_sim_ap
      value: 70.75454316885921
    - type: cos_sim_f1
      value: 65.38274012676743
    - type: cos_sim_precision
      value: 60.761214318078835
    - type: cos_sim_recall
      value: 70.76517150395777
    - type: dot_accuracy
      value: 79.0546581629612
    - type: dot_ap
      value: 47.3197121792147
    - type: dot_f1
      value: 49.20106524633821
    - type: dot_precision
      value: 42.45499808502489
    - type: dot_recall
      value: 58.49604221635884
    - type: euclidean_accuracy
      value: 85.08076533349228
    - type: euclidean_ap
      value: 70.95016106374474
    - type: euclidean_f1
      value: 65.43987900176455
    - type: euclidean_precision
      value: 62.64478764478765
    - type: euclidean_recall
      value: 68.49604221635884
    - type: manhattan_accuracy
      value: 84.93771234428085
    - type: manhattan_ap
      value: 70.63668388755362
    - type: manhattan_f1
      value: 65.23895401262398
    - type: manhattan_precision
      value: 56.946084218811485
    - type: manhattan_recall
      value: 76.35883905013192
    - type: max_accuracy
      value: 85.08076533349228
    - type: max_ap
      value: 70.95016106374474
    - type: max_f1
      value: 65.43987900176455
    task:
      type: PairClassification
  - dataset:
      config: default
      name: MTEB TwitterURLCorpus
      revision: 8b6510b0b1fa4e4c4f879467980e9be563ec1cdf
      split: test
      type: mteb/twitterurlcorpus-pairclassification
    metrics:
    - type: cos_sim_accuracy
      value: 88.69096130709822
    - type: cos_sim_ap
      value: 84.82526278228542
    - type: cos_sim_f1
      value: 77.65485060585536
    - type: cos_sim_precision
      value: 75.94582658619167
    - type: cos_sim_recall
      value: 79.44256236526024
    - type: dot_accuracy
      value: 80.97954748321496
    - type: dot_ap
      value: 64.81642914145866
    - type: dot_f1
      value: 60.631996987229975
    - type: dot_precision
      value: 54.5897293631712
    - type: dot_recall
      value: 68.17831844779796
    - type: euclidean_accuracy
      value: 88.6987231730508
    - type: euclidean_ap
      value: 84.80003825477253
    - type: euclidean_f1
      value: 77.67194179854496
    - type: euclidean_precision
      value: 75.7128235122094
    - type: euclidean_recall
      value: 79.73514012935017
    - type: manhattan_accuracy
      value: 88.62692591298949
    - type: manhattan_ap
      value: 84.80451408255276
    - type: manhattan_f1
      value: 77.69888949572183
    - type: manhattan_precision
      value: 73.70311528631622
    - type: manhattan_recall
      value: 82.15275639051433
    - type: max_accuracy
      value: 88.6987231730508
    - type: max_ap
      value: 84.82526278228542
    - type: max_f1
      value: 77.69888949572183
    task:
      type: PairClassification
  - dataset:
      config: ru-en
      name: MTEB BUCC.v2 (ru-en)
      revision: 1739dc11ffe9b7bfccd7f3d585aeb4c544fc6677
      split: test
      type: mteb/bucc-bitext-mining
    metrics:
    - type: accuracy
      value: 95.72566678212678
    - type: f1
      value: 94.42443135896548
    - type: main_score
      value: 94.42443135896548
    - type: precision
      value: 93.80868260016165
    - type: recall
      value: 95.72566678212678
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-rus_Cyrl
      name: MTEB BelebeleRetrieval (rus_Cyrl-rus_Cyrl)
      revision: 75b399394a9803252cfec289d103de462763db7c
      split: test
      type: facebook/belebele
    metrics:
    - type: main_score
      value: 92.23599999999999
    - type: map_at_1
      value: 87.111
    - type: map_at_10
      value: 90.717
    - type: map_at_100
      value: 90.879
    - type: map_at_1000
      value: 90.881
    - type: map_at_20
      value: 90.849
    - type: map_at_3
      value: 90.074
    - type: map_at_5
      value: 90.535
    - type: mrr_at_1
      value: 87.1111111111111
    - type: mrr_at_10
      value: 90.7173721340388
    - type: mrr_at_100
      value: 90.87859682638407
    - type: mrr_at_1000
      value: 90.88093553612326
    - type: mrr_at_20
      value: 90.84863516113515
    - type: mrr_at_3
      value: 90.07407407407409
    - type: mrr_at_5
      value: 90.53518518518521
    - type: nauc_map_at_1000_diff1
      value: 92.37373187280554
    - type: nauc_map_at_1000_max
      value: 79.90465445423249
    - type: nauc_map_at_1000_std
      value: -0.6220290556185463
    - type: nauc_map_at_100_diff1
      value: 92.37386697345335
    - type: nauc_map_at_100_max
      value: 79.90991577223959
    - type: nauc_map_at_100_std
      value: -0.602247514642845
    - type: nauc_map_at_10_diff1
      value: 92.30907447072467
    - type: nauc_map_at_10_max
      value: 79.86831935337598
    - type: nauc_map_at_10_std
      value: -0.7455191860719699
    - type: nauc_map_at_1_diff1
      value: 93.29828518358822
    - type: nauc_map_at_1_max
      value: 78.69539619887887
    - type: nauc_map_at_1_std
      value: -4.097150817605763
    - type: nauc_map_at_20_diff1
      value: 92.38414149703077
    - type: nauc_map_at_20_max
      value: 79.94789814504661
    - type: nauc_map_at_20_std
      value: -0.3928031130400773
    - type: nauc_map_at_3_diff1
      value: 92.21688899306734
    - type: nauc_map_at_3_max
      value: 80.34586671780885
    - type: nauc_map_at_3_std
      value: 0.24088319695435909
    - type: nauc_map_at_5_diff1
      value: 92.27931726042982
    - type: nauc_map_at_5_max
      value: 79.99198834003367
    - type: nauc_map_at_5_std
      value: -0.6296366922840796
    - type: nauc_mrr_at_1000_diff1
      value: 92.37373187280554
    - type: nauc_mrr_at_1000_max
      value: 79.90465445423249
    - type: nauc_mrr_at_1000_std
      value: -0.6220290556185463
    - type: nauc_mrr_at_100_diff1
      value: 92.37386697345335
    - type: nauc_mrr_at_100_max
      value: 79.90991577223959
    - type: nauc_mrr_at_100_std
      value: -0.602247514642845
    - type: nauc_mrr_at_10_diff1
      value: 92.30907447072467
    - type: nauc_mrr_at_10_max
      value: 79.86831935337598
    - type: nauc_mrr_at_10_std
      value: -0.7455191860719699
    - type: nauc_mrr_at_1_diff1
      value: 93.29828518358822
    - type: nauc_mrr_at_1_max
      value: 78.69539619887887
    - type: nauc_mrr_at_1_std
      value: -4.097150817605763
    - type: nauc_mrr_at_20_diff1
      value: 92.38414149703077
    - type: nauc_mrr_at_20_max
      value: 79.94789814504661
    - type: nauc_mrr_at_20_std
      value: -0.3928031130400773
    - type: nauc_mrr_at_3_diff1
      value: 92.21688899306734
    - type: nauc_mrr_at_3_max
      value: 80.34586671780885
    - type: nauc_mrr_at_3_std
      value: 0.24088319695435909
    - type: nauc_mrr_at_5_diff1
      value: 92.27931726042982
    - type: nauc_mrr_at_5_max
      value: 79.99198834003367
    - type: nauc_mrr_at_5_std
      value: -0.6296366922840796
    - type: nauc_ndcg_at_1000_diff1
      value: 92.30526497646306
    - type: nauc_ndcg_at_1000_max
      value: 80.12734537480418
    - type: nauc_ndcg_at_1000_std
      value: 0.22849408935578744
    - type: nauc_ndcg_at_100_diff1
      value: 92.31347123202318
    - type: nauc_ndcg_at_100_max
      value: 80.29207038703142
    - type: nauc_ndcg_at_100_std
      value: 0.816825944406239
    - type: nauc_ndcg_at_10_diff1
      value: 92.05430189845808
    - type: nauc_ndcg_at_10_max
      value: 80.16515667442968
    - type: nauc_ndcg_at_10_std
      value: 0.7486447532544893
    - type: nauc_ndcg_at_1_diff1
      value: 93.29828518358822
    - type: nauc_ndcg_at_1_max
      value: 78.69539619887887
    - type: nauc_ndcg_at_1_std
      value: -4.097150817605763
    - type: nauc_ndcg_at_20_diff1
      value: 92.40147868825079
    - type: nauc_ndcg_at_20_max
      value: 80.5117307181802
    - type: nauc_ndcg_at_20_std
      value: 2.0431351539517033
    - type: nauc_ndcg_at_3_diff1
      value: 91.88894444422789
    - type: nauc_ndcg_at_3_max
      value: 81.09256084196045
    - type: nauc_ndcg_at_3_std
      value: 2.422705909643621
    - type: nauc_ndcg_at_5_diff1
      value: 91.99711052955728
    - type: nauc_ndcg_at_5_max
      value: 80.46996334573979
    - type: nauc_ndcg_at_5_std
      value: 0.9086986899040708
    - type: nauc_precision_at_1000_diff1
      value: .nan
    - type: nauc_precision_at_1000_max
      value: .nan
    - type: nauc_precision_at_1000_std
      value: .nan
    - type: nauc_precision_at_100_diff1
      value: 93.46405228758012
    - type: nauc_precision_at_100_max
      value: 100.0
    - type: nauc_precision_at_100_std
      value: 70.71661998132774
    - type: nauc_precision_at_10_diff1
      value: 90.13938908896874
    - type: nauc_precision_at_10_max
      value: 82.21121782046167
    - type: nauc_precision_at_10_std
      value: 13.075230092036083
    - type: nauc_precision_at_1_diff1
      value: 93.29828518358822
    - type: nauc_precision_at_1_max
      value: 78.69539619887887
    - type: nauc_precision_at_1_std
      value: -4.097150817605763
    - type: nauc_precision_at_20_diff1
      value: 94.9723479135242
    - type: nauc_precision_at_20_max
      value: 91.04000574588684
    - type: nauc_precision_at_20_std
      value: 48.764634058749586
    - type: nauc_precision_at_3_diff1
      value: 90.52690041533852
    - type: nauc_precision_at_3_max
      value: 84.35075179497126
    - type: nauc_precision_at_3_std
      value: 12.036768730480507
    - type: nauc_precision_at_5_diff1
      value: 90.44234360410769
    - type: nauc_precision_at_5_max
      value: 83.21895424836558
    - type: nauc_precision_at_5_std
      value: 9.974323062558037
    - type: nauc_recall_at_1000_diff1
      value: .nan
    - type: nauc_recall_at_1000_max
      value: .nan
    - type: nauc_recall_at_1000_std
      value: .nan
    - type: nauc_recall_at_100_diff1
      value: 93.46405228758294
    - type: nauc_recall_at_100_max
      value: 100.0
    - type: nauc_recall_at_100_std
      value: 70.71661998132666
    - type: nauc_recall_at_10_diff1
      value: 90.13938908896864
    - type: nauc_recall_at_10_max
      value: 82.21121782046124
    - type: nauc_recall_at_10_std
      value: 13.075230092036506
    - type: nauc_recall_at_1_diff1
      value: 93.29828518358822
    - type: nauc_recall_at_1_max
      value: 78.69539619887887
    - type: nauc_recall_at_1_std
      value: -4.097150817605763
    - type: nauc_recall_at_20_diff1
      value: 94.97234791352489
    - type: nauc_recall_at_20_max
      value: 91.04000574588774
    - type: nauc_recall_at_20_std
      value: 48.764634058752065
    - type: nauc_recall_at_3_diff1
      value: 90.52690041533845
    - type: nauc_recall_at_3_max
      value: 84.35075179497079
    - type: nauc_recall_at_3_std
      value: 12.036768730480583
    - type: nauc_recall_at_5_diff1
      value: 90.44234360410861
    - type: nauc_recall_at_5_max
      value: 83.21895424836595
    - type: nauc_recall_at_5_std
      value: 9.974323062558147
    - type: ndcg_at_1
      value: 87.111
    - type: ndcg_at_10
      value: 92.23599999999999
    - type: ndcg_at_100
      value: 92.87100000000001
    - type: ndcg_at_1000
      value: 92.928
    - type: ndcg_at_20
      value: 92.67699999999999
    - type: ndcg_at_3
      value: 90.973
    - type: ndcg_at_5
      value: 91.801
    - type: precision_at_1
      value: 87.111
    - type: precision_at_10
      value: 9.689
    - type: precision_at_100
      value: 0.996
    - type: precision_at_1000
      value: 0.1
    - type: precision_at_20
      value: 4.928
    - type: precision_at_3
      value: 31.185000000000002
    - type: precision_at_5
      value: 19.111
    - type: recall_at_1
      value: 87.111
    - type: recall_at_10
      value: 96.88900000000001
    - type: recall_at_100
      value: 99.556
    - type: recall_at_1000
      value: 100.0
    - type: recall_at_20
      value: 98.556
    - type: recall_at_3
      value: 93.556
    - type: recall_at_5
      value: 95.556
    task:
      type: Retrieval
  - dataset:
      config: rus_Cyrl-eng_Latn
      name: MTEB BelebeleRetrieval (rus_Cyrl-eng_Latn)
      revision: 75b399394a9803252cfec289d103de462763db7c
      split: test
      type: facebook/belebele
    metrics:
    - type: main_score
      value: 86.615
    - type: map_at_1
      value: 78.0
    - type: map_at_10
      value: 83.822
    - type: map_at_100
      value: 84.033
    - type: map_at_1000
      value: 84.03500000000001
    - type: map_at_20
      value: 83.967
    - type: map_at_3
      value: 82.315
    - type: map_at_5
      value: 83.337
    - type: mrr_at_1
      value: 78.0
    - type: mrr_at_10
      value: 83.82213403880073
    - type: mrr_at_100
      value: 84.03281327810801
    - type: mrr_at_1000
      value: 84.03460051000452
    - type: mrr_at_20
      value: 83.9673773122303
    - type: mrr_at_3
      value: 82.31481481481484
    - type: mrr_at_5
      value: 83.33703703703708
    - type: nauc_map_at_1000_diff1
      value: 80.78467576987832
    - type: nauc_map_at_1000_max
      value: 51.41718334647604
    - type: nauc_map_at_1000_std
      value: -16.23873782768812
    - type: nauc_map_at_100_diff1
      value: 80.78490931240695
    - type: nauc_map_at_100_max
      value: 51.41504597713061
    - type: nauc_map_at_100_std
      value: -16.23538559475366
    - type: nauc_map_at_10_diff1
      value: 80.73989245374868
    - type: nauc_map_at_10_max
      value: 51.43026079433827
    - type: nauc_map_at_10_std
      value: -16.13414330905897
    - type: nauc_map_at_1_diff1
      value: 82.36966971144186
    - type: nauc_map_at_1_max
      value: 52.988877039509916
    - type: nauc_map_at_1_std
      value: -15.145824639495546
    - type: nauc_map_at_20_diff1
      value: 80.75923781626145
    - type: nauc_map_at_20_max
      value: 51.40181079374639
    - type: nauc_map_at_20_std
      value: -16.260566097377165
    - type: nauc_map_at_3_diff1
      value: 80.65242627065471
    - type: nauc_map_at_3_max
      value: 50.623980338841214
    - type: nauc_map_at_3_std
      value: -16.818343442794294
    - type: nauc_map_at_5_diff1
      value: 80.45976387021862
    - type: nauc_map_at_5_max
      value: 51.533621728445866
    - type: nauc_map_at_5_std
      value: -16.279891536945815
    - type: nauc_mrr_at_1000_diff1
      value: 80.78467576987832
    - type: nauc_mrr_at_1000_max
      value: 51.41718334647604
    - type: nauc_mrr_at_1000_std
      value: -16.23873782768812
    - type: nauc_mrr_at_100_diff1
      value: 80.78490931240695
    - type: nauc_mrr_at_100_max
      value: 51.41504597713061
    - type: nauc_mrr_at_100_std
      value: -16.23538559475366
    - type: nauc_mrr_at_10_diff1
      value: 80.73989245374868
    - type: nauc_mrr_at_10_max
      value: 51.43026079433827
    - type: nauc_mrr_at_10_std
      value: -16.13414330905897
    - type: nauc_mrr_at_1_diff1
      value: 82.36966971144186
    - type: nauc_mrr_at_1_max
      value: 52.988877039509916
    - type: nauc_mrr_at_1_std
      value: -15.145824639495546
    - type: nauc_mrr_at_20_diff1
      value: 80.75923781626145
    - type: nauc_mrr_at_20_max
      value: 51.40181079374639
    - type: nauc_mrr_at_20_std
      value: -16.260566097377165
    - type: nauc_mrr_at_3_diff1
      value: 80.65242627065471
    - type: nauc_mrr_at_3_max
      value: 50.623980338841214
    - type: nauc_mrr_at_3_std
      value: -16.818343442794294
    - type: nauc_mrr_at_5_diff1
      value: 80.45976387021862
    - type: nauc_mrr_at_5_max
      value: 51.533621728445866
    - type: nauc_mrr_at_5_std
      value: -16.279891536945815
    - type: nauc_ndcg_at_1000_diff1
      value: 80.60009446938174
    - type: nauc_ndcg_at_1000_max
      value: 51.381708043594166
    - type: nauc_ndcg_at_1000_std
      value: -16.054256944160848
    - type: nauc_ndcg_at_100_diff1
      value: 80.58971462930421
    - type: nauc_ndcg_at_100_max
      value: 51.25436917735444
    - type: nauc_ndcg_at_100_std
      value: -15.862944972269894
    - type: nauc_ndcg_at_10_diff1
      value: 80.37967179454489
    - type: nauc_ndcg_at_10_max
      value: 51.590394257251006
    - type: nauc_ndcg_at_10_std
      value: -15.489799384799591
    - type: nauc_ndcg_at_1_diff1
      value: 82.36966971144186
    - type: nauc_ndcg_at_1_max
      value: 52.988877039509916
    - type: nauc_ndcg_at_1_std
      value: -15.145824639495546
    - type: nauc_ndcg_at_20_diff1
      value: 80.40299527470081
    - type: nauc_ndcg_at_20_max
      value: 51.395132284307074
    - type: nauc_ndcg_at_20_std
      value: -15.906165526937203
    - type: nauc_ndcg_at_3_diff1
      value: 80.10347913649302
    - type: nauc_ndcg_at_3_max
      value: 50.018431855573844
    - type: nauc_ndcg_at_3_std
      value: -17.12743750163884
    - type: nauc_ndcg_at_5_diff1
      value: 79.65918647776613
    - type: nauc_ndcg_at_5_max
      value: 51.76710880330806
    - type: nauc_ndcg_at_5_std
      value: -16.071901882035945
    - type: nauc_precision_at_1000_diff1
      value: .nan
    - type: nauc_precision_at_1000_max
      value: .nan
    - type: nauc_precision_at_1000_std
      value: .nan
    - type: nauc_precision_at_100_diff1
      value: 77.41596638655459
    - type: nauc_precision_at_100_max
      value: 22.572362278246565
    - type: nauc_precision_at_100_std
      value: 26.890756302525716
    - type: nauc_precision_at_10_diff1
      value: 77.82112845138009
    - type: nauc_precision_at_10_max
      value: 54.2550353474723
    - type: nauc_precision_at_10_std
      value: -7.492997198879646
    - type: nauc_precision_at_1_diff1
      value: 82.36966971144186
    - type: nauc_precision_at_1_max
      value: 52.988877039509916
    - type: nauc_precision_at_1_std
      value: -15.145824639495546
    - type: nauc_precision_at_20_diff1
      value: 75.89091192032318
    - type: nauc_precision_at_20_max
      value: 52.03275754746293
    - type: nauc_precision_at_20_std
      value: -7.8411920323686175
    - type: nauc_precision_at_3_diff1
      value: 78.0256020644638
    - type: nauc_precision_at_3_max
      value: 47.80353641248523
    - type: nauc_precision_at_3_std
      value: -18.181625255723503
    - type: nauc_precision_at_5_diff1
      value: 75.21583976056174
    - type: nauc_precision_at_5_max
      value: 53.716281032960765
    - type: nauc_precision_at_5_std
      value: -14.411700753360812
    - type: nauc_recall_at_1000_diff1
      value: .nan
    - type: nauc_recall_at_1000_max
      value: .nan
    - type: nauc_recall_at_1000_std
      value: .nan
    - type: nauc_recall_at_100_diff1
      value: 77.4159663865523
    - type: nauc_recall_at_100_max
      value: 22.57236227824646
    - type: nauc_recall_at_100_std
      value: 26.89075630252133
    - type: nauc_recall_at_10_diff1
      value: 77.82112845138037
    - type: nauc_recall_at_10_max
      value: 54.25503534747204
    - type: nauc_recall_at_10_std
      value: -7.492997198879666
    - type: nauc_recall_at_1_diff1
      value: 82.36966971144186
    - type: nauc_recall_at_1_max
      value: 52.988877039509916
    - type: nauc_recall_at_1_std
      value: -15.145824639495546
    - type: nauc_recall_at_20_diff1
      value: 75.89091192032362
    - type: nauc_recall_at_20_max
      value: 52.032757547463184
    - type: nauc_recall_at_20_std
      value: -7.84119203236888
    - type: nauc_recall_at_3_diff1
      value: 78.02560206446354
    - type: nauc_recall_at_3_max
      value: 47.80353641248526
    - type: nauc_recall_at_3_std
      value: -18.181625255723656
    - type: nauc_recall_at_5_diff1
      value: 75.21583976056185
    - type: nauc_recall_at_5_max
      value: 53.71628103296118
    - type: nauc_recall_at_5_std
      value: -14.411700753360634
    - type: ndcg_at_1
      value: 78.0
    - type: ndcg_at_10
      value: 86.615
    - type: ndcg_at_100
      value: 87.558
    - type: ndcg_at_1000
      value: 87.613
    - type: ndcg_at_20
      value: 87.128
    - type: ndcg_at_3
      value: 83.639
    - type: ndcg_at_5
      value: 85.475
    - type: precision_at_1
      value: 78.0
    - type: precision_at_10
      value: 9.533
    - type: precision_at_100
      value: 0.996
    - type: precision_at_1000
      value: 0.1
    - type: precision_at_20
      value: 4.867
    - type: precision_at_3
      value: 29.148000000000003
    - type: precision_at_5
      value: 18.378
    - type: recall_at_1
      value: 78.0
    - type: recall_at_10
      value: 95.333
    - type: recall_at_100
      value: 99.556
    - type: recall_at_1000
      value: 100.0
    - type: recall_at_20
      value: 97.333
    - type: recall_at_3
      value: 87.444
    - type: recall_at_5
      value: 91.889
    task:
      type: Retrieval
  - dataset:
      config: eng_Latn-rus_Cyrl
      name: MTEB BelebeleRetrieval (eng_Latn-rus_Cyrl)
      revision: 75b399394a9803252cfec289d103de462763db7c
      split: test
      type: facebook/belebele
    metrics:
    - type: main_score
      value: 82.748
    - type: map_at_1
      value: 73.444
    - type: map_at_10
      value: 79.857
    - type: map_at_100
      value: 80.219
    - type: map_at_1000
      value: 80.22500000000001
    - type: map_at_20
      value: 80.10300000000001
    - type: map_at_3
      value: 78.593
    - type: map_at_5
      value: 79.515
    - type: mrr_at_1
      value: 73.44444444444444
    - type: mrr_at_10
      value: 79.85705467372136
    - type: mrr_at_100
      value: 80.21942320422542
    - type: mrr_at_1000
      value: 80.2245364027152
    - type: mrr_at_20
      value: 80.10273201266493
    - type: mrr_at_3
      value: 78.59259259259258
    - type: mrr_at_5
      value: 79.51481481481483
    - type: nauc_map_at_1000_diff1
      value: 83.69682652271125
    - type: nauc_map_at_1000_max
      value: 61.70131708044767
    - type: nauc_map_at_1000_std
      value: 9.345825405274955
    - type: nauc_map_at_100_diff1
      value: 83.68924820523492
    - type: nauc_map_at_100_max
      value: 61.6965735573098
    - type: nauc_map_at_100_std
      value: 9.366132859525775
    - type: nauc_map_at_10_diff1
      value: 83.61802964269985
    - type: nauc_map_at_10_max
      value: 61.74274476167882
    - type: nauc_map_at_10_std
      value: 9.504060995819101
    - type: nauc_map_at_1_diff1
      value: 86.37079221403225
    - type: nauc_map_at_1_max
      value: 61.856861655370686
    - type: nauc_map_at_1_std
      value: 4.708911881992707
    - type: nauc_map_at_20_diff1
      value: 83.62920965453047
    - type: nauc_map_at_20_max
      value: 61.761029350326965
    - type: nauc_map_at_20_std
      value: 9.572978651118351
    - type: nauc_map_at_3_diff1
      value: 83.66665673154306
    - type: nauc_map_at_3_max
      value: 61.13597610587937
    - type: nauc_map_at_3_std
      value: 9.309596395240598
    - type: nauc_map_at_5_diff1
      value: 83.52307226455358
    - type: nauc_map_at_5_max
      value: 61.59405758027573
    - type: nauc_map_at_5_std
      value: 9.320025423287671
    - type: nauc_mrr_at_1000_diff1
      value: 83.69682652271125
    - type: nauc_mrr_at_1000_max
      value: 61.70131708044767
    - type: nauc_mrr_at_1000_std
      value: 9.345825405274955
    - type: nauc_mrr_at_100_diff1
      value: 83.68924820523492
    - type: nauc_mrr_at_100_max
      value: 61.6965735573098
    - type: nauc_mrr_at_100_std
      value: 9.366132859525775
    - type: nauc_mrr_at_10_diff1
      value: 83.61802964269985
    - type: nauc_mrr_at_10_max
      value: 61.74274476167882
    - type: nauc_mrr_at_10_std
      value: 9.504060995819101
    - type: nauc_mrr_at_1_diff1
      value: 86.37079221403225
    - type: nauc_mrr_at_1_max
      value: 61.856861655370686
    - type: nauc_mrr_at_1_std
      value: 4.708911881992707
    - type: nauc_mrr_at_20_diff1
      value: 83.62920965453047
    - type: nauc_mrr_at_20_max
      value: 61.761029350326965
    - type: nauc_mrr_at_20_std
      value: 9.572978651118351
    - type: nauc_mrr_at_3_diff1
      value: 83.66665673154306
    - type: nauc_mrr_at_3_max
      value: 61.13597610587937
    - type: nauc_mrr_at_3_std
      value: 9.309596395240598
    - type: nauc_mrr_at_5_diff1
      value: 83.52307226455358
    - type: nauc_mrr_at_5_max
      value: 61.59405758027573
    - type: nauc_mrr_at_5_std
      value: 9.320025423287671
    - type: nauc_ndcg_at_1000_diff1
      value: 83.24213186482201
    - type: nauc_ndcg_at_1000_max
      value: 61.77629841787496
    - type: nauc_ndcg_at_1000_std
      value: 10.332527869705851
    - type: nauc_ndcg_at_100_diff1
      value: 83.06815820441027
    - type: nauc_ndcg_at_100_max
      value: 61.6947181864579
    - type: nauc_ndcg_at_100_std
      value: 10.888922975877316
    - type: nauc_ndcg_at_10_diff1
      value: 82.58238431386295
    - type: nauc_ndcg_at_10_max
      value: 62.10333663935709
    - type: nauc_ndcg_at_10_std
      value: 11.746030330958174
    - type: nauc_ndcg_at_1_diff1
      value: 86.37079221403225
    - type: nauc_ndcg_at_1_max
      value: 61.856861655370686
    - type: nauc_ndcg_at_1_std
      value: 4.708911881992707
    - type: nauc_ndcg_at_20_diff1
      value: 82.67888324480154
    - type: nauc_ndcg_at_20_max
      value: 62.28124917486516
    - type: nauc_ndcg_at_20_std
      value: 12.343058917563914
    - type: nauc_ndcg_at_3_diff1
      value: 82.71277373710663
    - type: nauc_ndcg_at_3_max
      value: 60.66677922989939
    - type: nauc_ndcg_at_3_std
      value: 10.843633736296528
    - type: nauc_ndcg_at_5_diff1
      value: 82.34691124846786
    - type: nauc_ndcg_at_5_max
      value: 61.605961382062716
    - type: nauc_ndcg_at_5_std
      value: 11.129011077702602
    - type: nauc_precision_at_1000_diff1
      value: .nan
    - type: nauc_precision_at_1000_max
      value: .nan
    - type: nauc_precision_at_1000_std
      value: .nan
    - type: nauc_precision_at_100_diff1
      value: 60.93103908230194
    - type: nauc_precision_at_100_max
      value: 52.621048419370695
    - type: nauc_precision_at_100_std
      value: 85.60090702947922
    - type: nauc_precision_at_10_diff1
      value: 76.26517273576093
    - type: nauc_precision_at_10_max
      value: 65.2013694366636
    - type: nauc_precision_at_10_std
      value: 26.50357920946173
    - type: nauc_precision_at_1_diff1
      value: 86.37079221403225
    - type: nauc_precision_at_1_max
      value: 61.856861655370686
    - type: nauc_precision_at_1_std
      value: 4.708911881992707
    - type: nauc_precision_at_20_diff1
      value: 73.47946930710295
    - type: nauc_precision_at_20_max
      value: 70.19520986689217
    - type: nauc_precision_at_20_std
      value: 45.93186111653967
    - type: nauc_precision_at_3_diff1
      value: 79.02026879450186
    - type: nauc_precision_at_3_max
      value: 58.75074624692399
    - type: nauc_precision_at_3_std
      value: 16.740684654251037
    - type: nauc_precision_at_5_diff1
      value: 76.47585662281637
    - type: nauc_precision_at_5_max
      value: 61.86270922013127
    - type: nauc_precision_at_5_std
      value: 20.1833625455035
    - type: nauc_recall_at_1000_diff1
      value: .nan
    - type: nauc_recall_at_1000_max
      value: .nan
    - type: nauc_recall_at_1000_std
      value: .nan
    - type: nauc_recall_at_100_diff1
      value: 60.93103908229921
    - type: nauc_recall_at_100_max
      value: 52.62104841936668
    - type: nauc_recall_at_100_std
      value: 85.60090702947748
    - type: nauc_recall_at_10_diff1
      value: 76.26517273576097
    - type: nauc_recall_at_10_max
      value: 65.20136943666347
    - type: nauc_recall_at_10_std
      value: 26.50357920946174
    - type: nauc_recall_at_1_diff1
      value: 86.37079221403225
    - type: nauc_recall_at_1_max
      value: 61.856861655370686
    - type: nauc_recall_at_1_std
      value: 4.708911881992707
    - type: nauc_recall_at_20_diff1
      value: 73.47946930710269
    - type: nauc_recall_at_20_max
      value: 70.19520986689254
    - type: nauc_recall_at_20_std
      value: 45.93186111653943
    - type: nauc_recall_at_3_diff1
      value: 79.02026879450173
    - type: nauc_recall_at_3_max
      value: 58.750746246923924
    - type: nauc_recall_at_3_std
      value: 16.740684654251076
    - type: nauc_recall_at_5_diff1
      value: 76.4758566228162
    - type: nauc_recall_at_5_max
      value: 61.862709220131386
    - type: nauc_recall_at_5_std
      value: 20.18336254550361
    - type: ndcg_at_1
      value: 73.444
    - type: ndcg_at_10
      value: 82.748
    - type: ndcg_at_100
      value: 84.416
    - type: ndcg_at_1000
      value: 84.52300000000001
    - type: ndcg_at_20
      value: 83.646
    - type: ndcg_at_3
      value: 80.267
    - type: ndcg_at_5
      value: 81.922
    - type: precision_at_1
      value: 73.444
    - type: precision_at_10
      value: 9.167
    - type: precision_at_100
      value: 0.992
    - type: precision_at_1000
      value: 0.1
    - type: precision_at_20
      value: 4.761
    - type: precision_at_3
      value: 28.37
    - type: precision_at_5
      value: 17.822
    - type: recall_at_1
      value: 73.444
    - type: recall_at_10
      value: 91.667
    - type: recall_at_100
      value: 99.222
    - type: recall_at_1000
      value: 100.0
    - type: recall_at_20
      value: 95.222
    - type: recall_at_3
      value: 85.111
    - type: recall_at_5
      value: 89.11099999999999
    task:
      type: Retrieval
  - dataset:
      config: eng_Latn-rus_Cyrl
      name: MTEB BibleNLPBitextMining (eng_Latn-rus_Cyrl)
      revision: 264a18480c529d9e922483839b4b9758e690b762
      split: train
      type: davidstap/biblenlp-corpus-mmteb
    metrics:
    - type: accuracy
      value: 96.875
    - type: f1
      value: 95.83333333333333
    - type: main_score
      value: 95.83333333333333
    - type: precision
      value: 95.3125
    - type: recall
      value: 96.875
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-eng_Latn
      name: MTEB BibleNLPBitextMining (rus_Cyrl-eng_Latn)
      revision: 264a18480c529d9e922483839b4b9758e690b762
      split: train
      type: davidstap/biblenlp-corpus-mmteb
    metrics:
    - type: accuracy
      value: 88.671875
    - type: f1
      value: 85.3515625
    - type: main_score
      value: 85.3515625
    - type: precision
      value: 83.85416666666667
    - type: recall
      value: 88.671875
    task:
      type: BitextMining
  - dataset:
      config: default
      name: MTEB CEDRClassification (default)
      revision: c0ba03d058e3e1b2f3fd20518875a4563dd12db4
      split: test
      type: ai-forever/cedr-classification
    metrics:
    - type: accuracy
      value: 40.06907545164719
    - type: f1
      value: 26.285000550712407
    - type: lrap
      value: 64.4280021253997
    - type: main_score
      value: 40.06907545164719
    task:
      type: MultilabelClassification
  - dataset:
      config: default
      name: MTEB CyrillicTurkicLangClassification (default)
      revision: e42d330f33d65b7b72dfd408883daf1661f06f18
      split: test
      type: tatiana-merz/cyrillic_turkic_langs
    metrics:
    - type: accuracy
      value: 43.3447265625
    - type: f1
      value: 40.08400146827895
    - type: f1_weighted
      value: 40.08499428040896
    - type: main_score
      value: 43.3447265625
    task:
      type: Classification
  - dataset:
      config: ace_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (ace_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 6.225296442687747
    - type: f1
      value: 5.5190958860075
    - type: main_score
      value: 5.5190958860075
    - type: precision
      value: 5.3752643758000005
    - type: recall
      value: 6.225296442687747
    task:
      type: BitextMining
  - dataset:
      config: bam_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (bam_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 68.37944664031622
    - type: f1
      value: 64.54819836666252
    - type: main_score
      value: 64.54819836666252
    - type: precision
      value: 63.07479233454916
    - type: recall
      value: 68.37944664031622
    task:
      type: BitextMining
  - dataset:
      config: dzo_Tibt-rus_Cyrl
      name: MTEB FloresBitextMining (dzo_Tibt-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 0.09881422924901186
    - type: f1
      value: 0.00019509225912934226
    - type: main_score
      value: 0.00019509225912934226
    - type: precision
      value: 9.76425190207627e-05
    - type: recall
      value: 0.09881422924901186
    task:
      type: BitextMining
  - dataset:
      config: hin_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (hin_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.60474308300395
    - type: f1
      value: 99.47299077733861
    - type: main_score
      value: 99.47299077733861
    - type: precision
      value: 99.40711462450594
    - type: recall
      value: 99.60474308300395
    task:
      type: BitextMining
  - dataset:
      config: khm_Khmr-rus_Cyrl
      name: MTEB FloresBitextMining (khm_Khmr-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 88.83399209486166
    - type: f1
      value: 87.71151056318254
    - type: main_score
      value: 87.71151056318254
    - type: precision
      value: 87.32012500709193
    - type: recall
      value: 88.83399209486166
    task:
      type: BitextMining
  - dataset:
      config: mag_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (mag_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.7239789196311
    - type: main_score
      value: 97.7239789196311
    - type: precision
      value: 97.61904761904762
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: pap_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (pap_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.0711462450593
    - type: f1
      value: 93.68187806922984
    - type: main_score
      value: 93.68187806922984
    - type: precision
      value: 93.58925452707051
    - type: recall
      value: 94.0711462450593
    task:
      type: BitextMining
  - dataset:
      config: sot_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (sot_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 90.9090909090909
    - type: f1
      value: 89.23171936758892
    - type: main_score
      value: 89.23171936758892
    - type: precision
      value: 88.51790014083866
    - type: recall
      value: 90.9090909090909
    task:
      type: BitextMining
  - dataset:
      config: tur_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (tur_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.9459815546772
    - type: main_score
      value: 98.9459815546772
    - type: precision
      value: 98.81422924901186
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: ace_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ace_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 66.10671936758892
    - type: f1
      value: 63.81888256297873
    - type: main_score
      value: 63.81888256297873
    - type: precision
      value: 63.01614067933451
    - type: recall
      value: 66.10671936758892
    task:
      type: BitextMining
  - dataset:
      config: ban_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ban_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 79.44664031620553
    - type: f1
      value: 77.6311962082713
    - type: main_score
      value: 77.6311962082713
    - type: precision
      value: 76.93977931929739
    - type: recall
      value: 79.44664031620553
    task:
      type: BitextMining
  - dataset:
      config: ell_Grek-rus_Cyrl
      name: MTEB FloresBitextMining (ell_Grek-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.2094861660079
    - type: main_score
      value: 99.2094861660079
    - type: precision
      value: 99.1106719367589
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: hne_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (hne_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.83794466403161
    - type: f1
      value: 96.25352907961603
    - type: main_score
      value: 96.25352907961603
    - type: precision
      value: 96.02155091285526
    - type: recall
      value: 96.83794466403161
    task:
      type: BitextMining
  - dataset:
      config: kik_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kik_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 76.28458498023716
    - type: f1
      value: 73.5596919895859
    - type: main_score
      value: 73.5596919895859
    - type: precision
      value: 72.40900759055246
    - type: recall
      value: 76.28458498023716
    task:
      type: BitextMining
  - dataset:
      config: mai_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (mai_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.72727272727273
    - type: f1
      value: 97.37812911725956
    - type: main_score
      value: 97.37812911725956
    - type: precision
      value: 97.26002258610953
    - type: recall
      value: 97.72727272727273
    task:
      type: BitextMining
  - dataset:
      config: pbt_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (pbt_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.0711462450593
    - type: f1
      value: 93.34700387331966
    - type: main_score
      value: 93.34700387331966
    - type: precision
      value: 93.06920556920556
    - type: recall
      value: 94.0711462450593
    task:
      type: BitextMining
  - dataset:
      config: spa_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (spa_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.9459815546772
    - type: main_score
      value: 98.9459815546772
    - type: precision
      value: 98.81422924901186
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: twi_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (twi_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 80.73122529644269
    - type: f1
      value: 77.77434363246721
    - type: main_score
      value: 77.77434363246721
    - type: precision
      value: 76.54444287596462
    - type: recall
      value: 80.73122529644269
    task:
      type: BitextMining
  - dataset:
      config: acm_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (acm_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.56521739130434
    - type: f1
      value: 92.92490118577075
    - type: main_score
      value: 92.92490118577075
    - type: precision
      value: 92.16897233201581
    - type: recall
      value: 94.56521739130434
    task:
      type: BitextMining
  - dataset:
      config: bel_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (bel_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.98550724637681
    - type: main_score
      value: 98.98550724637681
    - type: precision
      value: 98.88833992094862
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: eng_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (eng_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.60474308300395
    - type: f1
      value: 99.4729907773386
    - type: main_score
      value: 99.4729907773386
    - type: precision
      value: 99.40711462450594
    - type: recall
      value: 99.60474308300395
    task:
      type: BitextMining
  - dataset:
      config: hrv_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (hrv_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 99.05138339920948
    - type: main_score
      value: 99.05138339920948
    - type: precision
      value: 99.00691699604744
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: kin_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kin_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 88.2411067193676
    - type: f1
      value: 86.5485246227658
    - type: main_score
      value: 86.5485246227658
    - type: precision
      value: 85.90652101521667
    - type: recall
      value: 88.2411067193676
    task:
      type: BitextMining
  - dataset:
      config: mal_Mlym-rus_Cyrl
      name: MTEB FloresBitextMining (mal_Mlym-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.51778656126481
    - type: f1
      value: 98.07971014492753
    - type: main_score
      value: 98.07971014492753
    - type: precision
      value: 97.88372859025033
    - type: recall
      value: 98.51778656126481
    task:
      type: BitextMining
  - dataset:
      config: pes_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (pes_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.51778656126481
    - type: f1
      value: 98.0566534914361
    - type: main_score
      value: 98.0566534914361
    - type: precision
      value: 97.82608695652173
    - type: recall
      value: 98.51778656126481
    task:
      type: BitextMining
  - dataset:
      config: srd_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (srd_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 82.6086956521739
    - type: f1
      value: 80.9173470979821
    - type: main_score
      value: 80.9173470979821
    - type: precision
      value: 80.24468672882627
    - type: recall
      value: 82.6086956521739
    task:
      type: BitextMining
  - dataset:
      config: tzm_Tfng-rus_Cyrl
      name: MTEB FloresBitextMining (tzm_Tfng-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 7.41106719367589
    - type: f1
      value: 6.363562740945329
    - type: main_score
      value: 6.363562740945329
    - type: precision
      value: 6.090373175353411
    - type: recall
      value: 7.41106719367589
    task:
      type: BitextMining
  - dataset:
      config: acq_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (acq_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.25691699604744
    - type: f1
      value: 93.81422924901187
    - type: main_score
      value: 93.81422924901187
    - type: precision
      value: 93.14064558629775
    - type: recall
      value: 95.25691699604744
    task:
      type: BitextMining
  - dataset:
      config: bem_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (bem_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 68.08300395256917
    - type: f1
      value: 65.01368772860867
    - type: main_score
      value: 65.01368772860867
    - type: precision
      value: 63.91052337510628
    - type: recall
      value: 68.08300395256917
    task:
      type: BitextMining
  - dataset:
      config: epo_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (epo_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.41897233201581
    - type: f1
      value: 98.17193675889328
    - type: main_score
      value: 98.17193675889328
    - type: precision
      value: 98.08210564139418
    - type: recall
      value: 98.41897233201581
    task:
      type: BitextMining
  - dataset:
      config: hun_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (hun_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.1106719367589
    - type: main_score
      value: 99.1106719367589
    - type: precision
      value: 99.01185770750988
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: kir_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (kir_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.5296442687747
    - type: f1
      value: 97.07549806364035
    - type: main_score
      value: 97.07549806364035
    - type: precision
      value: 96.90958498023716
    - type: recall
      value: 97.5296442687747
    task:
      type: BitextMining
  - dataset:
      config: mar_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (mar_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.82608695652173
    - type: f1
      value: 97.44400527009222
    - type: main_score
      value: 97.44400527009222
    - type: precision
      value: 97.28966685488425
    - type: recall
      value: 97.82608695652173
    task:
      type: BitextMining
  - dataset:
      config: plt_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (plt_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 79.9407114624506
    - type: f1
      value: 78.3154177760691
    - type: main_score
      value: 78.3154177760691
    - type: precision
      value: 77.69877344877344
    - type: recall
      value: 79.9407114624506
    task:
      type: BitextMining
  - dataset:
      config: srp_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (srp_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.70355731225297
    - type: f1
      value: 99.60474308300395
    - type: main_score
      value: 99.60474308300395
    - type: precision
      value: 99.55533596837944
    - type: recall
      value: 99.70355731225297
    task:
      type: BitextMining
  - dataset:
      config: uig_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (uig_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 83.20158102766798
    - type: f1
      value: 81.44381923034585
    - type: main_score
      value: 81.44381923034585
    - type: precision
      value: 80.78813411582477
    - type: recall
      value: 83.20158102766798
    task:
      type: BitextMining
  - dataset:
      config: aeb_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (aeb_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.20553359683794
    - type: f1
      value: 88.75352907961603
    - type: main_score
      value: 88.75352907961603
    - type: precision
      value: 87.64328063241106
    - type: recall
      value: 91.20553359683794
    task:
      type: BitextMining
  - dataset:
      config: ben_Beng-rus_Cyrl
      name: MTEB FloresBitextMining (ben_Beng-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.60671936758894
    - type: main_score
      value: 98.60671936758894
    - type: precision
      value: 98.4766139657444
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: est_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (est_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.24505928853755
    - type: f1
      value: 95.27417027417027
    - type: main_score
      value: 95.27417027417027
    - type: precision
      value: 94.84107378129117
    - type: recall
      value: 96.24505928853755
    task:
      type: BitextMining
  - dataset:
      config: hye_Armn-rus_Cyrl
      name: MTEB FloresBitextMining (hye_Armn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.67786561264822
    - type: main_score
      value: 97.67786561264822
    - type: precision
      value: 97.55839022637441
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: kmb_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kmb_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 46.047430830039524
    - type: f1
      value: 42.94464804804471
    - type: main_score
      value: 42.94464804804471
    - type: precision
      value: 41.9851895607238
    - type: recall
      value: 46.047430830039524
    task:
      type: BitextMining
  - dataset:
      config: min_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (min_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 3.9525691699604746
    - type: f1
      value: 3.402665192725756
    - type: main_score
      value: 3.402665192725756
    - type: precision
      value: 3.303787557740127
    - type: recall
      value: 3.9525691699604746
    task:
      type: BitextMining
  - dataset:
      config: pol_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (pol_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.60474308300395
    - type: f1
      value: 99.4729907773386
    - type: main_score
      value: 99.4729907773386
    - type: precision
      value: 99.40711462450594
    - type: recall
      value: 99.60474308300395
    task:
      type: BitextMining
  - dataset:
      config: ssw_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ssw_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 73.22134387351778
    - type: f1
      value: 70.43086049508975
    - type: main_score
      value: 70.43086049508975
    - type: precision
      value: 69.35312022355656
    - type: recall
      value: 73.22134387351778
    task:
      type: BitextMining
  - dataset:
      config: ukr_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (ukr_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.90118577075098
    - type: f1
      value: 99.86824769433464
    - type: main_score
      value: 99.86824769433464
    - type: precision
      value: 99.85177865612648
    - type: recall
      value: 99.90118577075098
    task:
      type: BitextMining
  - dataset:
      config: afr_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (afr_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.9459815546772
    - type: main_score
      value: 98.9459815546772
    - type: precision
      value: 98.81422924901186
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: bho_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (bho_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.0711462450593
    - type: f1
      value: 93.12182382834557
    - type: main_score
      value: 93.12182382834557
    - type: precision
      value: 92.7523453232338
    - type: recall
      value: 94.0711462450593
    task:
      type: BitextMining
  - dataset:
      config: eus_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (eus_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.19367588932806
    - type: f1
      value: 91.23604975587072
    - type: main_score
      value: 91.23604975587072
    - type: precision
      value: 90.86697443588663
    - type: recall
      value: 92.19367588932806
    task:
      type: BitextMining
  - dataset:
      config: ibo_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ibo_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 82.21343873517787
    - type: f1
      value: 80.17901604858126
    - type: main_score
      value: 80.17901604858126
    - type: precision
      value: 79.3792284780028
    - type: recall
      value: 82.21343873517787
    task:
      type: BitextMining
  - dataset:
      config: kmr_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kmr_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 68.67588932806325
    - type: f1
      value: 66.72311714750278
    - type: main_score
      value: 66.72311714750278
    - type: precision
      value: 66.00178401554004
    - type: recall
      value: 68.67588932806325
    task:
      type: BitextMining
  - dataset:
      config: min_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (min_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 78.65612648221344
    - type: f1
      value: 76.26592719972166
    - type: main_score
      value: 76.26592719972166
    - type: precision
      value: 75.39980459997484
    - type: recall
      value: 78.65612648221344
    task:
      type: BitextMining
  - dataset:
      config: por_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (por_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.83794466403161
    - type: f1
      value: 95.9669678147939
    - type: main_score
      value: 95.9669678147939
    - type: precision
      value: 95.59453227931488
    - type: recall
      value: 96.83794466403161
    task:
      type: BitextMining
  - dataset:
      config: sun_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (sun_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.4901185770751
    - type: f1
      value: 91.66553983773662
    - type: main_score
      value: 91.66553983773662
    - type: precision
      value: 91.34530928009188
    - type: recall
      value: 92.4901185770751
    task:
      type: BitextMining
  - dataset:
      config: umb_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (umb_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 41.00790513833992
    - type: f1
      value: 38.21319326004483
    - type: main_score
      value: 38.21319326004483
    - type: precision
      value: 37.200655467675546
    - type: recall
      value: 41.00790513833992
    task:
      type: BitextMining
  - dataset:
      config: ajp_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (ajp_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.35573122529645
    - type: f1
      value: 93.97233201581028
    - type: main_score
      value: 93.97233201581028
    - type: precision
      value: 93.33333333333333
    - type: recall
      value: 95.35573122529645
    task:
      type: BitextMining
  - dataset:
      config: bjn_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (bjn_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 3.6561264822134385
    - type: f1
      value: 3.1071978056336484
    - type: main_score
      value: 3.1071978056336484
    - type: precision
      value: 3.0039741229718215
    - type: recall
      value: 3.6561264822134385
    task:
      type: BitextMining
  - dataset:
      config: ewe_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ewe_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 62.845849802371546
    - type: f1
      value: 59.82201175670472
    - type: main_score
      value: 59.82201175670472
    - type: precision
      value: 58.72629236362003
    - type: recall
      value: 62.845849802371546
    task:
      type: BitextMining
  - dataset:
      config: ilo_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ilo_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 83.10276679841897
    - type: f1
      value: 80.75065288987582
    - type: main_score
      value: 80.75065288987582
    - type: precision
      value: 79.80726451662179
    - type: recall
      value: 83.10276679841897
    task:
      type: BitextMining
  - dataset:
      config: knc_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (knc_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 10.079051383399209
    - type: f1
      value: 8.759282456080921
    - type: main_score
      value: 8.759282456080921
    - type: precision
      value: 8.474735138956142
    - type: recall
      value: 10.079051383399209
    task:
      type: BitextMining
  - dataset:
      config: mkd_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (mkd_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.55072463768116
    - type: main_score
      value: 98.55072463768116
    - type: precision
      value: 98.36956521739131
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: prs_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (prs_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.68247694334651
    - type: main_score
      value: 98.68247694334651
    - type: precision
      value: 98.51778656126481
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: swe_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (swe_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.22595520421606
    - type: main_score
      value: 99.22595520421606
    - type: precision
      value: 99.14361001317523
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: urd_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (urd_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.82608695652173
    - type: f1
      value: 97.25625823451911
    - type: main_score
      value: 97.25625823451911
    - type: precision
      value: 97.03063241106719
    - type: recall
      value: 97.82608695652173
    task:
      type: BitextMining
  - dataset:
      config: aka_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (aka_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.22529644268775
    - type: f1
      value: 77.94307687941227
    - type: main_score
      value: 77.94307687941227
    - type: precision
      value: 76.58782793293665
    - type: recall
      value: 81.22529644268775
    task:
      type: BitextMining
  - dataset:
      config: bjn_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (bjn_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 85.27667984189723
    - type: f1
      value: 83.6869192829922
    - type: main_score
      value: 83.6869192829922
    - type: precision
      value: 83.08670670691656
    - type: recall
      value: 85.27667984189723
    task:
      type: BitextMining
  - dataset:
      config: fao_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (fao_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 80.9288537549407
    - type: f1
      value: 79.29806087454745
    - type: main_score
      value: 79.29806087454745
    - type: precision
      value: 78.71445871526987
    - type: recall
      value: 80.9288537549407
    task:
      type: BitextMining
  - dataset:
      config: ind_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ind_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.12252964426878
    - type: f1
      value: 97.5296442687747
    - type: main_score
      value: 97.5296442687747
    - type: precision
      value: 97.23320158102767
    - type: recall
      value: 98.12252964426878
    task:
      type: BitextMining
  - dataset:
      config: knc_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (knc_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 33.49802371541502
    - type: f1
      value: 32.02378215033989
    - type: main_score
      value: 32.02378215033989
    - type: precision
      value: 31.511356103747406
    - type: recall
      value: 33.49802371541502
    task:
      type: BitextMining
  - dataset:
      config: mlt_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (mlt_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.40316205533597
    - type: f1
      value: 90.35317684386006
    - type: main_score
      value: 90.35317684386006
    - type: precision
      value: 89.94845939633488
    - type: recall
      value: 91.40316205533597
    task:
      type: BitextMining
  - dataset:
      config: quy_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (quy_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 40.612648221343875
    - type: f1
      value: 38.74337544712602
    - type: main_score
      value: 38.74337544712602
    - type: precision
      value: 38.133716022178575
    - type: recall
      value: 40.612648221343875
    task:
      type: BitextMining
  - dataset:
      config: swh_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (swh_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.13438735177866
    - type: f1
      value: 96.47435897435898
    - type: main_score
      value: 96.47435897435898
    - type: precision
      value: 96.18741765480895
    - type: recall
      value: 97.13438735177866
    task:
      type: BitextMining
  - dataset:
      config: uzn_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (uzn_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.83794466403161
    - type: f1
      value: 96.26355528529442
    - type: main_score
      value: 96.26355528529442
    - type: precision
      value: 96.0501756697409
    - type: recall
      value: 96.83794466403161
    task:
      type: BitextMining
  - dataset:
      config: als_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (als_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.6907114624506
    - type: main_score
      value: 98.6907114624506
    - type: precision
      value: 98.6142480707698
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: bod_Tibt-rus_Cyrl
      name: MTEB FloresBitextMining (bod_Tibt-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 1.0869565217391304
    - type: f1
      value: 0.9224649610442628
    - type: main_score
      value: 0.9224649610442628
    - type: precision
      value: 0.8894275740459898
    - type: recall
      value: 1.0869565217391304
    task:
      type: BitextMining
  - dataset:
      config: fij_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (fij_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 63.24110671936759
    - type: f1
      value: 60.373189068189525
    - type: main_score
      value: 60.373189068189525
    - type: precision
      value: 59.32326368115546
    - type: recall
      value: 63.24110671936759
    task:
      type: BitextMining
  - dataset:
      config: isl_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (isl_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 89.03162055335969
    - type: f1
      value: 87.3102634715907
    - type: main_score
      value: 87.3102634715907
    - type: precision
      value: 86.65991814698712
    - type: recall
      value: 89.03162055335969
    task:
      type: BitextMining
  - dataset:
      config: kon_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kon_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 73.91304347826086
    - type: f1
      value: 71.518235523573
    - type: main_score
      value: 71.518235523573
    - type: precision
      value: 70.58714102449801
    - type: recall
      value: 73.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: mni_Beng-rus_Cyrl
      name: MTEB FloresBitextMining (mni_Beng-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 29.545454545454547
    - type: f1
      value: 27.59513619889114
    - type: main_score
      value: 27.59513619889114
    - type: precision
      value: 26.983849851025344
    - type: recall
      value: 29.545454545454547
    task:
      type: BitextMining
  - dataset:
      config: ron_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ron_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.2094861660079
    - type: main_score
      value: 99.2094861660079
    - type: precision
      value: 99.1106719367589
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: szl_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (szl_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 86.26482213438736
    - type: f1
      value: 85.18912031587512
    - type: main_score
      value: 85.18912031587512
    - type: precision
      value: 84.77199409959775
    - type: recall
      value: 86.26482213438736
    task:
      type: BitextMining
  - dataset:
      config: vec_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (vec_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 85.67193675889328
    - type: f1
      value: 84.62529734716581
    - type: main_score
      value: 84.62529734716581
    - type: precision
      value: 84.2611422440705
    - type: recall
      value: 85.67193675889328
    task:
      type: BitextMining
  - dataset:
      config: amh_Ethi-rus_Cyrl
      name: MTEB FloresBitextMining (amh_Ethi-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.76284584980237
    - type: f1
      value: 93.91735076517685
    - type: main_score
      value: 93.91735076517685
    - type: precision
      value: 93.57553798858147
    - type: recall
      value: 94.76284584980237
    task:
      type: BitextMining
  - dataset:
      config: bos_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (bos_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 99.05655938264634
    - type: main_score
      value: 99.05655938264634
    - type: precision
      value: 99.01185770750988
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: fin_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (fin_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.43741765480895
    - type: main_score
      value: 97.43741765480895
    - type: precision
      value: 97.1590909090909
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: ita_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ita_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.70355731225297
    - type: f1
      value: 99.60474308300395
    - type: main_score
      value: 99.60474308300395
    - type: precision
      value: 99.55533596837944
    - type: recall
      value: 99.70355731225297
    task:
      type: BitextMining
  - dataset:
      config: kor_Hang-rus_Cyrl
      name: MTEB FloresBitextMining (kor_Hang-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.33201581027669
    - type: f1
      value: 96.49868247694334
    - type: main_score
      value: 96.49868247694334
    - type: precision
      value: 96.10507246376811
    - type: recall
      value: 97.33201581027669
    task:
      type: BitextMining
  - dataset:
      config: mos_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (mos_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 34.683794466403164
    - type: f1
      value: 32.766819308009076
    - type: main_score
      value: 32.766819308009076
    - type: precision
      value: 32.1637493670237
    - type: recall
      value: 34.683794466403164
    task:
      type: BitextMining
  - dataset:
      config: run_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (run_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 83.399209486166
    - type: f1
      value: 81.10578750604326
    - type: main_score
      value: 81.10578750604326
    - type: precision
      value: 80.16763162673529
    - type: recall
      value: 83.399209486166
    task:
      type: BitextMining
  - dataset:
      config: tam_Taml-rus_Cyrl
      name: MTEB FloresBitextMining (tam_Taml-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.41897233201581
    - type: f1
      value: 98.01548089591567
    - type: main_score
      value: 98.01548089591567
    - type: precision
      value: 97.84020327498588
    - type: recall
      value: 98.41897233201581
    task:
      type: BitextMining
  - dataset:
      config: vie_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (vie_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.1106719367589
    - type: f1
      value: 98.81422924901186
    - type: main_score
      value: 98.81422924901186
    - type: precision
      value: 98.66600790513834
    - type: recall
      value: 99.1106719367589
    task:
      type: BitextMining
  - dataset:
      config: apc_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (apc_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.87351778656127
    - type: f1
      value: 92.10803689064558
    - type: main_score
      value: 92.10803689064558
    - type: precision
      value: 91.30434782608695
    - type: recall
      value: 93.87351778656127
    task:
      type: BitextMining
  - dataset:
      config: bug_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (bug_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 57.608695652173914
    - type: f1
      value: 54.95878654927162
    - type: main_score
      value: 54.95878654927162
    - type: precision
      value: 54.067987427805654
    - type: recall
      value: 57.608695652173914
    task:
      type: BitextMining
  - dataset:
      config: fon_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (fon_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 61.95652173913043
    - type: f1
      value: 58.06537275812945
    - type: main_score
      value: 58.06537275812945
    - type: precision
      value: 56.554057596959204
    - type: recall
      value: 61.95652173913043
    task:
      type: BitextMining
  - dataset:
      config: jav_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (jav_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.47826086956522
    - type: f1
      value: 92.4784405318002
    - type: main_score
      value: 92.4784405318002
    - type: precision
      value: 92.09168143201127
    - type: recall
      value: 93.47826086956522
    task:
      type: BitextMining
  - dataset:
      config: lao_Laoo-rus_Cyrl
      name: MTEB FloresBitextMining (lao_Laoo-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.10671936758892
    - type: f1
      value: 89.76104922745239
    - type: main_score
      value: 89.76104922745239
    - type: precision
      value: 89.24754593232855
    - type: recall
      value: 91.10671936758892
    task:
      type: BitextMining
  - dataset:
      config: mri_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (mri_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 71.14624505928853
    - type: f1
      value: 68.26947125119062
    - type: main_score
      value: 68.26947125119062
    - type: precision
      value: 67.15942311051006
    - type: recall
      value: 71.14624505928853
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ace_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-ace_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 19.565217391304348
    - type: f1
      value: 16.321465000323805
    - type: main_score
      value: 16.321465000323805
    - type: precision
      value: 15.478527409347508
    - type: recall
      value: 19.565217391304348
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bam_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-bam_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 73.41897233201581
    - type: f1
      value: 68.77366228182746
    - type: main_score
      value: 68.77366228182746
    - type: precision
      value: 66.96012924273795
    - type: recall
      value: 73.41897233201581
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-dzo_Tibt
      name: MTEB FloresBitextMining (rus_Cyrl-dzo_Tibt)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 0.592885375494071
    - type: f1
      value: 0.02458062426370458
    - type: main_score
      value: 0.02458062426370458
    - type: precision
      value: 0.012824114724683876
    - type: recall
      value: 0.592885375494071
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hin_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-hin_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.90118577075098
    - type: f1
      value: 99.86824769433464
    - type: main_score
      value: 99.86824769433464
    - type: precision
      value: 99.85177865612648
    - type: recall
      value: 99.90118577075098
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-khm_Khmr
      name: MTEB FloresBitextMining (rus_Cyrl-khm_Khmr)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.13438735177866
    - type: f1
      value: 96.24505928853755
    - type: main_score
      value: 96.24505928853755
    - type: precision
      value: 95.81686429512516
    - type: recall
      value: 97.13438735177866
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mag_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-mag_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.50592885375494
    - type: f1
      value: 99.35770750988142
    - type: main_score
      value: 99.35770750988142
    - type: precision
      value: 99.29183135704875
    - type: recall
      value: 99.50592885375494
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-pap_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-pap_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.93675889328063
    - type: f1
      value: 96.05072463768116
    - type: main_score
      value: 96.05072463768116
    - type: precision
      value: 95.66040843214758
    - type: recall
      value: 96.93675889328063
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-sot_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-sot_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.67588932806325
    - type: f1
      value: 91.7786561264822
    - type: main_score
      value: 91.7786561264822
    - type: precision
      value: 90.91238471673255
    - type: recall
      value: 93.67588932806325
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tur_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-tur_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.68247694334651
    - type: main_score
      value: 98.68247694334651
    - type: precision
      value: 98.51778656126481
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ace_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ace_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 74.1106719367589
    - type: f1
      value: 70.21737923911836
    - type: main_score
      value: 70.21737923911836
    - type: precision
      value: 68.7068791410511
    - type: recall
      value: 74.1106719367589
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ban_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ban_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.7193675889328
    - type: f1
      value: 78.76470334510617
    - type: main_score
      value: 78.76470334510617
    - type: precision
      value: 77.76208475761422
    - type: recall
      value: 81.7193675889328
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ell_Grek
      name: MTEB FloresBitextMining (rus_Cyrl-ell_Grek)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.3201581027668
    - type: f1
      value: 97.76021080368908
    - type: main_score
      value: 97.76021080368908
    - type: precision
      value: 97.48023715415019
    - type: recall
      value: 98.3201581027668
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hne_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-hne_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.51778656126481
    - type: f1
      value: 98.0566534914361
    - type: main_score
      value: 98.0566534914361
    - type: precision
      value: 97.82608695652173
    - type: recall
      value: 98.51778656126481
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kik_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kik_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 80.73122529644269
    - type: f1
      value: 76.42689244220864
    - type: main_score
      value: 76.42689244220864
    - type: precision
      value: 74.63877909530083
    - type: recall
      value: 80.73122529644269
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mai_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-mai_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.56719367588933
    - type: main_score
      value: 98.56719367588933
    - type: precision
      value: 98.40250329380763
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-pbt_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-pbt_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.5296442687747
    - type: f1
      value: 96.73913043478261
    - type: main_score
      value: 96.73913043478261
    - type: precision
      value: 96.36034255599473
    - type: recall
      value: 97.5296442687747
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-spa_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-spa_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.20948616600789
    - type: main_score
      value: 99.20948616600789
    - type: precision
      value: 99.1106719367589
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-twi_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-twi_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 82.01581027667984
    - type: f1
      value: 78.064787822953
    - type: main_score
      value: 78.064787822953
    - type: precision
      value: 76.43272186750448
    - type: recall
      value: 82.01581027667984
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-acm_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-acm_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.3201581027668
    - type: f1
      value: 97.76021080368908
    - type: main_score
      value: 97.76021080368908
    - type: precision
      value: 97.48023715415019
    - type: recall
      value: 98.3201581027668
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bel_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-bel_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.22134387351778
    - type: f1
      value: 97.67786561264822
    - type: main_score
      value: 97.67786561264822
    - type: precision
      value: 97.4308300395257
    - type: recall
      value: 98.22134387351778
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-eng_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-eng_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.70355731225297
    - type: f1
      value: 99.60474308300395
    - type: main_score
      value: 99.60474308300395
    - type: precision
      value: 99.55533596837944
    - type: recall
      value: 99.70355731225297
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hrv_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-hrv_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.1106719367589
    - type: f1
      value: 98.83069828722002
    - type: main_score
      value: 98.83069828722002
    - type: precision
      value: 98.69894598155466
    - type: recall
      value: 99.1106719367589
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kin_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kin_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.37944664031622
    - type: f1
      value: 91.53162055335969
    - type: main_score
      value: 91.53162055335969
    - type: precision
      value: 90.71475625823452
    - type: recall
      value: 93.37944664031622
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mal_Mlym
      name: MTEB FloresBitextMining (rus_Cyrl-mal_Mlym)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.07773386034255
    - type: main_score
      value: 99.07773386034255
    - type: precision
      value: 98.96245059288538
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-pes_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-pes_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.30368906455863
    - type: main_score
      value: 98.30368906455863
    - type: precision
      value: 98.10606060606061
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-srd_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-srd_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 89.03162055335969
    - type: f1
      value: 86.11048371917937
    - type: main_score
      value: 86.11048371917937
    - type: precision
      value: 84.86001317523056
    - type: recall
      value: 89.03162055335969
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tzm_Tfng
      name: MTEB FloresBitextMining (rus_Cyrl-tzm_Tfng)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 12.351778656126482
    - type: f1
      value: 10.112177999067715
    - type: main_score
      value: 10.112177999067715
    - type: precision
      value: 9.53495885438645
    - type: recall
      value: 12.351778656126482
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-acq_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-acq_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.55072463768116
    - type: main_score
      value: 98.55072463768116
    - type: precision
      value: 98.36956521739131
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bem_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-bem_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 73.22134387351778
    - type: f1
      value: 68.30479412989295
    - type: main_score
      value: 68.30479412989295
    - type: precision
      value: 66.40073447632736
    - type: recall
      value: 73.22134387351778
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-epo_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-epo_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.1106719367589
    - type: f1
      value: 98.81422924901186
    - type: main_score
      value: 98.81422924901186
    - type: precision
      value: 98.66600790513834
    - type: recall
      value: 99.1106719367589
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hun_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-hun_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.83794466403161
    - type: f1
      value: 95.88274044795784
    - type: main_score
      value: 95.88274044795784
    - type: precision
      value: 95.45454545454545
    - type: recall
      value: 96.83794466403161
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kir_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-kir_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.34387351778656
    - type: f1
      value: 95.49280429715212
    - type: main_score
      value: 95.49280429715212
    - type: precision
      value: 95.14163372859026
    - type: recall
      value: 96.34387351778656
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mar_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-mar_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.28722002635047
    - type: main_score
      value: 98.28722002635047
    - type: precision
      value: 98.07312252964427
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-plt_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-plt_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 88.04347826086956
    - type: f1
      value: 85.14328063241106
    - type: main_score
      value: 85.14328063241106
    - type: precision
      value: 83.96339168078298
    - type: recall
      value: 88.04347826086956
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-srp_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-srp_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.2094861660079
    - type: main_score
      value: 99.2094861660079
    - type: precision
      value: 99.1106719367589
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-uig_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-uig_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.19367588932806
    - type: f1
      value: 89.98541313758706
    - type: main_score
      value: 89.98541313758706
    - type: precision
      value: 89.01021080368906
    - type: recall
      value: 92.19367588932806
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-aeb_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-aeb_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.8498023715415
    - type: f1
      value: 94.63109354413703
    - type: main_score
      value: 94.63109354413703
    - type: precision
      value: 94.05467720685111
    - type: recall
      value: 95.8498023715415
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ben_Beng
      name: MTEB FloresBitextMining (rus_Cyrl-ben_Beng)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.2094861660079
    - type: main_score
      value: 99.2094861660079
    - type: precision
      value: 99.1106719367589
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-est_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-est_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.55335968379447
    - type: f1
      value: 94.2588932806324
    - type: main_score
      value: 94.2588932806324
    - type: precision
      value: 93.65118577075098
    - type: recall
      value: 95.55335968379447
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hye_Armn
      name: MTEB FloresBitextMining (rus_Cyrl-hye_Armn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.28722002635045
    - type: main_score
      value: 98.28722002635045
    - type: precision
      value: 98.07312252964427
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kmb_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kmb_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 54.24901185770751
    - type: f1
      value: 49.46146674116913
    - type: main_score
      value: 49.46146674116913
    - type: precision
      value: 47.81033799314432
    - type: recall
      value: 54.24901185770751
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-min_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-min_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 15.810276679841898
    - type: f1
      value: 13.271207641419332
    - type: main_score
      value: 13.271207641419332
    - type: precision
      value: 12.510673148766033
    - type: recall
      value: 15.810276679841898
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-pol_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-pol_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.32674571805006
    - type: main_score
      value: 98.32674571805006
    - type: precision
      value: 98.14723320158103
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ssw_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ssw_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 80.8300395256917
    - type: f1
      value: 76.51717847370023
    - type: main_score
      value: 76.51717847370023
    - type: precision
      value: 74.74143610013175
    - type: recall
      value: 80.8300395256917
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ukr_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-ukr_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.60474308300395
    - type: f1
      value: 99.4729907773386
    - type: main_score
      value: 99.4729907773386
    - type: precision
      value: 99.40711462450594
    - type: recall
      value: 99.60474308300395
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-afr_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-afr_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.1106719367589
    - type: f1
      value: 98.81422924901186
    - type: main_score
      value: 98.81422924901186
    - type: precision
      value: 98.66600790513834
    - type: recall
      value: 99.1106719367589
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bho_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-bho_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.6403162055336
    - type: f1
      value: 95.56982872200265
    - type: main_score
      value: 95.56982872200265
    - type: precision
      value: 95.0592885375494
    - type: recall
      value: 96.6403162055336
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-eus_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-eus_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.62845849802372
    - type: f1
      value: 96.9038208168643
    - type: main_score
      value: 96.9038208168643
    - type: precision
      value: 96.55797101449275
    - type: recall
      value: 97.62845849802372
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ibo_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ibo_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 89.2292490118577
    - type: f1
      value: 86.35234330886506
    - type: main_score
      value: 86.35234330886506
    - type: precision
      value: 85.09881422924902
    - type: recall
      value: 89.2292490118577
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kmr_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kmr_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 83.49802371541502
    - type: f1
      value: 79.23630717108978
    - type: main_score
      value: 79.23630717108978
    - type: precision
      value: 77.48188405797102
    - type: recall
      value: 83.49802371541502
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-min_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-min_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 79.34782608695652
    - type: f1
      value: 75.31689928429059
    - type: main_score
      value: 75.31689928429059
    - type: precision
      value: 73.91519410541149
    - type: recall
      value: 79.34782608695652
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-por_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-por_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.54150197628458
    - type: f1
      value: 95.53218520609825
    - type: main_score
      value: 95.53218520609825
    - type: precision
      value: 95.07575757575756
    - type: recall
      value: 96.54150197628458
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-sun_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-sun_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.2806324110672
    - type: f1
      value: 91.56973461321287
    - type: main_score
      value: 91.56973461321287
    - type: precision
      value: 90.84396334890405
    - type: recall
      value: 93.2806324110672
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-umb_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-umb_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 51.87747035573123
    - type: f1
      value: 46.36591778884269
    - type: main_score
      value: 46.36591778884269
    - type: precision
      value: 44.57730391234227
    - type: recall
      value: 51.87747035573123
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ajp_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-ajp_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.30368906455863
    - type: main_score
      value: 98.30368906455863
    - type: precision
      value: 98.10606060606061
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bjn_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-bjn_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 14.82213438735178
    - type: f1
      value: 12.365434276616856
    - type: main_score
      value: 12.365434276616856
    - type: precision
      value: 11.802079517180589
    - type: recall
      value: 14.82213438735178
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ewe_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ewe_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 71.44268774703558
    - type: f1
      value: 66.74603174603175
    - type: main_score
      value: 66.74603174603175
    - type: precision
      value: 64.99933339607253
    - type: recall
      value: 71.44268774703558
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ilo_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ilo_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 85.86956521739131
    - type: f1
      value: 83.00139015960917
    - type: main_score
      value: 83.00139015960917
    - type: precision
      value: 81.91411396574439
    - type: recall
      value: 85.86956521739131
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-knc_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-knc_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 14.525691699604742
    - type: f1
      value: 12.618283715726806
    - type: main_score
      value: 12.618283715726806
    - type: precision
      value: 12.048458493742352
    - type: recall
      value: 14.525691699604742
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mkd_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-mkd_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.22595520421606
    - type: main_score
      value: 99.22595520421606
    - type: precision
      value: 99.14361001317523
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-prs_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-prs_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.07773386034255
    - type: main_score
      value: 99.07773386034255
    - type: precision
      value: 98.96245059288538
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-swe_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-swe_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.07773386034256
    - type: main_score
      value: 99.07773386034256
    - type: precision
      value: 98.96245059288538
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-urd_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-urd_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.61660079051383
    - type: f1
      value: 98.15546772068511
    - type: main_score
      value: 98.15546772068511
    - type: precision
      value: 97.92490118577075
    - type: recall
      value: 98.61660079051383
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-aka_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-aka_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.02766798418972
    - type: f1
      value: 76.73277809147375
    - type: main_score
      value: 76.73277809147375
    - type: precision
      value: 74.97404165882426
    - type: recall
      value: 81.02766798418972
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bjn_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-bjn_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 86.7588932806324
    - type: f1
      value: 83.92064566965753
    - type: main_score
      value: 83.92064566965753
    - type: precision
      value: 82.83734079929732
    - type: recall
      value: 86.7588932806324
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fao_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-fao_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 88.43873517786561
    - type: f1
      value: 85.48136645962732
    - type: main_score
      value: 85.48136645962732
    - type: precision
      value: 84.23418972332016
    - type: recall
      value: 88.43873517786561
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ind_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ind_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.68247694334651
    - type: main_score
      value: 98.68247694334651
    - type: precision
      value: 98.51778656126481
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-knc_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-knc_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 45.8498023715415
    - type: f1
      value: 40.112030865489366
    - type: main_score
      value: 40.112030865489366
    - type: precision
      value: 38.28262440050776
    - type: recall
      value: 45.8498023715415
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mlt_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-mlt_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.18181818181817
    - type: f1
      value: 91.30787690570298
    - type: main_score
      value: 91.30787690570298
    - type: precision
      value: 90.4983060417843
    - type: recall
      value: 93.18181818181817
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-quy_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-quy_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 62.450592885375485
    - type: f1
      value: 57.28742975628178
    - type: main_score
      value: 57.28742975628178
    - type: precision
      value: 55.56854987623269
    - type: recall
      value: 62.450592885375485
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-swh_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-swh_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.3201581027668
    - type: f1
      value: 97.77667984189723
    - type: main_score
      value: 97.77667984189723
    - type: precision
      value: 97.51317523056655
    - type: recall
      value: 98.3201581027668
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-uzn_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-uzn_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.12252964426878
    - type: f1
      value: 97.59081498211933
    - type: main_score
      value: 97.59081498211933
    - type: precision
      value: 97.34848484848484
    - type: recall
      value: 98.12252964426878
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-als_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-als_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.09420289855073
    - type: main_score
      value: 99.09420289855073
    - type: precision
      value: 98.99538866930172
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bod_Tibt
      name: MTEB FloresBitextMining (rus_Cyrl-bod_Tibt)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 11.561264822134387
    - type: f1
      value: 8.121312045385636
    - type: main_score
      value: 8.121312045385636
    - type: precision
      value: 7.350577020893972
    - type: recall
      value: 11.561264822134387
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fij_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-fij_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 72.23320158102767
    - type: f1
      value: 67.21000233846082
    - type: main_score
      value: 67.21000233846082
    - type: precision
      value: 65.3869439739005
    - type: recall
      value: 72.23320158102767
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-isl_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-isl_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.99604743083005
    - type: f1
      value: 89.75955204216073
    - type: main_score
      value: 89.75955204216073
    - type: precision
      value: 88.7598814229249
    - type: recall
      value: 91.99604743083005
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kon_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kon_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.81818181818183
    - type: f1
      value: 77.77800098452272
    - type: main_score
      value: 77.77800098452272
    - type: precision
      value: 76.1521268586486
    - type: recall
      value: 81.81818181818183
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mni_Beng
      name: MTEB FloresBitextMining (rus_Cyrl-mni_Beng)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 54.74308300395256
    - type: f1
      value: 48.97285299254615
    - type: main_score
      value: 48.97285299254615
    - type: precision
      value: 46.95125742968299
    - type: recall
      value: 54.74308300395256
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ron_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ron_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.22134387351778
    - type: f1
      value: 97.64492753623189
    - type: main_score
      value: 97.64492753623189
    - type: precision
      value: 97.36495388669302
    - type: recall
      value: 98.22134387351778
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-szl_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-szl_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.09486166007905
    - type: f1
      value: 90.10375494071147
    - type: main_score
      value: 90.10375494071147
    - type: precision
      value: 89.29606625258798
    - type: recall
      value: 92.09486166007905
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-vec_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-vec_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.4901185770751
    - type: f1
      value: 90.51430453604365
    - type: main_score
      value: 90.51430453604365
    - type: precision
      value: 89.69367588932808
    - type: recall
      value: 92.4901185770751
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-amh_Ethi
      name: MTEB FloresBitextMining (rus_Cyrl-amh_Ethi)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.82608695652173
    - type: f1
      value: 97.11791831357048
    - type: main_score
      value: 97.11791831357048
    - type: precision
      value: 96.77206851119894
    - type: recall
      value: 97.82608695652173
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bos_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-bos_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.55072463768116
    - type: main_score
      value: 98.55072463768116
    - type: precision
      value: 98.36956521739131
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fin_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-fin_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.65217391304348
    - type: f1
      value: 94.4235836627141
    - type: main_score
      value: 94.4235836627141
    - type: precision
      value: 93.84881422924902
    - type: recall
      value: 95.65217391304348
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ita_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ita_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.55072463768117
    - type: main_score
      value: 98.55072463768117
    - type: precision
      value: 98.36956521739131
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kor_Hang
      name: MTEB FloresBitextMining (rus_Cyrl-kor_Hang)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.55335968379447
    - type: f1
      value: 94.15349143610013
    - type: main_score
      value: 94.15349143610013
    - type: precision
      value: 93.49472990777339
    - type: recall
      value: 95.55335968379447
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mos_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-mos_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 43.67588932806324
    - type: f1
      value: 38.84849721190082
    - type: main_score
      value: 38.84849721190082
    - type: precision
      value: 37.43294462099682
    - type: recall
      value: 43.67588932806324
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-run_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-run_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 90.21739130434783
    - type: f1
      value: 87.37483530961792
    - type: main_score
      value: 87.37483530961792
    - type: precision
      value: 86.07872200263506
    - type: recall
      value: 90.21739130434783
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tam_Taml
      name: MTEB FloresBitextMining (rus_Cyrl-tam_Taml)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.2094861660079
    - type: main_score
      value: 99.2094861660079
    - type: precision
      value: 99.1106719367589
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-vie_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-vie_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.03557312252964
    - type: f1
      value: 96.13636363636364
    - type: main_score
      value: 96.13636363636364
    - type: precision
      value: 95.70981554677206
    - type: recall
      value: 97.03557312252964
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-apc_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-apc_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.12252964426878
    - type: f1
      value: 97.49670619235836
    - type: main_score
      value: 97.49670619235836
    - type: precision
      value: 97.18379446640316
    - type: recall
      value: 98.12252964426878
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bug_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-bug_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 67.29249011857708
    - type: f1
      value: 62.09268717667927
    - type: main_score
      value: 62.09268717667927
    - type: precision
      value: 60.28554009748714
    - type: recall
      value: 67.29249011857708
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fon_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-fon_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 63.43873517786561
    - type: f1
      value: 57.66660107569199
    - type: main_score
      value: 57.66660107569199
    - type: precision
      value: 55.66676396919363
    - type: recall
      value: 63.43873517786561
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-jav_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-jav_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.46640316205533
    - type: f1
      value: 92.89384528514964
    - type: main_score
      value: 92.89384528514964
    - type: precision
      value: 92.19367588932806
    - type: recall
      value: 94.46640316205533
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lao_Laoo
      name: MTEB FloresBitextMining (rus_Cyrl-lao_Laoo)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.23320158102767
    - type: f1
      value: 96.40974967061922
    - type: main_score
      value: 96.40974967061922
    - type: precision
      value: 96.034255599473
    - type: recall
      value: 97.23320158102767
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mri_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-mri_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 76.77865612648222
    - type: f1
      value: 73.11286539547409
    - type: main_score
      value: 73.11286539547409
    - type: precision
      value: 71.78177214337046
    - type: recall
      value: 76.77865612648222
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-taq_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-taq_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 41.99604743083004
    - type: f1
      value: 37.25127063318763
    - type: main_score
      value: 37.25127063318763
    - type: precision
      value: 35.718929186985726
    - type: recall
      value: 41.99604743083004
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-war_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-war_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.55335968379447
    - type: f1
      value: 94.1699604743083
    - type: main_score
      value: 94.1699604743083
    - type: precision
      value: 93.52766798418972
    - type: recall
      value: 95.55335968379447
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-arb_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-arb_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.60474308300395
    - type: f1
      value: 99.4729907773386
    - type: main_score
      value: 99.4729907773386
    - type: precision
      value: 99.40711462450594
    - type: recall
      value: 99.60474308300395
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bul_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-bul_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.70355731225297
    - type: f1
      value: 99.60474308300395
    - type: main_score
      value: 99.60474308300395
    - type: precision
      value: 99.55533596837944
    - type: recall
      value: 99.70355731225297
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fra_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-fra_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.60474308300395
    - type: f1
      value: 99.47299077733861
    - type: main_score
      value: 99.47299077733861
    - type: precision
      value: 99.40711462450594
    - type: recall
      value: 99.60474308300395
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-jpn_Jpan
      name: MTEB FloresBitextMining (rus_Cyrl-jpn_Jpan)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.44268774703558
    - type: f1
      value: 95.30632411067194
    - type: main_score
      value: 95.30632411067194
    - type: precision
      value: 94.76284584980237
    - type: recall
      value: 96.44268774703558
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lij_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lij_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 90.21739130434783
    - type: f1
      value: 87.4703557312253
    - type: main_score
      value: 87.4703557312253
    - type: precision
      value: 86.29611330698287
    - type: recall
      value: 90.21739130434783
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mya_Mymr
      name: MTEB FloresBitextMining (rus_Cyrl-mya_Mymr)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.364953886693
    - type: main_score
      value: 97.364953886693
    - type: precision
      value: 97.03557312252964
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-sag_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-sag_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 54.841897233201585
    - type: f1
      value: 49.61882037503349
    - type: main_score
      value: 49.61882037503349
    - type: precision
      value: 47.831968755881796
    - type: recall
      value: 54.841897233201585
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-taq_Tfng
      name: MTEB FloresBitextMining (rus_Cyrl-taq_Tfng)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 15.316205533596838
    - type: f1
      value: 11.614836360389717
    - type: main_score
      value: 11.614836360389717
    - type: precision
      value: 10.741446193235223
    - type: recall
      value: 15.316205533596838
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-wol_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-wol_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 67.88537549407114
    - type: f1
      value: 62.2536417249856
    - type: main_score
      value: 62.2536417249856
    - type: precision
      value: 60.27629128666678
    - type: recall
      value: 67.88537549407114
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-arb_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-arb_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 27.766798418972332
    - type: f1
      value: 23.39674889624077
    - type: main_score
      value: 23.39674889624077
    - type: precision
      value: 22.28521155585345
    - type: recall
      value: 27.766798418972332
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-cat_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-cat_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.23320158102767
    - type: f1
      value: 96.42151326933936
    - type: main_score
      value: 96.42151326933936
    - type: precision
      value: 96.04743083003953
    - type: recall
      value: 97.23320158102767
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fur_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-fur_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 88.63636363636364
    - type: f1
      value: 85.80792396009788
    - type: main_score
      value: 85.80792396009788
    - type: precision
      value: 84.61508901726293
    - type: recall
      value: 88.63636363636364
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kab_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kab_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 48.12252964426877
    - type: f1
      value: 43.05387582971066
    - type: main_score
      value: 43.05387582971066
    - type: precision
      value: 41.44165117538212
    - type: recall
      value: 48.12252964426877
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lim_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lim_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.81818181818183
    - type: f1
      value: 77.81676163099087
    - type: main_score
      value: 77.81676163099087
    - type: precision
      value: 76.19565217391305
    - type: recall
      value: 81.81818181818183
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-nld_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-nld_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.33201581027669
    - type: f1
      value: 96.4756258234519
    - type: main_score
      value: 96.4756258234519
    - type: precision
      value: 96.06389986824769
    - type: recall
      value: 97.33201581027669
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-san_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-san_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.47826086956522
    - type: f1
      value: 91.70289855072463
    - type: main_score
      value: 91.70289855072463
    - type: precision
      value: 90.9370882740448
    - type: recall
      value: 93.47826086956522
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tat_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-tat_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.72727272727273
    - type: f1
      value: 97.00263504611331
    - type: main_score
      value: 97.00263504611331
    - type: precision
      value: 96.65678524374177
    - type: recall
      value: 97.72727272727273
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-xho_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-xho_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.08300395256917
    - type: f1
      value: 91.12977602108036
    - type: main_score
      value: 91.12977602108036
    - type: precision
      value: 90.22562582345192
    - type: recall
      value: 93.08300395256917
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ars_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-ars_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.40711462450594
    - type: f1
      value: 99.2094861660079
    - type: main_score
      value: 99.2094861660079
    - type: precision
      value: 99.1106719367589
    - type: recall
      value: 99.40711462450594
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ceb_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ceb_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.65217391304348
    - type: f1
      value: 94.3544137022398
    - type: main_score
      value: 94.3544137022398
    - type: precision
      value: 93.76646903820817
    - type: recall
      value: 95.65217391304348
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fuv_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-fuv_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 51.18577075098815
    - type: f1
      value: 44.5990252610806
    - type: main_score
      value: 44.5990252610806
    - type: precision
      value: 42.34331599450177
    - type: recall
      value: 51.18577075098815
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kac_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kac_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 46.93675889328063
    - type: f1
      value: 41.79004018701787
    - type: main_score
      value: 41.79004018701787
    - type: precision
      value: 40.243355662392624
    - type: recall
      value: 46.93675889328063
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lin_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lin_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.50197628458498
    - type: f1
      value: 89.1205533596838
    - type: main_score
      value: 89.1205533596838
    - type: precision
      value: 88.07147562582345
    - type: recall
      value: 91.50197628458498
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-nno_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-nno_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.81422924901186
    - type: f1
      value: 98.41897233201581
    - type: main_score
      value: 98.41897233201581
    - type: precision
      value: 98.22134387351778
    - type: recall
      value: 98.81422924901186
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-sat_Olck
      name: MTEB FloresBitextMining (rus_Cyrl-sat_Olck)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 2.371541501976284
    - type: f1
      value: 1.0726274943087382
    - type: main_score
      value: 1.0726274943087382
    - type: precision
      value: 0.875279634748803
    - type: recall
      value: 2.371541501976284
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tel_Telu
      name: MTEB FloresBitextMining (rus_Cyrl-tel_Telu)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.68247694334651
    - type: main_score
      value: 98.68247694334651
    - type: precision
      value: 98.51778656126481
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ydd_Hebr
      name: MTEB FloresBitextMining (rus_Cyrl-ydd_Hebr)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 89.42687747035573
    - type: f1
      value: 86.47609636740073
    - type: main_score
      value: 86.47609636740073
    - type: precision
      value: 85.13669301712781
    - type: recall
      value: 89.42687747035573
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ary_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-ary_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 89.82213438735178
    - type: f1
      value: 87.04545454545456
    - type: main_score
      value: 87.04545454545456
    - type: precision
      value: 85.76910408432148
    - type: recall
      value: 89.82213438735178
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ces_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ces_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.9459815546772
    - type: main_score
      value: 98.9459815546772
    - type: precision
      value: 98.81422924901186
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-gaz_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-gaz_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 64.9209486166008
    - type: f1
      value: 58.697458119394874
    - type: main_score
      value: 58.697458119394874
    - type: precision
      value: 56.43402189597842
    - type: recall
      value: 64.9209486166008
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kam_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kam_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 59.18972332015811
    - type: f1
      value: 53.19031511966295
    - type: main_score
      value: 53.19031511966295
    - type: precision
      value: 51.08128357343655
    - type: recall
      value: 59.18972332015811
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lit_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lit_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.54150197628458
    - type: f1
      value: 95.5368906455863
    - type: main_score
      value: 95.5368906455863
    - type: precision
      value: 95.0592885375494
    - type: recall
      value: 96.54150197628458
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-nob_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-nob_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.12252964426878
    - type: f1
      value: 97.51317523056655
    - type: main_score
      value: 97.51317523056655
    - type: precision
      value: 97.2167325428195
    - type: recall
      value: 98.12252964426878
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-scn_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-scn_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 84.0909090909091
    - type: f1
      value: 80.37000439174352
    - type: main_score
      value: 80.37000439174352
    - type: precision
      value: 78.83994628559846
    - type: recall
      value: 84.0909090909091
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tgk_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-tgk_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.68774703557312
    - type: f1
      value: 90.86344814605684
    - type: main_score
      value: 90.86344814605684
    - type: precision
      value: 90.12516469038208
    - type: recall
      value: 92.68774703557312
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-yor_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-yor_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 72.13438735177866
    - type: f1
      value: 66.78759646150951
    - type: main_score
      value: 66.78759646150951
    - type: precision
      value: 64.85080192096002
    - type: recall
      value: 72.13438735177866
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-arz_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-arz_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.364953886693
    - type: main_score
      value: 97.364953886693
    - type: precision
      value: 97.03557312252964
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-cjk_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-cjk_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 51.976284584980235
    - type: f1
      value: 46.468762353149714
    - type: main_score
      value: 46.468762353149714
    - type: precision
      value: 44.64073366247278
    - type: recall
      value: 51.976284584980235
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-gla_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-gla_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 79.74308300395256
    - type: f1
      value: 75.55611165294958
    - type: main_score
      value: 75.55611165294958
    - type: precision
      value: 73.95033408620365
    - type: recall
      value: 79.74308300395256
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kan_Knda
      name: MTEB FloresBitextMining (rus_Cyrl-kan_Knda)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.96245059288538
    - type: main_score
      value: 98.96245059288538
    - type: precision
      value: 98.84716732542819
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lmo_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lmo_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 82.41106719367589
    - type: f1
      value: 78.56413514022209
    - type: main_score
      value: 78.56413514022209
    - type: precision
      value: 77.15313068573938
    - type: recall
      value: 82.41106719367589
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-npi_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-npi_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.3201581027668
    - type: main_score
      value: 98.3201581027668
    - type: precision
      value: 98.12252964426878
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-shn_Mymr
      name: MTEB FloresBitextMining (rus_Cyrl-shn_Mymr)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 57.11462450592886
    - type: f1
      value: 51.51361369197337
    - type: main_score
      value: 51.51361369197337
    - type: precision
      value: 49.71860043649573
    - type: recall
      value: 57.11462450592886
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tgl_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-tgl_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.82608695652173
    - type: f1
      value: 97.18379446640316
    - type: main_score
      value: 97.18379446640316
    - type: precision
      value: 96.88735177865613
    - type: recall
      value: 97.82608695652173
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-yue_Hant
      name: MTEB FloresBitextMining (rus_Cyrl-yue_Hant)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.09420289855072
    - type: main_score
      value: 99.09420289855072
    - type: precision
      value: 98.9953886693017
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-asm_Beng
      name: MTEB FloresBitextMining (rus_Cyrl-asm_Beng)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.55335968379447
    - type: f1
      value: 94.16007905138339
    - type: main_score
      value: 94.16007905138339
    - type: precision
      value: 93.50296442687747
    - type: recall
      value: 95.55335968379447
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ckb_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-ckb_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.88537549407114
    - type: f1
      value: 90.76745718050066
    - type: main_score
      value: 90.76745718050066
    - type: precision
      value: 89.80072463768116
    - type: recall
      value: 92.88537549407114
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-gle_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-gle_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.699604743083
    - type: f1
      value: 89.40899680030115
    - type: main_score
      value: 89.40899680030115
    - type: precision
      value: 88.40085638998683
    - type: recall
      value: 91.699604743083
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kas_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-kas_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 88.3399209486166
    - type: f1
      value: 85.14351590438548
    - type: main_score
      value: 85.14351590438548
    - type: precision
      value: 83.72364953886692
    - type: recall
      value: 88.3399209486166
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ltg_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ltg_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 83.399209486166
    - type: f1
      value: 79.88408934061107
    - type: main_score
      value: 79.88408934061107
    - type: precision
      value: 78.53794509179885
    - type: recall
      value: 83.399209486166
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-nso_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-nso_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.20553359683794
    - type: f1
      value: 88.95406635525212
    - type: main_score
      value: 88.95406635525212
    - type: precision
      value: 88.01548089591567
    - type: recall
      value: 91.20553359683794
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-sin_Sinh
      name: MTEB FloresBitextMining (rus_Cyrl-sin_Sinh)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.56719367588933
    - type: main_score
      value: 98.56719367588933
    - type: precision
      value: 98.40250329380763
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tha_Thai
      name: MTEB FloresBitextMining (rus_Cyrl-tha_Thai)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.94861660079052
    - type: f1
      value: 94.66403162055336
    - type: main_score
      value: 94.66403162055336
    - type: precision
      value: 94.03820816864295
    - type: recall
      value: 95.94861660079052
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-zho_Hans
      name: MTEB FloresBitextMining (rus_Cyrl-zho_Hans)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.4308300395257
    - type: f1
      value: 96.5909090909091
    - type: main_score
      value: 96.5909090909091
    - type: precision
      value: 96.17918313570487
    - type: recall
      value: 97.4308300395257
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ast_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ast_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.46640316205533
    - type: f1
      value: 92.86890645586297
    - type: main_score
      value: 92.86890645586297
    - type: precision
      value: 92.14756258234519
    - type: recall
      value: 94.46640316205533
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-crh_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-crh_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.66403162055336
    - type: f1
      value: 93.2663592446201
    - type: main_score
      value: 93.2663592446201
    - type: precision
      value: 92.66716073781292
    - type: recall
      value: 94.66403162055336
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-glg_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-glg_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.81422924901186
    - type: f1
      value: 98.46837944664031
    - type: main_score
      value: 98.46837944664031
    - type: precision
      value: 98.3201581027668
    - type: recall
      value: 98.81422924901186
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kas_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-kas_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 69.1699604743083
    - type: f1
      value: 63.05505292906477
    - type: main_score
      value: 63.05505292906477
    - type: precision
      value: 60.62594108789761
    - type: recall
      value: 69.1699604743083
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ltz_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ltz_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.40316205533597
    - type: f1
      value: 89.26571616789009
    - type: main_score
      value: 89.26571616789009
    - type: precision
      value: 88.40179747788443
    - type: recall
      value: 91.40316205533597
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-nus_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-nus_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 38.93280632411067
    - type: f1
      value: 33.98513032905371
    - type: main_score
      value: 33.98513032905371
    - type: precision
      value: 32.56257884802308
    - type: recall
      value: 38.93280632411067
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-slk_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-slk_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.42094861660078
    - type: main_score
      value: 97.42094861660078
    - type: precision
      value: 97.14262187088273
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tir_Ethi
      name: MTEB FloresBitextMining (rus_Cyrl-tir_Ethi)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.30434782608695
    - type: f1
      value: 88.78129117259552
    - type: main_score
      value: 88.78129117259552
    - type: precision
      value: 87.61528326745717
    - type: recall
      value: 91.30434782608695
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-zho_Hant
      name: MTEB FloresBitextMining (rus_Cyrl-zho_Hant)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.1106719367589
    - type: f1
      value: 98.81422924901186
    - type: main_score
      value: 98.81422924901186
    - type: precision
      value: 98.66600790513834
    - type: recall
      value: 99.1106719367589
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-awa_Deva
      name: MTEB FloresBitextMining (rus_Cyrl-awa_Deva)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.12252964426878
    - type: f1
      value: 97.70092226613966
    - type: main_score
      value: 97.70092226613966
    - type: precision
      value: 97.50494071146245
    - type: recall
      value: 98.12252964426878
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-cym_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-cym_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.94861660079052
    - type: f1
      value: 94.74308300395256
    - type: main_score
      value: 94.74308300395256
    - type: precision
      value: 94.20289855072464
    - type: recall
      value: 95.94861660079052
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-grn_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-grn_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 77.96442687747036
    - type: f1
      value: 73.64286789187975
    - type: main_score
      value: 73.64286789187975
    - type: precision
      value: 71.99324893260821
    - type: recall
      value: 77.96442687747036
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kat_Geor
      name: MTEB FloresBitextMining (rus_Cyrl-kat_Geor)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.56719367588933
    - type: main_score
      value: 98.56719367588933
    - type: precision
      value: 98.40250329380764
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lua_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lua_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 72.03557312252964
    - type: f1
      value: 67.23928163404449
    - type: main_score
      value: 67.23928163404449
    - type: precision
      value: 65.30797101449275
    - type: recall
      value: 72.03557312252964
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-nya_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-nya_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.29249011857708
    - type: f1
      value: 90.0494071146245
    - type: main_score
      value: 90.0494071146245
    - type: precision
      value: 89.04808959156786
    - type: recall
      value: 92.29249011857708
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-slv_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-slv_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.30368906455863
    - type: main_score
      value: 98.30368906455863
    - type: precision
      value: 98.10606060606061
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tpi_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-tpi_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 80.53359683794467
    - type: f1
      value: 76.59481822525301
    - type: main_score
      value: 76.59481822525301
    - type: precision
      value: 75.12913223140497
    - type: recall
      value: 80.53359683794467
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-zsm_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-zsm_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.33201581027669
    - type: f1
      value: 96.58620365142104
    - type: main_score
      value: 96.58620365142104
    - type: precision
      value: 96.26152832674572
    - type: recall
      value: 97.33201581027669
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ayr_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-ayr_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 45.55335968379446
    - type: f1
      value: 40.13076578531388
    - type: main_score
      value: 40.13076578531388
    - type: precision
      value: 38.398064362362355
    - type: recall
      value: 45.55335968379446
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-dan_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-dan_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.68247694334651
    - type: main_score
      value: 98.68247694334651
    - type: precision
      value: 98.51778656126481
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-guj_Gujr
      name: MTEB FloresBitextMining (rus_Cyrl-guj_Gujr)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.68247694334651
    - type: main_score
      value: 98.68247694334651
    - type: precision
      value: 98.51778656126481
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kaz_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-kaz_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.81422924901186
    - type: f1
      value: 98.43544137022398
    - type: main_score
      value: 98.43544137022398
    - type: precision
      value: 98.25428194993412
    - type: recall
      value: 98.81422924901186
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lug_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lug_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 82.21343873517787
    - type: f1
      value: 77.97485726833554
    - type: main_score
      value: 77.97485726833554
    - type: precision
      value: 76.22376717485415
    - type: recall
      value: 82.21343873517787
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-oci_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-oci_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.87351778656127
    - type: f1
      value: 92.25319969885187
    - type: main_score
      value: 92.25319969885187
    - type: precision
      value: 91.5638528138528
    - type: recall
      value: 93.87351778656127
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-smo_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-smo_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 84.88142292490119
    - type: f1
      value: 81.24364765669114
    - type: main_score
      value: 81.24364765669114
    - type: precision
      value: 79.69991416137661
    - type: recall
      value: 84.88142292490119
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tsn_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-tsn_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 87.05533596837944
    - type: f1
      value: 83.90645586297761
    - type: main_score
      value: 83.90645586297761
    - type: precision
      value: 82.56752305665349
    - type: recall
      value: 87.05533596837944
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-zul_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-zul_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.15810276679841
    - type: f1
      value: 93.77140974967062
    - type: main_score
      value: 93.77140974967062
    - type: precision
      value: 93.16534914361002
    - type: recall
      value: 95.15810276679841
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-azb_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-azb_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.91699604743083
    - type: f1
      value: 77.18050065876152
    - type: main_score
      value: 77.18050065876152
    - type: precision
      value: 75.21519543258673
    - type: recall
      value: 81.91699604743083
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-deu_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-deu_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.50592885375494
    - type: f1
      value: 99.34123847167325
    - type: main_score
      value: 99.34123847167325
    - type: precision
      value: 99.2588932806324
    - type: recall
      value: 99.50592885375494
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hat_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-hat_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.00790513833992
    - type: f1
      value: 88.69126043039086
    - type: main_score
      value: 88.69126043039086
    - type: precision
      value: 87.75774044795784
    - type: recall
      value: 91.00790513833992
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kbp_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kbp_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 47.233201581027664
    - type: f1
      value: 43.01118618096943
    - type: main_score
      value: 43.01118618096943
    - type: precision
      value: 41.739069205043556
    - type: recall
      value: 47.233201581027664
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-luo_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-luo_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 60.47430830039525
    - type: f1
      value: 54.83210565429816
    - type: main_score
      value: 54.83210565429816
    - type: precision
      value: 52.81630744284779
    - type: recall
      value: 60.47430830039525
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ory_Orya
      name: MTEB FloresBitextMining (rus_Cyrl-ory_Orya)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.1106719367589
    - type: f1
      value: 98.83069828722003
    - type: main_score
      value: 98.83069828722003
    - type: precision
      value: 98.69894598155467
    - type: recall
      value: 99.1106719367589
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-sna_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-sna_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 89.72332015810277
    - type: f1
      value: 87.30013645774514
    - type: main_score
      value: 87.30013645774514
    - type: precision
      value: 86.25329380764163
    - type: recall
      value: 89.72332015810277
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tso_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-tso_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 84.38735177865613
    - type: f1
      value: 80.70424744337788
    - type: main_score
      value: 80.70424744337788
    - type: precision
      value: 79.18560606060606
    - type: recall
      value: 84.38735177865613
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-azj_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-azj_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.33201581027669
    - type: f1
      value: 96.56455862977602
    - type: main_score
      value: 96.56455862977602
    - type: precision
      value: 96.23682476943345
    - type: recall
      value: 97.33201581027669
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-dik_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-dik_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 46.047430830039524
    - type: f1
      value: 40.05513069495283
    - type: main_score
      value: 40.05513069495283
    - type: precision
      value: 38.072590197096126
    - type: recall
      value: 46.047430830039524
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hau_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-hau_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 87.94466403162056
    - type: f1
      value: 84.76943346508563
    - type: main_score
      value: 84.76943346508563
    - type: precision
      value: 83.34486166007905
    - type: recall
      value: 87.94466403162056
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kea_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-kea_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 89.42687747035573
    - type: f1
      value: 86.83803021747684
    - type: main_score
      value: 86.83803021747684
    - type: precision
      value: 85.78416149068323
    - type: recall
      value: 89.42687747035573
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lus_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lus_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 68.97233201581028
    - type: f1
      value: 64.05480726292745
    - type: main_score
      value: 64.05480726292745
    - type: precision
      value: 62.42670749487858
    - type: recall
      value: 68.97233201581028
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-pag_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-pag_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 78.75494071146245
    - type: f1
      value: 74.58573558401933
    - type: main_score
      value: 74.58573558401933
    - type: precision
      value: 73.05532028358115
    - type: recall
      value: 78.75494071146245
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-snd_Arab
      name: MTEB FloresBitextMining (rus_Cyrl-snd_Arab)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.8498023715415
    - type: f1
      value: 94.56521739130434
    - type: main_score
      value: 94.56521739130434
    - type: precision
      value: 93.97233201581028
    - type: recall
      value: 95.8498023715415
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tuk_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-tuk_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 68.08300395256917
    - type: f1
      value: 62.93565240205557
    - type: main_score
      value: 62.93565240205557
    - type: precision
      value: 61.191590257043934
    - type: recall
      value: 68.08300395256917
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bak_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-bak_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.04743083003953
    - type: f1
      value: 94.86824769433464
    - type: main_score
      value: 94.86824769433464
    - type: precision
      value: 94.34288537549406
    - type: recall
      value: 96.04743083003953
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-dyu_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-dyu_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 37.45059288537549
    - type: f1
      value: 31.670482312800807
    - type: main_score
      value: 31.670482312800807
    - type: precision
      value: 29.99928568357422
    - type: recall
      value: 37.45059288537549
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-heb_Hebr
      name: MTEB FloresBitextMining (rus_Cyrl-heb_Hebr)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.23320158102767
    - type: f1
      value: 96.38998682476942
    - type: main_score
      value: 96.38998682476942
    - type: precision
      value: 95.99802371541502
    - type: recall
      value: 97.23320158102767
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-khk_Cyrl
      name: MTEB FloresBitextMining (rus_Cyrl-khk_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.41897233201581
    - type: f1
      value: 98.00724637681158
    - type: main_score
      value: 98.00724637681158
    - type: precision
      value: 97.82938076416336
    - type: recall
      value: 98.41897233201581
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lvs_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-lvs_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.4308300395257
    - type: f1
      value: 96.61396574440053
    - type: main_score
      value: 96.61396574440053
    - type: precision
      value: 96.2203557312253
    - type: recall
      value: 97.4308300395257
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-pan_Guru
      name: MTEB FloresBitextMining (rus_Cyrl-pan_Guru)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.07773386034256
    - type: main_score
      value: 99.07773386034256
    - type: precision
      value: 98.96245059288538
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-som_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-som_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 87.74703557312253
    - type: f1
      value: 84.52898550724638
    - type: main_score
      value: 84.52898550724638
    - type: precision
      value: 83.09288537549409
    - type: recall
      value: 87.74703557312253
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tum_Latn
      name: MTEB FloresBitextMining (rus_Cyrl-tum_Latn)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 87.15415019762845
    - type: f1
      value: 83.85069640504425
    - type: main_score
      value: 83.85069640504425
    - type: precision
      value: 82.43671183888576
    - type: recall
      value: 87.15415019762845
    task:
      type: BitextMining
  - dataset:
      config: taq_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (taq_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 28.55731225296443
    - type: f1
      value: 26.810726360049568
    - type: main_score
      value: 26.810726360049568
    - type: precision
      value: 26.260342858265577
    - type: recall
      value: 28.55731225296443
    task:
      type: BitextMining
  - dataset:
      config: war_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (war_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.86166007905138
    - type: f1
      value: 94.03147083483051
    - type: main_score
      value: 94.03147083483051
    - type: precision
      value: 93.70653606003322
    - type: recall
      value: 94.86166007905138
    task:
      type: BitextMining
  - dataset:
      config: arb_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (arb_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.34387351778656
    - type: f1
      value: 95.23056653491436
    - type: main_score
      value: 95.23056653491436
    - type: precision
      value: 94.70520421607378
    - type: recall
      value: 96.34387351778656
    task:
      type: BitextMining
  - dataset:
      config: bul_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (bul_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.90118577075098
    - type: f1
      value: 99.86824769433464
    - type: main_score
      value: 99.86824769433464
    - type: precision
      value: 99.85177865612648
    - type: recall
      value: 99.90118577075098
    task:
      type: BitextMining
  - dataset:
      config: fra_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (fra_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.9459815546772
    - type: main_score
      value: 98.9459815546772
    - type: precision
      value: 98.81422924901186
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: jpn_Jpan-rus_Cyrl
      name: MTEB FloresBitextMining (jpn_Jpan-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.3201581027668
    - type: f1
      value: 97.76021080368905
    - type: main_score
      value: 97.76021080368905
    - type: precision
      value: 97.48023715415019
    - type: recall
      value: 98.3201581027668
    task:
      type: BitextMining
  - dataset:
      config: lij_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lij_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 83.49802371541502
    - type: f1
      value: 81.64800059239636
    - type: main_score
      value: 81.64800059239636
    - type: precision
      value: 80.9443055878478
    - type: recall
      value: 83.49802371541502
    task:
      type: BitextMining
  - dataset:
      config: mya_Mymr-rus_Cyrl
      name: MTEB FloresBitextMining (mya_Mymr-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 90.21739130434783
    - type: f1
      value: 88.76776366313682
    - type: main_score
      value: 88.76776366313682
    - type: precision
      value: 88.18370446119435
    - type: recall
      value: 90.21739130434783
    task:
      type: BitextMining
  - dataset:
      config: sag_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (sag_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 41.699604743083
    - type: f1
      value: 39.53066322643847
    - type: main_score
      value: 39.53066322643847
    - type: precision
      value: 38.822876239229274
    - type: recall
      value: 41.699604743083
    task:
      type: BitextMining
  - dataset:
      config: taq_Tfng-rus_Cyrl
      name: MTEB FloresBitextMining (taq_Tfng-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 10.67193675889328
    - type: f1
      value: 9.205744965817951
    - type: main_score
      value: 9.205744965817951
    - type: precision
      value: 8.85195219073817
    - type: recall
      value: 10.67193675889328
    task:
      type: BitextMining
  - dataset:
      config: wol_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (wol_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 63.537549407114625
    - type: f1
      value: 60.65190727391827
    - type: main_score
      value: 60.65190727391827
    - type: precision
      value: 59.61144833427442
    - type: recall
      value: 63.537549407114625
    task:
      type: BitextMining
  - dataset:
      config: arb_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (arb_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 13.142292490118576
    - type: f1
      value: 12.372910318176764
    - type: main_score
      value: 12.372910318176764
    - type: precision
      value: 12.197580895919188
    - type: recall
      value: 13.142292490118576
    task:
      type: BitextMining
  - dataset:
      config: cat_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (cat_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.80599472990777
    - type: main_score
      value: 98.80599472990777
    - type: precision
      value: 98.72953133822698
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: fur_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (fur_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.02766798418972
    - type: f1
      value: 79.36184294084613
    - type: main_score
      value: 79.36184294084613
    - type: precision
      value: 78.69187826527705
    - type: recall
      value: 81.02766798418972
    task:
      type: BitextMining
  - dataset:
      config: kab_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kab_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 34.387351778656125
    - type: f1
      value: 32.02306921576947
    - type: main_score
      value: 32.02306921576947
    - type: precision
      value: 31.246670347137467
    - type: recall
      value: 34.387351778656125
    task:
      type: BitextMining
  - dataset:
      config: lim_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lim_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 78.26086956521739
    - type: f1
      value: 75.90239449214359
    - type: main_score
      value: 75.90239449214359
    - type: precision
      value: 75.02211430745493
    - type: recall
      value: 78.26086956521739
    task:
      type: BitextMining
  - dataset:
      config: nld_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (nld_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.9459815546772
    - type: main_score
      value: 98.9459815546772
    - type: precision
      value: 98.81422924901186
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: san_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (san_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 87.94466403162056
    - type: f1
      value: 86.68928897189767
    - type: main_score
      value: 86.68928897189767
    - type: precision
      value: 86.23822997079216
    - type: recall
      value: 87.94466403162056
    task:
      type: BitextMining
  - dataset:
      config: tat_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (tat_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.03557312252964
    - type: f1
      value: 96.4167365353136
    - type: main_score
      value: 96.4167365353136
    - type: precision
      value: 96.16847826086958
    - type: recall
      value: 97.03557312252964
    task:
      type: BitextMining
  - dataset:
      config: xho_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (xho_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 86.95652173913044
    - type: f1
      value: 85.5506497283435
    - type: main_score
      value: 85.5506497283435
    - type: precision
      value: 84.95270479733395
    - type: recall
      value: 86.95652173913044
    task:
      type: BitextMining
  - dataset:
      config: ars_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (ars_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 96.6403162055336
    - type: f1
      value: 95.60935441370223
    - type: main_score
      value: 95.60935441370223
    - type: precision
      value: 95.13339920948617
    - type: recall
      value: 96.6403162055336
    task:
      type: BitextMining
  - dataset:
      config: ceb_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ceb_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.7509881422925
    - type: f1
      value: 95.05209198303827
    - type: main_score
      value: 95.05209198303827
    - type: precision
      value: 94.77662283368805
    - type: recall
      value: 95.7509881422925
    task:
      type: BitextMining
  - dataset:
      config: fuv_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (fuv_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 45.25691699604743
    - type: f1
      value: 42.285666666742365
    - type: main_score
      value: 42.285666666742365
    - type: precision
      value: 41.21979853402283
    - type: recall
      value: 45.25691699604743
    task:
      type: BitextMining
  - dataset:
      config: kac_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kac_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 34.683794466403164
    - type: f1
      value: 33.3235346229031
    - type: main_score
      value: 33.3235346229031
    - type: precision
      value: 32.94673924616852
    - type: recall
      value: 34.683794466403164
    task:
      type: BitextMining
  - dataset:
      config: lin_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lin_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 86.85770750988142
    - type: f1
      value: 85.1867110799439
    - type: main_score
      value: 85.1867110799439
    - type: precision
      value: 84.53038212173273
    - type: recall
      value: 86.85770750988142
    task:
      type: BitextMining
  - dataset:
      config: nno_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (nno_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.4308300395257
    - type: f1
      value: 96.78383210991906
    - type: main_score
      value: 96.78383210991906
    - type: precision
      value: 96.51185770750989
    - type: recall
      value: 97.4308300395257
    task:
      type: BitextMining
  - dataset:
      config: sat_Olck-rus_Cyrl
      name: MTEB FloresBitextMining (sat_Olck-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 1.185770750988142
    - type: f1
      value: 1.0279253129117258
    - type: main_score
      value: 1.0279253129117258
    - type: precision
      value: 1.0129746819135175
    - type: recall
      value: 1.185770750988142
    task:
      type: BitextMining
  - dataset:
      config: tel_Telu-rus_Cyrl
      name: MTEB FloresBitextMining (tel_Telu-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.12252964426878
    - type: f1
      value: 97.61198945981555
    - type: main_score
      value: 97.61198945981555
    - type: precision
      value: 97.401185770751
    - type: recall
      value: 98.12252964426878
    task:
      type: BitextMining
  - dataset:
      config: ydd_Hebr-rus_Cyrl
      name: MTEB FloresBitextMining (ydd_Hebr-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 75.8893280632411
    - type: f1
      value: 74.00244008018511
    - type: main_score
      value: 74.00244008018511
    - type: precision
      value: 73.25683020960382
    - type: recall
      value: 75.8893280632411
    task:
      type: BitextMining
  - dataset:
      config: ary_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (ary_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 86.56126482213439
    - type: f1
      value: 83.72796285839765
    - type: main_score
      value: 83.72796285839765
    - type: precision
      value: 82.65014273166447
    - type: recall
      value: 86.56126482213439
    task:
      type: BitextMining
  - dataset:
      config: ces_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ces_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.60474308300395
    - type: f1
      value: 99.4729907773386
    - type: main_score
      value: 99.4729907773386
    - type: precision
      value: 99.40711462450594
    - type: recall
      value: 99.60474308300395
    task:
      type: BitextMining
  - dataset:
      config: gaz_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (gaz_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 42.58893280632411
    - type: f1
      value: 40.75832866805978
    - type: main_score
      value: 40.75832866805978
    - type: precision
      value: 40.14285046917723
    - type: recall
      value: 42.58893280632411
    task:
      type: BitextMining
  - dataset:
      config: kam_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kam_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 45.25691699604743
    - type: f1
      value: 42.6975518029456
    - type: main_score
      value: 42.6975518029456
    - type: precision
      value: 41.87472710984596
    - type: recall
      value: 45.25691699604743
    task:
      type: BitextMining
  - dataset:
      config: lit_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lit_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.33201581027669
    - type: f1
      value: 96.62384716732542
    - type: main_score
      value: 96.62384716732542
    - type: precision
      value: 96.3175230566535
    - type: recall
      value: 97.33201581027669
    task:
      type: BitextMining
  - dataset:
      config: nob_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (nob_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.30368906455863
    - type: main_score
      value: 98.30368906455863
    - type: precision
      value: 98.10606060606061
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: scn_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (scn_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 70.45454545454545
    - type: f1
      value: 68.62561022640075
    - type: main_score
      value: 68.62561022640075
    - type: precision
      value: 67.95229103411222
    - type: recall
      value: 70.45454545454545
    task:
      type: BitextMining
  - dataset:
      config: tgk_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (tgk_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.4901185770751
    - type: f1
      value: 91.58514492753623
    - type: main_score
      value: 91.58514492753623
    - type: precision
      value: 91.24759298672342
    - type: recall
      value: 92.4901185770751
    task:
      type: BitextMining
  - dataset:
      config: yor_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (yor_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 67.98418972332016
    - type: f1
      value: 64.72874247330768
    - type: main_score
      value: 64.72874247330768
    - type: precision
      value: 63.450823399938685
    - type: recall
      value: 67.98418972332016
    task:
      type: BitextMining
  - dataset:
      config: arz_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (arz_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 94.56521739130434
    - type: f1
      value: 93.07971014492755
    - type: main_score
      value: 93.07971014492755
    - type: precision
      value: 92.42753623188406
    - type: recall
      value: 94.56521739130434
    task:
      type: BitextMining
  - dataset:
      config: cjk_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (cjk_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 38.63636363636363
    - type: f1
      value: 36.25747140862938
    - type: main_score
      value: 36.25747140862938
    - type: precision
      value: 35.49101355074723
    - type: recall
      value: 38.63636363636363
    task:
      type: BitextMining
  - dataset:
      config: gla_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (gla_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 69.26877470355731
    - type: f1
      value: 66.11797423328613
    - type: main_score
      value: 66.11797423328613
    - type: precision
      value: 64.89369649409694
    - type: recall
      value: 69.26877470355731
    task:
      type: BitextMining
  - dataset:
      config: kan_Knda-rus_Cyrl
      name: MTEB FloresBitextMining (kan_Knda-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.51505740636176
    - type: main_score
      value: 97.51505740636176
    - type: precision
      value: 97.30731225296442
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: lmo_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lmo_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 73.3201581027668
    - type: f1
      value: 71.06371608677273
    - type: main_score
      value: 71.06371608677273
    - type: precision
      value: 70.26320288266223
    - type: recall
      value: 73.3201581027668
    task:
      type: BitextMining
  - dataset:
      config: npi_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (npi_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.82608695652173
    - type: f1
      value: 97.36645107198466
    - type: main_score
      value: 97.36645107198466
    - type: precision
      value: 97.1772068511199
    - type: recall
      value: 97.82608695652173
    task:
      type: BitextMining
  - dataset:
      config: shn_Mymr-rus_Cyrl
      name: MTEB FloresBitextMining (shn_Mymr-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 39.426877470355734
    - type: f1
      value: 37.16728785513024
    - type: main_score
      value: 37.16728785513024
    - type: precision
      value: 36.56918548278505
    - type: recall
      value: 39.426877470355734
    task:
      type: BitextMining
  - dataset:
      config: tgl_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (tgl_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.92490118577075
    - type: f1
      value: 97.6378693769998
    - type: main_score
      value: 97.6378693769998
    - type: precision
      value: 97.55371440154047
    - type: recall
      value: 97.92490118577075
    task:
      type: BitextMining
  - dataset:
      config: yue_Hant-rus_Cyrl
      name: MTEB FloresBitextMining (yue_Hant-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.92490118577075
    - type: f1
      value: 97.3833051006964
    - type: main_score
      value: 97.3833051006964
    - type: precision
      value: 97.1590909090909
    - type: recall
      value: 97.92490118577075
    task:
      type: BitextMining
  - dataset:
      config: asm_Beng-rus_Cyrl
      name: MTEB FloresBitextMining (asm_Beng-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.78656126482213
    - type: f1
      value: 91.76917395296842
    - type: main_score
      value: 91.76917395296842
    - type: precision
      value: 91.38292866553736
    - type: recall
      value: 92.78656126482213
    task:
      type: BitextMining
  - dataset:
      config: ckb_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (ckb_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 80.8300395256917
    - type: f1
      value: 79.17664345468799
    - type: main_score
      value: 79.17664345468799
    - type: precision
      value: 78.5622171683459
    - type: recall
      value: 80.8300395256917
    task:
      type: BitextMining
  - dataset:
      config: gle_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (gle_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 85.86956521739131
    - type: f1
      value: 84.45408265372492
    - type: main_score
      value: 84.45408265372492
    - type: precision
      value: 83.8774340026703
    - type: recall
      value: 85.86956521739131
    task:
      type: BitextMining
  - dataset:
      config: kas_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (kas_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 76.28458498023716
    - type: f1
      value: 74.11216313578267
    - type: main_score
      value: 74.11216313578267
    - type: precision
      value: 73.2491277759584
    - type: recall
      value: 76.28458498023716
    task:
      type: BitextMining
  - dataset:
      config: ltg_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ltg_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 71.14624505928853
    - type: f1
      value: 68.69245357723618
    - type: main_score
      value: 68.69245357723618
    - type: precision
      value: 67.8135329666459
    - type: recall
      value: 71.14624505928853
    task:
      type: BitextMining
  - dataset:
      config: nso_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (nso_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 87.64822134387352
    - type: f1
      value: 85.98419219986725
    - type: main_score
      value: 85.98419219986725
    - type: precision
      value: 85.32513873917036
    - type: recall
      value: 87.64822134387352
    task:
      type: BitextMining
  - dataset:
      config: sin_Sinh-rus_Cyrl
      name: MTEB FloresBitextMining (sin_Sinh-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.62845849802372
    - type: f1
      value: 97.10144927536231
    - type: main_score
      value: 97.10144927536231
    - type: precision
      value: 96.87986585219788
    - type: recall
      value: 97.62845849802372
    task:
      type: BitextMining
  - dataset:
      config: tha_Thai-rus_Cyrl
      name: MTEB FloresBitextMining (tha_Thai-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.71541501976284
    - type: f1
      value: 98.28722002635045
    - type: main_score
      value: 98.28722002635045
    - type: precision
      value: 98.07312252964427
    - type: recall
      value: 98.71541501976284
    task:
      type: BitextMining
  - dataset:
      config: zho_Hans-rus_Cyrl
      name: MTEB FloresBitextMining (zho_Hans-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.68247694334651
    - type: main_score
      value: 98.68247694334651
    - type: precision
      value: 98.51778656126481
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: ast_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ast_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.65217391304348
    - type: f1
      value: 94.90649683857505
    - type: main_score
      value: 94.90649683857505
    - type: precision
      value: 94.61352657004831
    - type: recall
      value: 95.65217391304348
    task:
      type: BitextMining
  - dataset:
      config: crh_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (crh_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 93.08300395256917
    - type: f1
      value: 92.20988998886428
    - type: main_score
      value: 92.20988998886428
    - type: precision
      value: 91.85631013694254
    - type: recall
      value: 93.08300395256917
    task:
      type: BitextMining
  - dataset:
      config: glg_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (glg_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.55335968379447
    - type: f1
      value: 95.18006148440931
    - type: main_score
      value: 95.18006148440931
    - type: precision
      value: 95.06540560888386
    - type: recall
      value: 95.55335968379447
    task:
      type: BitextMining
  - dataset:
      config: kas_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (kas_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 55.03952569169961
    - type: f1
      value: 52.19871938895554
    - type: main_score
      value: 52.19871938895554
    - type: precision
      value: 51.17660971469557
    - type: recall
      value: 55.03952569169961
    task:
      type: BitextMining
  - dataset:
      config: ltz_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ltz_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 87.64822134387352
    - type: f1
      value: 86.64179841897234
    - type: main_score
      value: 86.64179841897234
    - type: precision
      value: 86.30023235431587
    - type: recall
      value: 87.64822134387352
    task:
      type: BitextMining
  - dataset:
      config: nus_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (nus_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 27.4703557312253
    - type: f1
      value: 25.703014277858088
    - type: main_score
      value: 25.703014277858088
    - type: precision
      value: 25.194105476917315
    - type: recall
      value: 27.4703557312253
    task:
      type: BitextMining
  - dataset:
      config: slk_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (slk_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.1106719367589
    - type: main_score
      value: 99.1106719367589
    - type: precision
      value: 99.02832674571805
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: tir_Ethi-rus_Cyrl
      name: MTEB FloresBitextMining (tir_Ethi-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 80.73122529644269
    - type: f1
      value: 78.66903754775608
    - type: main_score
      value: 78.66903754775608
    - type: precision
      value: 77.86431694163612
    - type: recall
      value: 80.73122529644269
    task:
      type: BitextMining
  - dataset:
      config: zho_Hant-rus_Cyrl
      name: MTEB FloresBitextMining (zho_Hant-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.22134387351778
    - type: f1
      value: 97.66798418972333
    - type: main_score
      value: 97.66798418972333
    - type: precision
      value: 97.40612648221344
    - type: recall
      value: 98.22134387351778
    task:
      type: BitextMining
  - dataset:
      config: awa_Deva-rus_Cyrl
      name: MTEB FloresBitextMining (awa_Deva-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.5296442687747
    - type: f1
      value: 96.94224857268335
    - type: main_score
      value: 96.94224857268335
    - type: precision
      value: 96.68560606060606
    - type: recall
      value: 97.5296442687747
    task:
      type: BitextMining
  - dataset:
      config: cym_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (cym_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 92.68774703557312
    - type: f1
      value: 91.69854302097961
    - type: main_score
      value: 91.69854302097961
    - type: precision
      value: 91.31236846157795
    - type: recall
      value: 92.68774703557312
    task:
      type: BitextMining
  - dataset:
      config: grn_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (grn_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 64.13043478260869
    - type: f1
      value: 61.850586118740004
    - type: main_score
      value: 61.850586118740004
    - type: precision
      value: 61.0049495186209
    - type: recall
      value: 64.13043478260869
    task:
      type: BitextMining
  - dataset:
      config: kat_Geor-rus_Cyrl
      name: MTEB FloresBitextMining (kat_Geor-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.59881422924902
    - type: main_score
      value: 97.59881422924902
    - type: precision
      value: 97.42534036012296
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: lua_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lua_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 63.63636363636363
    - type: f1
      value: 60.9709122526128
    - type: main_score
      value: 60.9709122526128
    - type: precision
      value: 60.03915902282226
    - type: recall
      value: 63.63636363636363
    task:
      type: BitextMining
  - dataset:
      config: nya_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (nya_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 89.2292490118577
    - type: f1
      value: 87.59723824473149
    - type: main_score
      value: 87.59723824473149
    - type: precision
      value: 86.90172707867349
    - type: recall
      value: 89.2292490118577
    task:
      type: BitextMining
  - dataset:
      config: slv_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (slv_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.01185770750988
    - type: f1
      value: 98.74835309617917
    - type: main_score
      value: 98.74835309617917
    - type: precision
      value: 98.63636363636364
    - type: recall
      value: 99.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: tpi_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (tpi_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 77.37154150197628
    - type: f1
      value: 75.44251611276084
    - type: main_score
      value: 75.44251611276084
    - type: precision
      value: 74.78103665109595
    - type: recall
      value: 77.37154150197628
    task:
      type: BitextMining
  - dataset:
      config: zsm_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (zsm_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.2094861660079
    - type: f1
      value: 98.96245059288538
    - type: main_score
      value: 98.96245059288538
    - type: precision
      value: 98.8471673254282
    - type: recall
      value: 99.2094861660079
    task:
      type: BitextMining
  - dataset:
      config: ayr_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (ayr_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 27.766798418972332
    - type: f1
      value: 26.439103195281312
    - type: main_score
      value: 26.439103195281312
    - type: precision
      value: 26.052655604573964
    - type: recall
      value: 27.766798418972332
    task:
      type: BitextMining
  - dataset:
      config: dan_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (dan_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.30830039525692
    - type: f1
      value: 99.07773386034255
    - type: main_score
      value: 99.07773386034255
    - type: precision
      value: 98.96245059288538
    - type: recall
      value: 99.30830039525692
    task:
      type: BitextMining
  - dataset:
      config: guj_Gujr-rus_Cyrl
      name: MTEB FloresBitextMining (guj_Gujr-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.82608695652173
    - type: f1
      value: 97.26449275362317
    - type: main_score
      value: 97.26449275362317
    - type: precision
      value: 97.02498588368154
    - type: recall
      value: 97.82608695652173
    task:
      type: BitextMining
  - dataset:
      config: kaz_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (kaz_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.5296442687747
    - type: f1
      value: 97.03557312252964
    - type: main_score
      value: 97.03557312252964
    - type: precision
      value: 96.85022158342316
    - type: recall
      value: 97.5296442687747
    task:
      type: BitextMining
  - dataset:
      config: lug_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lug_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 68.57707509881423
    - type: f1
      value: 65.93361605820395
    - type: main_score
      value: 65.93361605820395
    - type: precision
      value: 64.90348248593789
    - type: recall
      value: 68.57707509881423
    task:
      type: BitextMining
  - dataset:
      config: oci_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (oci_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 86.26482213438736
    - type: f1
      value: 85.33176417155623
    - type: main_score
      value: 85.33176417155623
    - type: precision
      value: 85.00208833384637
    - type: recall
      value: 86.26482213438736
    task:
      type: BitextMining
  - dataset:
      config: smo_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (smo_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 77.96442687747036
    - type: f1
      value: 75.70960450188885
    - type: main_score
      value: 75.70960450188885
    - type: precision
      value: 74.8312632736777
    - type: recall
      value: 77.96442687747036
    task:
      type: BitextMining
  - dataset:
      config: tsn_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (tsn_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 84.38735177865613
    - type: f1
      value: 82.13656376349225
    - type: main_score
      value: 82.13656376349225
    - type: precision
      value: 81.16794543904518
    - type: recall
      value: 84.38735177865613
    task:
      type: BitextMining
  - dataset:
      config: zul_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (zul_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 90.21739130434783
    - type: f1
      value: 88.77570602050753
    - type: main_score
      value: 88.77570602050753
    - type: precision
      value: 88.15978104021582
    - type: recall
      value: 90.21739130434783
    task:
      type: BitextMining
  - dataset:
      config: azb_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (azb_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 65.71146245059289
    - type: f1
      value: 64.18825390221271
    - type: main_score
      value: 64.18825390221271
    - type: precision
      value: 63.66811154793568
    - type: recall
      value: 65.71146245059289
    task:
      type: BitextMining
  - dataset:
      config: deu_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (deu_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 99.70355731225297
    - type: f1
      value: 99.60474308300395
    - type: main_score
      value: 99.60474308300395
    - type: precision
      value: 99.55533596837944
    - type: recall
      value: 99.70355731225297
    task:
      type: BitextMining
  - dataset:
      config: hat_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (hat_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 86.7588932806324
    - type: f1
      value: 85.86738623695146
    - type: main_score
      value: 85.86738623695146
    - type: precision
      value: 85.55235467420822
    - type: recall
      value: 86.7588932806324
    task:
      type: BitextMining
  - dataset:
      config: kbp_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kbp_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 34.88142292490119
    - type: f1
      value: 32.16511669463015
    - type: main_score
      value: 32.16511669463015
    - type: precision
      value: 31.432098549546318
    - type: recall
      value: 34.88142292490119
    task:
      type: BitextMining
  - dataset:
      config: luo_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (luo_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 52.27272727272727
    - type: f1
      value: 49.60489626836975
    - type: main_score
      value: 49.60489626836975
    - type: precision
      value: 48.69639631803339
    - type: recall
      value: 52.27272727272727
    task:
      type: BitextMining
  - dataset:
      config: ory_Orya-rus_Cyrl
      name: MTEB FloresBitextMining (ory_Orya-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.82608695652173
    - type: f1
      value: 97.27437417654808
    - type: main_score
      value: 97.27437417654808
    - type: precision
      value: 97.04968944099377
    - type: recall
      value: 97.82608695652173
    task:
      type: BitextMining
  - dataset:
      config: sna_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (sna_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 85.37549407114624
    - type: f1
      value: 83.09911316305177
    - type: main_score
      value: 83.09911316305177
    - type: precision
      value: 82.1284950958864
    - type: recall
      value: 85.37549407114624
    task:
      type: BitextMining
  - dataset:
      config: tso_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (tso_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 82.90513833992095
    - type: f1
      value: 80.28290385503824
    - type: main_score
      value: 80.28290385503824
    - type: precision
      value: 79.23672543237761
    - type: recall
      value: 82.90513833992095
    task:
      type: BitextMining
  - dataset:
      config: azj_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (azj_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.02371541501977
    - type: f1
      value: 97.49200075287031
    - type: main_score
      value: 97.49200075287031
    - type: precision
      value: 97.266139657444
    - type: recall
      value: 98.02371541501977
    task:
      type: BitextMining
  - dataset:
      config: dik_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (dik_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 38.43873517786561
    - type: f1
      value: 35.78152442955223
    - type: main_score
      value: 35.78152442955223
    - type: precision
      value: 34.82424325078237
    - type: recall
      value: 38.43873517786561
    task:
      type: BitextMining
  - dataset:
      config: hau_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (hau_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.42292490118577
    - type: f1
      value: 79.24612283124593
    - type: main_score
      value: 79.24612283124593
    - type: precision
      value: 78.34736070751448
    - type: recall
      value: 81.42292490118577
    task:
      type: BitextMining
  - dataset:
      config: kea_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (kea_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 81.62055335968378
    - type: f1
      value: 80.47015182884748
    - type: main_score
      value: 80.47015182884748
    - type: precision
      value: 80.02671028885862
    - type: recall
      value: 81.62055335968378
    task:
      type: BitextMining
  - dataset:
      config: lus_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lus_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 62.74703557312253
    - type: f1
      value: 60.53900079111122
    - type: main_score
      value: 60.53900079111122
    - type: precision
      value: 59.80024202850289
    - type: recall
      value: 62.74703557312253
    task:
      type: BitextMining
  - dataset:
      config: pag_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (pag_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 74.01185770750988
    - type: f1
      value: 72.57280648279529
    - type: main_score
      value: 72.57280648279529
    - type: precision
      value: 71.99952968456789
    - type: recall
      value: 74.01185770750988
    task:
      type: BitextMining
  - dataset:
      config: snd_Arab-rus_Cyrl
      name: MTEB FloresBitextMining (snd_Arab-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 91.30434782608695
    - type: f1
      value: 90.24653499445358
    - type: main_score
      value: 90.24653499445358
    - type: precision
      value: 89.83134068200232
    - type: recall
      value: 91.30434782608695
    task:
      type: BitextMining
  - dataset:
      config: tuk_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (tuk_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 47.62845849802372
    - type: f1
      value: 45.812928836644254
    - type: main_score
      value: 45.812928836644254
    - type: precision
      value: 45.23713833170355
    - type: recall
      value: 47.62845849802372
    task:
      type: BitextMining
  - dataset:
      config: bak_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (bak_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.8498023715415
    - type: f1
      value: 95.18904459615922
    - type: main_score
      value: 95.18904459615922
    - type: precision
      value: 94.92812441182006
    - type: recall
      value: 95.8498023715415
    task:
      type: BitextMining
  - dataset:
      config: dyu_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (dyu_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 29.64426877470356
    - type: f1
      value: 27.287335193938166
    - type: main_score
      value: 27.287335193938166
    - type: precision
      value: 26.583996026587492
    - type: recall
      value: 29.64426877470356
    task:
      type: BitextMining
  - dataset:
      config: heb_Hebr-rus_Cyrl
      name: MTEB FloresBitextMining (heb_Hebr-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 98.91304347826086
    - type: f1
      value: 98.55072463768116
    - type: main_score
      value: 98.55072463768116
    - type: precision
      value: 98.36956521739131
    - type: recall
      value: 98.91304347826086
    task:
      type: BitextMining
  - dataset:
      config: khk_Cyrl-rus_Cyrl
      name: MTEB FloresBitextMining (khk_Cyrl-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 95.15810276679841
    - type: f1
      value: 94.44009547764487
    - type: main_score
      value: 94.44009547764487
    - type: precision
      value: 94.16579797014579
    - type: recall
      value: 95.15810276679841
    task:
      type: BitextMining
  - dataset:
      config: lvs_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (lvs_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.92490118577075
    - type: f1
      value: 97.51467241585817
    - type: main_score
      value: 97.51467241585817
    - type: precision
      value: 97.36166007905138
    - type: recall
      value: 97.92490118577075
    task:
      type: BitextMining
  - dataset:
      config: pan_Guru-rus_Cyrl
      name: MTEB FloresBitextMining (pan_Guru-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 97.92490118577075
    - type: f1
      value: 97.42918313570486
    - type: main_score
      value: 97.42918313570486
    - type: precision
      value: 97.22261434217955
    - type: recall
      value: 97.92490118577075
    task:
      type: BitextMining
  - dataset:
      config: som_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (som_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 75.69169960474308
    - type: f1
      value: 73.7211667065916
    - type: main_score
      value: 73.7211667065916
    - type: precision
      value: 72.95842401892384
    - type: recall
      value: 75.69169960474308
    task:
      type: BitextMining
  - dataset:
      config: tum_Latn-rus_Cyrl
      name: MTEB FloresBitextMining (tum_Latn-rus_Cyrl)
      revision: e6b647fcb6299a2f686f742f4d4c023e553ea67e
      split: devtest
      type: mteb/flores
    metrics:
    - type: accuracy
      value: 85.67193675889328
    - type: f1
      value: 82.9296066252588
    - type: main_score
      value: 82.9296066252588
    - type: precision
      value: 81.77330225447936
    - type: recall
      value: 85.67193675889328
    task:
      type: BitextMining
  - dataset:
      config: default
      name: MTEB GeoreviewClassification (default)
      revision: 3765c0d1de6b7d264bc459433c45e5a75513839c
      split: test
      type: ai-forever/georeview-classification
    metrics:
    - type: accuracy
      value: 44.6630859375
    - type: f1
      value: 42.607425073610536
    - type: f1_weighted
      value: 42.60639474586065
    - type: main_score
      value: 44.6630859375
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB GeoreviewClusteringP2P (default)
      revision: 97a313c8fc85b47f13f33e7e9a95c1ad888c7fec
      split: test
      type: ai-forever/georeview-clustering-p2p
    metrics:
    - type: main_score
      value: 58.15951247070825
    - type: v_measure
      value: 58.15951247070825
    - type: v_measure_std
      value: 0.6739615788288809
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB HeadlineClassification (default)
      revision: 2fe05ee6b5832cda29f2ef7aaad7b7fe6a3609eb
      split: test
      type: ai-forever/headline-classification
    metrics:
    - type: accuracy
      value: 73.935546875
    - type: f1
      value: 73.8654872186846
    - type: f1_weighted
      value: 73.86733122685095
    - type: main_score
      value: 73.935546875
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB InappropriatenessClassification (default)
      revision: 601651fdc45ef243751676e62dd7a19f491c0285
      split: test
      type: ai-forever/inappropriateness-classification
    metrics:
    - type: accuracy
      value: 59.16015624999999
    - type: ap
      value: 55.52276605836938
    - type: ap_weighted
      value: 55.52276605836938
    - type: f1
      value: 58.614248199637956
    - type: f1_weighted
      value: 58.614248199637956
    - type: main_score
      value: 59.16015624999999
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB KinopoiskClassification (default)
      revision: 5911f26666ac11af46cb9c6849d0dc80a378af24
      split: test
      type: ai-forever/kinopoisk-sentiment-classification
    metrics:
    - type: accuracy
      value: 49.959999999999994
    - type: f1
      value: 48.4900332316098
    - type: f1_weighted
      value: 48.4900332316098
    - type: main_score
      value: 49.959999999999994
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB LanguageClassification (default)
      revision: aa56583bf2bc52b0565770607d6fc3faebecf9e2
      split: test
      type: papluca/language-identification
    metrics:
    - type: accuracy
      value: 71.005859375
    - type: f1
      value: 69.63481100303348
    - type: f1_weighted
      value: 69.64640413409529
    - type: main_score
      value: 71.005859375
    task:
      type: Classification
  - dataset:
      config: ru
      name: MTEB MLSUMClusteringP2P (ru)
      revision: b5d54f8f3b61ae17845046286940f03c6bc79bc7
      split: test
      type: reciTAL/mlsum
    metrics:
    - type: main_score
      value: 42.11280087032343
    - type: v_measure
      value: 42.11280087032343
    - type: v_measure_std
      value: 6.7619971723605135
    task:
      type: Clustering
  - dataset:
      config: ru
      name: MTEB MLSUMClusteringP2P.v2 (ru)
      revision: b5d54f8f3b61ae17845046286940f03c6bc79bc7
      split: test
      type: reciTAL/mlsum
    metrics:
    - type: main_score
      value: 43.00112546945811
    - type: v_measure
      value: 43.00112546945811
    - type: v_measure_std
      value: 1.4740560414835675
    task:
      type: Clustering
  - dataset:
      config: ru
      name: MTEB MLSUMClusteringS2S (ru)
      revision: b5d54f8f3b61ae17845046286940f03c6bc79bc7
      split: test
      type: reciTAL/mlsum
    metrics:
    - type: main_score
      value: 39.81446080575161
    - type: v_measure
      value: 39.81446080575161
    - type: v_measure_std
      value: 7.125661320308298
    task:
      type: Clustering
  - dataset:
      config: ru
      name: MTEB MLSUMClusteringS2S.v2 (ru)
      revision: b5d54f8f3b61ae17845046286940f03c6bc79bc7
      split: test
      type: reciTAL/mlsum
    metrics:
    - type: main_score
      value: 39.29659668980239
    - type: v_measure
      value: 39.29659668980239
    - type: v_measure_std
      value: 2.6570502923023094
    task:
      type: Clustering
  - dataset:
      config: ru
      name: MTEB MultiLongDocRetrieval (ru)
      revision: d67138e705d963e346253a80e59676ddb418810a
      split: dev
      type: Shitao/MLDR
    metrics:
    - type: main_score
      value: 38.671
    - type: map_at_1
      value: 30.0
    - type: map_at_10
      value: 36.123
    - type: map_at_100
      value: 36.754999999999995
    - type: map_at_1000
      value: 36.806
    - type: map_at_20
      value: 36.464
    - type: map_at_3
      value: 35.25
    - type: map_at_5
      value: 35.8
    - type: mrr_at_1
      value: 30.0
    - type: mrr_at_10
      value: 36.122817460317464
    - type: mrr_at_100
      value: 36.75467016625293
    - type: mrr_at_1000
      value: 36.80612724920882
    - type: mrr_at_20
      value: 36.46359681984682
    - type: mrr_at_3
      value: 35.25
    - type: mrr_at_5
      value: 35.800000000000004
    - type: nauc_map_at_1000_diff1
      value: 55.61987610843598
    - type: nauc_map_at_1000_max
      value: 52.506795017152186
    - type: nauc_map_at_1000_std
      value: 2.95487192066911
    - type: nauc_map_at_100_diff1
      value: 55.598419532054734
    - type: nauc_map_at_100_max
      value: 52.48192017040307
    - type: nauc_map_at_100_std
      value: 2.930120252521189
    - type: nauc_map_at_10_diff1
      value: 56.02309155375198
    - type: nauc_map_at_10_max
      value: 52.739573233234424
    - type: nauc_map_at_10_std
      value: 2.4073432421641545
    - type: nauc_map_at_1_diff1
      value: 52.57059856776112
    - type: nauc_map_at_1_max
      value: 50.55668152952304
    - type: nauc_map_at_1_std
      value: 1.6572084853398048
    - type: nauc_map_at_20_diff1
      value: 55.75769029917031
    - type: nauc_map_at_20_max
      value: 52.53663737242853
    - type: nauc_map_at_20_std
      value: 2.8489192879814
    - type: nauc_map_at_3_diff1
      value: 56.90294128342709
    - type: nauc_map_at_3_max
      value: 53.10608389782041
    - type: nauc_map_at_3_std
      value: 1.4909731657889491
    - type: nauc_map_at_5_diff1
      value: 56.1258315436073
    - type: nauc_map_at_5_max
      value: 52.398078357541564
    - type: nauc_map_at_5_std
      value: 1.8256862015101467
    - type: nauc_mrr_at_1000_diff1
      value: 55.61987610843598
    - type: nauc_mrr_at_1000_max
      value: 52.506795017152186
    - type: nauc_mrr_at_1000_std
      value: 2.95487192066911
    - type: nauc_mrr_at_100_diff1
      value: 55.598419532054734
    - type: nauc_mrr_at_100_max
      value: 52.48192017040307
    - type: nauc_mrr_at_100_std
      value: 2.930120252521189
    - type: nauc_mrr_at_10_diff1
      value: 56.02309155375198
    - type: nauc_mrr_at_10_max
      value: 52.739573233234424
    - type: nauc_mrr_at_10_std
      value: 2.4073432421641545
    - type: nauc_mrr_at_1_diff1
      value: 52.57059856776112
    - type: nauc_mrr_at_1_max
      value: 50.55668152952304
    - type: nauc_mrr_at_1_std
      value: 1.6572084853398048
    - type: nauc_mrr_at_20_diff1
      value: 55.75769029917031
    - type: nauc_mrr_at_20_max
      value: 52.53663737242853
    - type: nauc_mrr_at_20_std
      value: 2.8489192879814
    - type: nauc_mrr_at_3_diff1
      value: 56.90294128342709
    - type: nauc_mrr_at_3_max
      value: 53.10608389782041
    - type: nauc_mrr_at_3_std
      value: 1.4909731657889491
    - type: nauc_mrr_at_5_diff1
      value: 56.1258315436073
    - type: nauc_mrr_at_5_max
      value: 52.398078357541564
    - type: nauc_mrr_at_5_std
      value: 1.8256862015101467
    - type: nauc_ndcg_at_1000_diff1
      value: 55.30733548408918
    - type: nauc_ndcg_at_1000_max
      value: 53.51143366189318
    - type: nauc_ndcg_at_1000_std
      value: 7.133789405525702
    - type: nauc_ndcg_at_100_diff1
      value: 54.32209039488095
    - type: nauc_ndcg_at_100_max
      value: 52.67499334461009
    - type: nauc_ndcg_at_100_std
      value: 6.878823275077807
    - type: nauc_ndcg_at_10_diff1
      value: 56.266780806997716
    - type: nauc_ndcg_at_10_max
      value: 53.52837255793743
    - type: nauc_ndcg_at_10_std
      value: 3.756832592964262
    - type: nauc_ndcg_at_1_diff1
      value: 52.57059856776112
    - type: nauc_ndcg_at_1_max
      value: 50.55668152952304
    - type: nauc_ndcg_at_1_std
      value: 1.6572084853398048
    - type: nauc_ndcg_at_20_diff1
      value: 55.39255420432796
    - type: nauc_ndcg_at_20_max
      value: 52.946114684072235
    - type: nauc_ndcg_at_20_std
      value: 5.414933414031693
    - type: nauc_ndcg_at_3_diff1
      value: 57.92826624996289
    - type: nauc_ndcg_at_3_max
      value: 53.89907760306972
    - type: nauc_ndcg_at_3_std
      value: 1.6661401245309218
    - type: nauc_ndcg_at_5_diff1
      value: 56.47508936029308
    - type: nauc_ndcg_at_5_max
      value: 52.66800998045517
    - type: nauc_ndcg_at_5_std
      value: 2.4127296184140423
    - type: nauc_precision_at_1000_diff1
      value: 57.25924020238401
    - type: nauc_precision_at_1000_max
      value: 65.1132590931922
    - type: nauc_precision_at_1000_std
      value: 40.60788709618145
    - type: nauc_precision_at_100_diff1
      value: 46.49620002554606
    - type: nauc_precision_at_100_max
      value: 53.02960148167071
    - type: nauc_precision_at_100_std
      value: 28.206028867032863
    - type: nauc_precision_at_10_diff1
      value: 56.562744749606765
    - type: nauc_precision_at_10_max
      value: 56.00594967783547
    - type: nauc_precision_at_10_std
      value: 8.368379831645163
    - type: nauc_precision_at_1_diff1
      value: 52.57059856776112
    - type: nauc_precision_at_1_max
      value: 50.55668152952304
    - type: nauc_precision_at_1_std
      value: 1.6572084853398048
    - type: nauc_precision_at_20_diff1
      value: 53.25915754614111
    - type: nauc_precision_at_20_max
      value: 54.03255118937036
    - type: nauc_precision_at_20_std
      value: 15.161611674272718
    - type: nauc_precision_at_3_diff1
      value: 60.726785748943854
    - type: nauc_precision_at_3_max
      value: 56.139896875869354
    - type: nauc_precision_at_3_std
      value: 2.2306901035769893
    - type: nauc_precision_at_5_diff1
      value: 57.1201127525187
    - type: nauc_precision_at_5_max
      value: 53.28665761862506
    - type: nauc_precision_at_5_std
      value: 4.358720050112237
    - type: nauc_recall_at_1000_diff1
      value: 57.259240202383964
    - type: nauc_recall_at_1000_max
      value: 65.11325909319218
    - type: nauc_recall_at_1000_std
      value: 40.60788709618142
    - type: nauc_recall_at_100_diff1
      value: 46.49620002554603
    - type: nauc_recall_at_100_max
      value: 53.02960148167071
    - type: nauc_recall_at_100_std
      value: 28.206028867032835
    - type: nauc_recall_at_10_diff1
      value: 56.562744749606765
    - type: nauc_recall_at_10_max
      value: 56.00594967783549
    - type: nauc_recall_at_10_std
      value: 8.368379831645147
    - type: nauc_recall_at_1_diff1
      value: 52.57059856776112
    - type: nauc_recall_at_1_max
      value: 50.55668152952304
    - type: nauc_recall_at_1_std
      value: 1.6572084853398048
    - type: nauc_recall_at_20_diff1
      value: 53.259157546141154
    - type: nauc_recall_at_20_max
      value: 54.03255118937038
    - type: nauc_recall_at_20_std
      value: 15.16161167427274
    - type: nauc_recall_at_3_diff1
      value: 60.72678574894387
    - type: nauc_recall_at_3_max
      value: 56.13989687586933
    - type: nauc_recall_at_3_std
      value: 2.2306901035770066
    - type: nauc_recall_at_5_diff1
      value: 57.12011275251864
    - type: nauc_recall_at_5_max
      value: 53.28665761862502
    - type: nauc_recall_at_5_std
      value: 4.3587200501122245
    - type: ndcg_at_1
      value: 30.0
    - type: ndcg_at_10
      value: 38.671
    - type: ndcg_at_100
      value: 42.173
    - type: ndcg_at_1000
      value: 44.016
    - type: ndcg_at_20
      value: 39.845000000000006
    - type: ndcg_at_3
      value: 36.863
    - type: ndcg_at_5
      value: 37.874
    - type: precision_at_1
      value: 30.0
    - type: precision_at_10
      value: 4.65
    - type: precision_at_100
      value: 0.64
    - type: precision_at_1000
      value: 0.08
    - type: precision_at_20
      value: 2.55
    - type: precision_at_3
      value: 13.833
    - type: precision_at_5
      value: 8.799999999999999
    - type: recall_at_1
      value: 30.0
    - type: recall_at_10
      value: 46.5
    - type: recall_at_100
      value: 64.0
    - type: recall_at_1000
      value: 79.5
    - type: recall_at_20
      value: 51.0
    - type: recall_at_3
      value: 41.5
    - type: recall_at_5
      value: 44.0
    task:
      type: Retrieval
  - dataset:
      config: rus
      name: MTEB MultilingualSentimentClassification (rus)
      revision: 2b9b4d10fc589af67794141fe8cbd3739de1eb33
      split: test
      type: mteb/multilingual-sentiment-classification
    metrics:
    - type: accuracy
      value: 79.52710495963092
    - type: ap
      value: 84.5713457178972
    - type: ap_weighted
      value: 84.5713457178972
    - type: f1
      value: 77.88661181524105
    - type: f1_weighted
      value: 79.87563079922718
    - type: main_score
      value: 79.52710495963092
    task:
      type: Classification
  - dataset:
      config: arb_Arab-rus_Cyrl
      name: MTEB NTREXBitextMining (arb_Arab-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 86.47971957936905
    - type: f1
      value: 82.79864240805654
    - type: main_score
      value: 82.79864240805654
    - type: precision
      value: 81.21485800128767
    - type: recall
      value: 86.47971957936905
    task:
      type: BitextMining
  - dataset:
      config: bel_Cyrl-rus_Cyrl
      name: MTEB NTREXBitextMining (bel_Cyrl-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.84226339509264
    - type: f1
      value: 93.56399067465667
    - type: main_score
      value: 93.56399067465667
    - type: precision
      value: 93.01619095309631
    - type: recall
      value: 94.84226339509264
    task:
      type: BitextMining
  - dataset:
      config: ben_Beng-rus_Cyrl
      name: MTEB NTREXBitextMining (ben_Beng-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 92.18828242363544
    - type: f1
      value: 90.42393889620612
    - type: main_score
      value: 90.42393889620612
    - type: precision
      value: 89.67904925153297
    - type: recall
      value: 92.18828242363544
    task:
      type: BitextMining
  - dataset:
      config: bos_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (bos_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.69203805708563
    - type: f1
      value: 93.37172425304624
    - type: main_score
      value: 93.37172425304624
    - type: precision
      value: 92.79204521067315
    - type: recall
      value: 94.69203805708563
    task:
      type: BitextMining
  - dataset:
      config: bul_Cyrl-rus_Cyrl
      name: MTEB NTREXBitextMining (bul_Cyrl-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 96.99549323985978
    - type: f1
      value: 96.13086296110833
    - type: main_score
      value: 96.13086296110833
    - type: precision
      value: 95.72441996327827
    - type: recall
      value: 96.99549323985978
    task:
      type: BitextMining
  - dataset:
      config: ces_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (ces_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.94391587381071
    - type: f1
      value: 94.90680465142157
    - type: main_score
      value: 94.90680465142157
    - type: precision
      value: 94.44541812719079
    - type: recall
      value: 95.94391587381071
    task:
      type: BitextMining
  - dataset:
      config: deu_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (deu_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 96.09414121181773
    - type: f1
      value: 94.94408279085295
    - type: main_score
      value: 94.94408279085295
    - type: precision
      value: 94.41245201135037
    - type: recall
      value: 96.09414121181773
    task:
      type: BitextMining
  - dataset:
      config: ell_Grek-rus_Cyrl
      name: MTEB NTREXBitextMining (ell_Grek-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 96.19429143715573
    - type: f1
      value: 95.12101485561676
    - type: main_score
      value: 95.12101485561676
    - type: precision
      value: 94.60440660991488
    - type: recall
      value: 96.19429143715573
    task:
      type: BitextMining
  - dataset:
      config: eng_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (eng_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 96.49474211316975
    - type: f1
      value: 95.46581777428045
    - type: main_score
      value: 95.46581777428045
    - type: precision
      value: 94.98414288098814
    - type: recall
      value: 96.49474211316975
    task:
      type: BitextMining
  - dataset:
      config: fas_Arab-rus_Cyrl
      name: MTEB NTREXBitextMining (fas_Arab-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.44166249374061
    - type: f1
      value: 92.92383018972905
    - type: main_score
      value: 92.92383018972905
    - type: precision
      value: 92.21957936905358
    - type: recall
      value: 94.44166249374061
    task:
      type: BitextMining
  - dataset:
      config: fin_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (fin_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 92.18828242363544
    - type: f1
      value: 90.2980661468393
    - type: main_score
      value: 90.2980661468393
    - type: precision
      value: 89.42580537472877
    - type: recall
      value: 92.18828242363544
    task:
      type: BitextMining
  - dataset:
      config: fra_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (fra_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.84376564847271
    - type: f1
      value: 94.81054915706895
    - type: main_score
      value: 94.81054915706895
    - type: precision
      value: 94.31369276136427
    - type: recall
      value: 95.84376564847271
    task:
      type: BitextMining
  - dataset:
      config: heb_Hebr-rus_Cyrl
      name: MTEB NTREXBitextMining (heb_Hebr-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.89233850776164
    - type: f1
      value: 93.42513770655985
    - type: main_score
      value: 93.42513770655985
    - type: precision
      value: 92.73493573693875
    - type: recall
      value: 94.89233850776164
    task:
      type: BitextMining
  - dataset:
      config: hin_Deva-rus_Cyrl
      name: MTEB NTREXBitextMining (hin_Deva-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.23985978968453
    - type: f1
      value: 91.52816526376867
    - type: main_score
      value: 91.52816526376867
    - type: precision
      value: 90.76745946425466
    - type: recall
      value: 93.23985978968453
    task:
      type: BitextMining
  - dataset:
      config: hrv_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (hrv_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.99098647971958
    - type: f1
      value: 92.36354531797697
    - type: main_score
      value: 92.36354531797697
    - type: precision
      value: 91.63228970439788
    - type: recall
      value: 93.99098647971958
    task:
      type: BitextMining
  - dataset:
      config: hun_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (hun_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.64046069103655
    - type: f1
      value: 92.05224503421799
    - type: main_score
      value: 92.05224503421799
    - type: precision
      value: 91.33998616973079
    - type: recall
      value: 93.64046069103655
    task:
      type: BitextMining
  - dataset:
      config: ind_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (ind_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 91.68753129694541
    - type: f1
      value: 89.26222667334335
    - type: main_score
      value: 89.26222667334335
    - type: precision
      value: 88.14638624603572
    - type: recall
      value: 91.68753129694541
    task:
      type: BitextMining
  - dataset:
      config: jpn_Jpan-rus_Cyrl
      name: MTEB NTREXBitextMining (jpn_Jpan-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 91.28693039559339
    - type: f1
      value: 89.21161763348957
    - type: main_score
      value: 89.21161763348957
    - type: precision
      value: 88.31188340952988
    - type: recall
      value: 91.28693039559339
    task:
      type: BitextMining
  - dataset:
      config: kor_Hang-rus_Cyrl
      name: MTEB NTREXBitextMining (kor_Hang-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 89.53430145217827
    - type: f1
      value: 86.88322165788365
    - type: main_score
      value: 86.88322165788365
    - type: precision
      value: 85.73950211030831
    - type: recall
      value: 89.53430145217827
    task:
      type: BitextMining
  - dataset:
      config: lit_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (lit_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 90.28542814221332
    - type: f1
      value: 88.10249103814452
    - type: main_score
      value: 88.10249103814452
    - type: precision
      value: 87.17689323973752
    - type: recall
      value: 90.28542814221332
    task:
      type: BitextMining
  - dataset:
      config: mkd_Cyrl-rus_Cyrl
      name: MTEB NTREXBitextMining (mkd_Cyrl-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.04256384576865
    - type: f1
      value: 93.65643703650713
    - type: main_score
      value: 93.65643703650713
    - type: precision
      value: 93.02036387915207
    - type: recall
      value: 95.04256384576865
    task:
      type: BitextMining
  - dataset:
      config: nld_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (nld_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.39308963445168
    - type: f1
      value: 94.16207644800535
    - type: main_score
      value: 94.16207644800535
    - type: precision
      value: 93.582516632091
    - type: recall
      value: 95.39308963445168
    task:
      type: BitextMining
  - dataset:
      config: pol_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (pol_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.7436154231347
    - type: f1
      value: 94.5067601402103
    - type: main_score
      value: 94.5067601402103
    - type: precision
      value: 93.91587381071608
    - type: recall
      value: 95.7436154231347
    task:
      type: BitextMining
  - dataset:
      config: por_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (por_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 65.89884827240861
    - type: f1
      value: 64.61805459419219
    - type: main_score
      value: 64.61805459419219
    - type: precision
      value: 64.07119451106485
    - type: recall
      value: 65.89884827240861
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-arb_Arab
      name: MTEB NTREXBitextMining (rus_Cyrl-arb_Arab)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.2413620430646
    - type: f1
      value: 92.67663399861698
    - type: main_score
      value: 92.67663399861698
    - type: precision
      value: 91.94625271240193
    - type: recall
      value: 94.2413620430646
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bel_Cyrl
      name: MTEB NTREXBitextMining (rus_Cyrl-bel_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.89233850776164
    - type: f1
      value: 93.40343849106993
    - type: main_score
      value: 93.40343849106993
    - type: precision
      value: 92.74077783341679
    - type: recall
      value: 94.89233850776164
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ben_Beng
      name: MTEB NTREXBitextMining (rus_Cyrl-ben_Beng)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.2914371557336
    - type: f1
      value: 92.62226673343348
    - type: main_score
      value: 92.62226673343348
    - type: precision
      value: 91.84610248706393
    - type: recall
      value: 94.2914371557336
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bos_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-bos_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.69354031046569
    - type: f1
      value: 94.50418051319403
    - type: main_score
      value: 94.50418051319403
    - type: precision
      value: 93.95843765648473
    - type: recall
      value: 95.69354031046569
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-bul_Cyrl
      name: MTEB NTREXBitextMining (rus_Cyrl-bul_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.89384076114172
    - type: f1
      value: 94.66199298948423
    - type: main_score
      value: 94.66199298948423
    - type: precision
      value: 94.08028709731263
    - type: recall
      value: 95.89384076114172
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ces_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-ces_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.94091136705057
    - type: f1
      value: 92.3746731207923
    - type: main_score
      value: 92.3746731207923
    - type: precision
      value: 91.66207644800535
    - type: recall
      value: 93.94091136705057
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-deu_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-deu_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.94391587381071
    - type: f1
      value: 94.76214321482223
    - type: main_score
      value: 94.76214321482223
    - type: precision
      value: 94.20380570856285
    - type: recall
      value: 95.94391587381071
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ell_Grek
      name: MTEB NTREXBitextMining (rus_Cyrl-ell_Grek)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.44316474712068
    - type: f1
      value: 94.14788849941579
    - type: main_score
      value: 94.14788849941579
    - type: precision
      value: 93.54197963612084
    - type: recall
      value: 95.44316474712068
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-eng_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-eng_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 98.14722083124687
    - type: f1
      value: 97.57135703555333
    - type: main_score
      value: 97.57135703555333
    - type: precision
      value: 97.2959439158738
    - type: recall
      value: 98.14722083124687
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fas_Arab
      name: MTEB NTREXBitextMining (rus_Cyrl-fas_Arab)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.64196294441662
    - type: f1
      value: 93.24653647137372
    - type: main_score
      value: 93.24653647137372
    - type: precision
      value: 92.60724419963279
    - type: recall
      value: 94.64196294441662
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fin_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-fin_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 87.98197295943916
    - type: f1
      value: 85.23368385912201
    - type: main_score
      value: 85.23368385912201
    - type: precision
      value: 84.08159858835873
    - type: recall
      value: 87.98197295943916
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-fra_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-fra_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 96.24436654982473
    - type: f1
      value: 95.07093974294774
    - type: main_score
      value: 95.07093974294774
    - type: precision
      value: 94.49591053246536
    - type: recall
      value: 96.24436654982473
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-heb_Hebr
      name: MTEB NTREXBitextMining (rus_Cyrl-heb_Hebr)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 91.08662994491738
    - type: f1
      value: 88.5161074945752
    - type: main_score
      value: 88.5161074945752
    - type: precision
      value: 87.36187614755467
    - type: recall
      value: 91.08662994491738
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hin_Deva
      name: MTEB NTREXBitextMining (rus_Cyrl-hin_Deva)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.04256384576865
    - type: f1
      value: 93.66382907694876
    - type: main_score
      value: 93.66382907694876
    - type: precision
      value: 93.05291270238692
    - type: recall
      value: 95.04256384576865
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hrv_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-hrv_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.14271407110667
    - type: f1
      value: 93.7481221832749
    - type: main_score
      value: 93.7481221832749
    - type: precision
      value: 93.10930681736892
    - type: recall
      value: 95.14271407110667
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-hun_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-hun_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 90.18527791687532
    - type: f1
      value: 87.61415933423946
    - type: main_score
      value: 87.61415933423946
    - type: precision
      value: 86.5166400394242
    - type: recall
      value: 90.18527791687532
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ind_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-ind_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.69053580370556
    - type: f1
      value: 91.83608746453012
    - type: main_score
      value: 91.83608746453012
    - type: precision
      value: 90.97145718577868
    - type: recall
      value: 93.69053580370556
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-jpn_Jpan
      name: MTEB NTREXBitextMining (rus_Cyrl-jpn_Jpan)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 89.48422633950926
    - type: f1
      value: 86.91271033534429
    - type: main_score
      value: 86.91271033534429
    - type: precision
      value: 85.82671626487351
    - type: recall
      value: 89.48422633950926
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-kor_Hang
      name: MTEB NTREXBitextMining (rus_Cyrl-kor_Hang)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 88.4827240861292
    - type: f1
      value: 85.35080398375342
    - type: main_score
      value: 85.35080398375342
    - type: precision
      value: 83.9588549490903
    - type: recall
      value: 88.4827240861292
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-lit_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-lit_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 90.33550325488233
    - type: f1
      value: 87.68831819157307
    - type: main_score
      value: 87.68831819157307
    - type: precision
      value: 86.51524906407231
    - type: recall
      value: 90.33550325488233
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-mkd_Cyrl
      name: MTEB NTREXBitextMining (rus_Cyrl-mkd_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.94391587381071
    - type: f1
      value: 94.90402270071775
    - type: main_score
      value: 94.90402270071775
    - type: precision
      value: 94.43915873810715
    - type: recall
      value: 95.94391587381071
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-nld_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-nld_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 92.98948422633951
    - type: f1
      value: 91.04323151393756
    - type: main_score
      value: 91.04323151393756
    - type: precision
      value: 90.14688699716241
    - type: recall
      value: 92.98948422633951
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-pol_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-pol_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.34151226840261
    - type: f1
      value: 92.8726422967785
    - type: main_score
      value: 92.8726422967785
    - type: precision
      value: 92.19829744616925
    - type: recall
      value: 94.34151226840261
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-por_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-por_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 86.17926890335504
    - type: f1
      value: 82.7304882287356
    - type: main_score
      value: 82.7304882287356
    - type: precision
      value: 81.28162481817964
    - type: recall
      value: 86.17926890335504
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-slk_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-slk_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 92.7391086629945
    - type: f1
      value: 90.75112669003506
    - type: main_score
      value: 90.75112669003506
    - type: precision
      value: 89.8564513436822
    - type: recall
      value: 92.7391086629945
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-slv_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-slv_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 92.8893340010015
    - type: f1
      value: 91.05992321816058
    - type: main_score
      value: 91.05992321816058
    - type: precision
      value: 90.22589439715128
    - type: recall
      value: 92.8893340010015
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-spa_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-spa_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 96.49474211316975
    - type: f1
      value: 95.4715406442998
    - type: main_score
      value: 95.4715406442998
    - type: precision
      value: 94.9799699549324
    - type: recall
      value: 96.49474211316975
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-srp_Cyrl
      name: MTEB NTREXBitextMining (rus_Cyrl-srp_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 81.07160741111667
    - type: f1
      value: 76.55687285507015
    - type: main_score
      value: 76.55687285507015
    - type: precision
      value: 74.71886401030116
    - type: recall
      value: 81.07160741111667
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-srp_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-srp_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.14271407110667
    - type: f1
      value: 93.73302377809138
    - type: main_score
      value: 93.73302377809138
    - type: precision
      value: 93.06960440660991
    - type: recall
      value: 95.14271407110667
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-swa_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-swa_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.79218828242364
    - type: f1
      value: 93.25988983475212
    - type: main_score
      value: 93.25988983475212
    - type: precision
      value: 92.53463528626273
    - type: recall
      value: 94.79218828242364
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-swe_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-swe_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.04256384576865
    - type: f1
      value: 93.58704723752295
    - type: main_score
      value: 93.58704723752295
    - type: precision
      value: 92.91437155733601
    - type: recall
      value: 95.04256384576865
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tam_Taml
      name: MTEB NTREXBitextMining (rus_Cyrl-tam_Taml)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.28993490235354
    - type: f1
      value: 91.63912535469872
    - type: main_score
      value: 91.63912535469872
    - type: precision
      value: 90.87738750983617
    - type: recall
      value: 93.28993490235354
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-tur_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-tur_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.74061091637456
    - type: f1
      value: 91.96628275746953
    - type: main_score
      value: 91.96628275746953
    - type: precision
      value: 91.15923885828742
    - type: recall
      value: 93.74061091637456
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-ukr_Cyrl
      name: MTEB NTREXBitextMining (rus_Cyrl-ukr_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.99399098647972
    - type: f1
      value: 94.89567684860624
    - type: main_score
      value: 94.89567684860624
    - type: precision
      value: 94.37072275079286
    - type: recall
      value: 95.99399098647972
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-vie_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-vie_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 91.4371557336004
    - type: f1
      value: 88.98681355366382
    - type: main_score
      value: 88.98681355366382
    - type: precision
      value: 87.89183775663496
    - type: recall
      value: 91.4371557336004
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-zho_Hant
      name: MTEB NTREXBitextMining (rus_Cyrl-zho_Hant)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 92.7891837756635
    - type: f1
      value: 90.79047142141783
    - type: main_score
      value: 90.79047142141783
    - type: precision
      value: 89.86980470706058
    - type: recall
      value: 92.7891837756635
    task:
      type: BitextMining
  - dataset:
      config: rus_Cyrl-zul_Latn
      name: MTEB NTREXBitextMining (rus_Cyrl-zul_Latn)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 87.43114672008012
    - type: f1
      value: 84.04618833011422
    - type: main_score
      value: 84.04618833011422
    - type: precision
      value: 82.52259341393041
    - type: recall
      value: 87.43114672008012
    task:
      type: BitextMining
  - dataset:
      config: slk_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (slk_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.34301452178268
    - type: f1
      value: 94.20392493502158
    - type: main_score
      value: 94.20392493502158
    - type: precision
      value: 93.67384409948257
    - type: recall
      value: 95.34301452178268
    task:
      type: BitextMining
  - dataset:
      config: slv_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (slv_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 92.23835753630446
    - type: f1
      value: 90.5061759305625
    - type: main_score
      value: 90.5061759305625
    - type: precision
      value: 89.74231188051918
    - type: recall
      value: 92.23835753630446
    task:
      type: BitextMining
  - dataset:
      config: spa_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (spa_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 96.54481722583876
    - type: f1
      value: 95.54665331330328
    - type: main_score
      value: 95.54665331330328
    - type: precision
      value: 95.06342847604739
    - type: recall
      value: 96.54481722583876
    task:
      type: BitextMining
  - dataset:
      config: srp_Cyrl-rus_Cyrl
      name: MTEB NTREXBitextMining (srp_Cyrl-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 83.62543815723585
    - type: f1
      value: 80.77095672699816
    - type: main_score
      value: 80.77095672699816
    - type: precision
      value: 79.74674313056886
    - type: recall
      value: 83.62543815723585
    task:
      type: BitextMining
  - dataset:
      config: srp_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (srp_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 94.44166249374061
    - type: f1
      value: 93.00733206591994
    - type: main_score
      value: 93.00733206591994
    - type: precision
      value: 92.37203026762366
    - type: recall
      value: 94.44166249374061
    task:
      type: BitextMining
  - dataset:
      config: swa_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (swa_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 90.23535302954431
    - type: f1
      value: 87.89596482636041
    - type: main_score
      value: 87.89596482636041
    - type: precision
      value: 86.87060227370694
    - type: recall
      value: 90.23535302954431
    task:
      type: BitextMining
  - dataset:
      config: swe_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (swe_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 95.44316474712068
    - type: f1
      value: 94.1896177599733
    - type: main_score
      value: 94.1896177599733
    - type: precision
      value: 93.61542313470206
    - type: recall
      value: 95.44316474712068
    task:
      type: BitextMining
  - dataset:
      config: tam_Taml-rus_Cyrl
      name: MTEB NTREXBitextMining (tam_Taml-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 89.68452679018529
    - type: f1
      value: 87.37341160650037
    - type: main_score
      value: 87.37341160650037
    - type: precision
      value: 86.38389402285247
    - type: recall
      value: 89.68452679018529
    task:
      type: BitextMining
  - dataset:
      config: tur_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (tur_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.89083625438157
    - type: f1
      value: 92.33892505424804
    - type: main_score
      value: 92.33892505424804
    - type: precision
      value: 91.63125640842216
    - type: recall
      value: 93.89083625438157
    task:
      type: BitextMining
  - dataset:
      config: ukr_Cyrl-rus_Cyrl
      name: MTEB NTREXBitextMining (ukr_Cyrl-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 96.14421632448673
    - type: f1
      value: 95.11028447433054
    - type: main_score
      value: 95.11028447433054
    - type: precision
      value: 94.62944416624937
    - type: recall
      value: 96.14421632448673
    task:
      type: BitextMining
  - dataset:
      config: vie_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (vie_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 93.79068602904357
    - type: f1
      value: 92.14989150392256
    - type: main_score
      value: 92.14989150392256
    - type: precision
      value: 91.39292271740945
    - type: recall
      value: 93.79068602904357
    task:
      type: BitextMining
  - dataset:
      config: zho_Hant-rus_Cyrl
      name: MTEB NTREXBitextMining (zho_Hant-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 89.13370055082625
    - type: f1
      value: 86.51514618639217
    - type: main_score
      value: 86.51514618639217
    - type: precision
      value: 85.383920035898
    - type: recall
      value: 89.13370055082625
    task:
      type: BitextMining
  - dataset:
      config: zul_Latn-rus_Cyrl
      name: MTEB NTREXBitextMining (zul_Latn-rus_Cyrl)
      revision: ed9a4403ed4adbfaf4aab56d5b2709e9f6c3ba33
      split: test
      type: mteb/NTREX
    metrics:
    - type: accuracy
      value: 81.17175763645467
    - type: f1
      value: 77.72331766047338
    - type: main_score
      value: 77.72331766047338
    - type: precision
      value: 76.24629555848075
    - type: recall
      value: 81.17175763645467
    task:
      type: BitextMining
  - dataset:
      config: ru
      name: MTEB OpusparcusPC (ru)
      revision: 9e9b1f8ef51616073f47f306f7f47dd91663f86a
      split: test.full
      type: GEM/opusparcus
    metrics:
    - type: cosine_accuracy
      value: 73.09136420525657
    - type: cosine_accuracy_threshold
      value: 87.70400881767273
    - type: cosine_ap
      value: 86.51938550599533
    - type: cosine_f1
      value: 80.84358523725834
    - type: cosine_f1_threshold
      value: 86.90648078918457
    - type: cosine_precision
      value: 73.24840764331209
    - type: cosine_recall
      value: 90.19607843137256
    - type: dot_accuracy
      value: 73.09136420525657
    - type: dot_accuracy_threshold
      value: 87.7040147781372
    - type: dot_ap
      value: 86.51934769946833
    - type: dot_f1
      value: 80.84358523725834
    - type: dot_f1_threshold
      value: 86.90648078918457
    - type: dot_precision
      value: 73.24840764331209
    - type: dot_recall
      value: 90.19607843137256
    - type: euclidean_accuracy
      value: 73.09136420525657
    - type: euclidean_accuracy_threshold
      value: 49.590304493904114
    - type: euclidean_ap
      value: 86.51934769946833
    - type: euclidean_f1
      value: 80.84358523725834
    - type: euclidean_f1_threshold
      value: 51.173269748687744
    - type: euclidean_precision
      value: 73.24840764331209
    - type: euclidean_recall
      value: 90.19607843137256
    - type: main_score
      value: 86.51976811057995
    - type: manhattan_accuracy
      value: 73.40425531914893
    - type: manhattan_accuracy_threshold
      value: 757.8278541564941
    - type: manhattan_ap
      value: 86.51976811057995
    - type: manhattan_f1
      value: 80.92898615453328
    - type: manhattan_f1_threshold
      value: 778.3821105957031
    - type: manhattan_precision
      value: 74.32321575061526
    - type: manhattan_recall
      value: 88.8235294117647
    - type: max_ap
      value: 86.51976811057995
    - type: max_f1
      value: 80.92898615453328
    - type: max_precision
      value: 74.32321575061526
    - type: max_recall
      value: 90.19607843137256
    - type: similarity_accuracy
      value: 73.09136420525657
    - type: similarity_accuracy_threshold
      value: 87.70400881767273
    - type: similarity_ap
      value: 86.51938550599533
    - type: similarity_f1
      value: 80.84358523725834
    - type: similarity_f1_threshold
      value: 86.90648078918457
    - type: similarity_precision
      value: 73.24840764331209
    - type: similarity_recall
      value: 90.19607843137256
    task:
      type: PairClassification
  - dataset:
      config: russian
      name: MTEB PublicHealthQA (russian)
      revision: main
      split: test
      type: xhluca/publichealth-qa
    metrics:
    - type: main_score
      value: 79.303
    - type: map_at_1
      value: 61.538000000000004
    - type: map_at_10
      value: 74.449
    - type: map_at_100
      value: 74.687
    - type: map_at_1000
      value: 74.687
    - type: map_at_20
      value: 74.589
    - type: map_at_3
      value: 73.333
    - type: map_at_5
      value: 74.256
    - type: mrr_at_1
      value: 61.53846153846154
    - type: mrr_at_10
      value: 74.44871794871794
    - type: mrr_at_100
      value: 74.68730304304074
    - type: mrr_at_1000
      value: 74.68730304304074
    - type: mrr_at_20
      value: 74.58857808857809
    - type: mrr_at_3
      value: 73.33333333333333
    - type: mrr_at_5
      value: 74.25641025641025
    - type: nauc_map_at_1000_diff1
      value: 61.375798048778506
    - type: nauc_map_at_1000_max
      value: 51.37093181241067
    - type: nauc_map_at_1000_std
      value: 41.735794471409015
    - type: nauc_map_at_100_diff1
      value: 61.375798048778506
    - type: nauc_map_at_100_max
      value: 51.37093181241067
    - type: nauc_map_at_100_std
      value: 41.735794471409015
    - type: nauc_map_at_10_diff1
      value: 61.12796039757213
    - type: nauc_map_at_10_max
      value: 51.843445267118014
    - type: nauc_map_at_10_std
      value: 42.243121474939365
    - type: nauc_map_at_1_diff1
      value: 66.39100974909151
    - type: nauc_map_at_1_max
      value: 44.77165601342703
    - type: nauc_map_at_1_std
      value: 32.38542979413408
    - type: nauc_map_at_20_diff1
      value: 61.16611123434347
    - type: nauc_map_at_20_max
      value: 51.52605092407306
    - type: nauc_map_at_20_std
      value: 41.94787773313971
    - type: nauc_map_at_3_diff1
      value: 61.40157474408937
    - type: nauc_map_at_3_max
      value: 51.47230077853947
    - type: nauc_map_at_3_std
      value: 42.63540269440141
    - type: nauc_map_at_5_diff1
      value: 61.07631147583098
    - type: nauc_map_at_5_max
      value: 52.02626939341523
    - type: nauc_map_at_5_std
      value: 42.511607332150334
    - type: nauc_mrr_at_1000_diff1
      value: 61.375798048778506
    - type: nauc_mrr_at_1000_max
      value: 51.37093181241067
    - type: nauc_mrr_at_1000_std
      value: 41.735794471409015
    - type: nauc_mrr_at_100_diff1
      value: 61.375798048778506
    - type: nauc_mrr_at_100_max
      value: 51.37093181241067
    - type: nauc_mrr_at_100_std
      value: 41.735794471409015
    - type: nauc_mrr_at_10_diff1
      value: 61.12796039757213
    - type: nauc_mrr_at_10_max
      value: 51.843445267118014
    - type: nauc_mrr_at_10_std
      value: 42.243121474939365
    - type: nauc_mrr_at_1_diff1
      value: 66.39100974909151
    - type: nauc_mrr_at_1_max
      value: 44.77165601342703
    - type: nauc_mrr_at_1_std
      value: 32.38542979413408
    - type: nauc_mrr_at_20_diff1
      value: 61.16611123434347
    - type: nauc_mrr_at_20_max
      value: 51.52605092407306
    - type: nauc_mrr_at_20_std
      value: 41.94787773313971
    - type: nauc_mrr_at_3_diff1
      value: 61.40157474408937
    - type: nauc_mrr_at_3_max
      value: 51.47230077853947
    - type: nauc_mrr_at_3_std
      value: 42.63540269440141
    - type: nauc_mrr_at_5_diff1
      value: 61.07631147583098
    - type: nauc_mrr_at_5_max
      value: 52.02626939341523
    - type: nauc_mrr_at_5_std
      value: 42.511607332150334
    - type: nauc_ndcg_at_1000_diff1
      value: 60.54821630436157
    - type: nauc_ndcg_at_1000_max
      value: 52.584328363863634
    - type: nauc_ndcg_at_1000_std
      value: 43.306961101645946
    - type: nauc_ndcg_at_100_diff1
      value: 60.54821630436157
    - type: nauc_ndcg_at_100_max
      value: 52.584328363863634
    - type: nauc_ndcg_at_100_std
      value: 43.306961101645946
    - type: nauc_ndcg_at_10_diff1
      value: 58.800340278109886
    - type: nauc_ndcg_at_10_max
      value: 55.31050771670664
    - type: nauc_ndcg_at_10_std
      value: 46.40931672942848
    - type: nauc_ndcg_at_1_diff1
      value: 66.39100974909151
    - type: nauc_ndcg_at_1_max
      value: 44.77165601342703
    - type: nauc_ndcg_at_1_std
      value: 32.38542979413408
    - type: nauc_ndcg_at_20_diff1
      value: 58.88690479697946
    - type: nauc_ndcg_at_20_max
      value: 54.19269661177923
    - type: nauc_ndcg_at_20_std
      value: 45.39305589413174
    - type: nauc_ndcg_at_3_diff1
      value: 59.61866351451574
    - type: nauc_ndcg_at_3_max
      value: 54.23992718744033
    - type: nauc_ndcg_at_3_std
      value: 46.997379274101
    - type: nauc_ndcg_at_5_diff1
      value: 58.70739588066225
    - type: nauc_ndcg_at_5_max
      value: 55.76766902539152
    - type: nauc_ndcg_at_5_std
      value: 47.10553115762958
    - type: nauc_precision_at_1000_diff1
      value: 100.0
    - type: nauc_precision_at_1000_max
      value: 100.0
    - type: nauc_precision_at_1000_std
      value: 100.0
    - type: nauc_precision_at_100_diff1
      value: .nan
    - type: nauc_precision_at_100_max
      value: .nan
    - type: nauc_precision_at_100_std
      value: .nan
    - type: nauc_precision_at_10_diff1
      value: 35.72622112397501
    - type: nauc_precision_at_10_max
      value: 89.84297108673948
    - type: nauc_precision_at_10_std
      value: 86.60269192422707
    - type: nauc_precision_at_1_diff1
      value: 66.39100974909151
    - type: nauc_precision_at_1_max
      value: 44.77165601342703
    - type: nauc_precision_at_1_std
      value: 32.38542979413408
    - type: nauc_precision_at_20_diff1
      value: 29.188449183726433
    - type: nauc_precision_at_20_max
      value: 86.45729478231968
    - type: nauc_precision_at_20_std
      value: 86.45729478231968
    - type: nauc_precision_at_3_diff1
      value: 50.294126629236224
    - type: nauc_precision_at_3_max
      value: 68.98223127174579
    - type: nauc_precision_at_3_std
      value: 70.31195520376356
    - type: nauc_precision_at_5_diff1
      value: 39.648884288124385
    - type: nauc_precision_at_5_max
      value: 86.3409770687935
    - type: nauc_precision_at_5_std
      value: 83.74875373878356
    - type: nauc_recall_at_1000_diff1
      value: .nan
    - type: nauc_recall_at_1000_max
      value: .nan
    - type: nauc_recall_at_1000_std
      value: .nan
    - type: nauc_recall_at_100_diff1
      value: .nan
    - type: nauc_recall_at_100_max
      value: .nan
    - type: nauc_recall_at_100_std
      value: .nan
    - type: nauc_recall_at_10_diff1
      value: 35.72622112397516
    - type: nauc_recall_at_10_max
      value: 89.84297108673968
    - type: nauc_recall_at_10_std
      value: 86.60269192422749
    - type: nauc_recall_at_1_diff1
      value: 66.39100974909151
    - type: nauc_recall_at_1_max
      value: 44.77165601342703
    - type: nauc_recall_at_1_std
      value: 32.38542979413408
    - type: nauc_recall_at_20_diff1
      value: 29.188449183726323
    - type: nauc_recall_at_20_max
      value: 86.45729478231985
    - type: nauc_recall_at_20_std
      value: 86.45729478231985
    - type: nauc_recall_at_3_diff1
      value: 50.29412662923603
    - type: nauc_recall_at_3_max
      value: 68.98223127174562
    - type: nauc_recall_at_3_std
      value: 70.31195520376346
    - type: nauc_recall_at_5_diff1
      value: 39.64888428812445
    - type: nauc_recall_at_5_max
      value: 86.34097706879359
    - type: nauc_recall_at_5_std
      value: 83.74875373878366
    - type: ndcg_at_1
      value: 61.538000000000004
    - type: ndcg_at_10
      value: 79.303
    - type: ndcg_at_100
      value: 80.557
    - type: ndcg_at_1000
      value: 80.557
    - type: ndcg_at_20
      value: 79.732
    - type: ndcg_at_3
      value: 77.033
    - type: ndcg_at_5
      value: 78.818
    - type: precision_at_1
      value: 61.538000000000004
    - type: precision_at_10
      value: 9.385
    - type: precision_at_100
      value: 1.0
    - type: precision_at_1000
      value: 0.1
    - type: precision_at_20
      value: 4.769
    - type: precision_at_3
      value: 29.231
    - type: precision_at_5
      value: 18.462
    - type: recall_at_1
      value: 61.538000000000004
    - type: recall_at_10
      value: 93.84599999999999
    - type: recall_at_100
      value: 100.0
    - type: recall_at_1000
      value: 100.0
    - type: recall_at_20
      value: 95.38499999999999
    - type: recall_at_3
      value: 87.69200000000001
    - type: recall_at_5
      value: 92.308
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB RUParaPhraserSTS (default)
      revision: 43265056790b8f7c59e0139acb4be0a8dad2c8f4
      split: test
      type: merionum/ru_paraphraser
    metrics:
    - type: cosine_pearson
      value: 64.73554596215753
    - type: cosine_spearman
      value: 70.45849652271855
    - type: euclidean_pearson
      value: 68.08069844834267
    - type: euclidean_spearman
      value: 70.45854872959124
    - type: main_score
      value: 70.45849652271855
    - type: manhattan_pearson
      value: 67.88325986519624
    - type: manhattan_spearman
      value: 70.21131896834542
    - type: pearson
      value: 64.73554596215753
    - type: spearman
      value: 70.45849652271855
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB RiaNewsRetrieval (default)
      revision: 82374b0bbacda6114f39ff9c5b925fa1512ca5d7
      split: test
      type: ai-forever/ria-news-retrieval
    metrics:
    - type: main_score
      value: 70.00999999999999
    - type: map_at_1
      value: 55.97
    - type: map_at_10
      value: 65.59700000000001
    - type: map_at_100
      value: 66.057
    - type: map_at_1000
      value: 66.074
    - type: map_at_20
      value: 65.892
    - type: map_at_3
      value: 63.74999999999999
    - type: map_at_5
      value: 64.84299999999999
    - type: mrr_at_1
      value: 55.88999999999999
    - type: mrr_at_10
      value: 65.55873015872977
    - type: mrr_at_100
      value: 66.01891495129716
    - type: mrr_at_1000
      value: 66.03538391493299
    - type: mrr_at_20
      value: 65.85351193431555
    - type: mrr_at_3
      value: 63.7133333333329
    - type: mrr_at_5
      value: 64.80483333333268
    - type: nauc_map_at_1000_diff1
      value: 65.95332946436318
    - type: nauc_map_at_1000_max
      value: 28.21204156197811
    - type: nauc_map_at_1000_std
      value: -13.139245767083743
    - type: nauc_map_at_100_diff1
      value: 65.94763105024367
    - type: nauc_map_at_100_max
      value: 28.212832170078205
    - type: nauc_map_at_100_std
      value: -13.131425849370665
    - type: nauc_map_at_10_diff1
      value: 65.88455089448388
    - type: nauc_map_at_10_max
      value: 28.13555838776792
    - type: nauc_map_at_10_std
      value: -13.326989827081023
    - type: nauc_map_at_1_diff1
      value: 69.31275711813979
    - type: nauc_map_at_1_max
      value: 26.386708520283758
    - type: nauc_map_at_1_std
      value: -14.434616447245464
    - type: nauc_map_at_20_diff1
      value: 65.91227032605677
    - type: nauc_map_at_20_max
      value: 28.20538655600886
    - type: nauc_map_at_20_std
      value: -13.191148834410274
    - type: nauc_map_at_3_diff1
      value: 66.0051677952641
    - type: nauc_map_at_3_max
      value: 28.25443420019022
    - type: nauc_map_at_3_std
      value: -13.893284109029558
    - type: nauc_map_at_5_diff1
      value: 65.89784348297898
    - type: nauc_map_at_5_max
      value: 28.26449765184183
    - type: nauc_map_at_5_std
      value: -13.506692912805008
    - type: nauc_mrr_at_1000_diff1
      value: 66.06599513750889
    - type: nauc_mrr_at_1000_max
      value: 28.191556650722287
    - type: nauc_mrr_at_1000_std
      value: -13.098487982930276
    - type: nauc_mrr_at_100_diff1
      value: 66.0602307977725
    - type: nauc_mrr_at_100_max
      value: 28.19235936624514
    - type: nauc_mrr_at_100_std
      value: -13.09069677716269
    - type: nauc_mrr_at_10_diff1
      value: 65.99546819079403
    - type: nauc_mrr_at_10_max
      value: 28.11556170120022
    - type: nauc_mrr_at_10_std
      value: -13.286711073897553
    - type: nauc_mrr_at_1_diff1
      value: 69.49541040517995
    - type: nauc_mrr_at_1_max
      value: 26.354622707276153
    - type: nauc_mrr_at_1_std
      value: -14.358839778104695
    - type: nauc_mrr_at_20_diff1
      value: 66.02427154257936
    - type: nauc_mrr_at_20_max
      value: 28.18509383563462
    - type: nauc_mrr_at_20_std
      value: -13.150543398429
    - type: nauc_mrr_at_3_diff1
      value: 66.11258119082618
    - type: nauc_mrr_at_3_max
      value: 28.239510722224004
    - type: nauc_mrr_at_3_std
      value: -13.857249251136269
    - type: nauc_mrr_at_5_diff1
      value: 66.00633786765626
    - type: nauc_mrr_at_5_max
      value: 28.244875152193032
    - type: nauc_mrr_at_5_std
      value: -13.467206028704434
    - type: nauc_ndcg_at_1000_diff1
      value: 65.02876183314446
    - type: nauc_ndcg_at_1000_max
      value: 29.109368390197194
    - type: nauc_ndcg_at_1000_std
      value: -11.56514359821697
    - type: nauc_ndcg_at_100_diff1
      value: 64.85837726893713
    - type: nauc_ndcg_at_100_max
      value: 29.19990133137256
    - type: nauc_ndcg_at_100_std
      value: -11.17450348161257
    - type: nauc_ndcg_at_10_diff1
      value: 64.53842705024796
    - type: nauc_ndcg_at_10_max
      value: 28.748734006088526
    - type: nauc_ndcg_at_10_std
      value: -12.331395505957063
    - type: nauc_ndcg_at_1_diff1
      value: 69.31275711813979
    - type: nauc_ndcg_at_1_max
      value: 26.386708520283758
    - type: nauc_ndcg_at_1_std
      value: -14.434616447245464
    - type: nauc_ndcg_at_20_diff1
      value: 64.59017606740504
    - type: nauc_ndcg_at_20_max
      value: 29.047332048898017
    - type: nauc_ndcg_at_20_std
      value: -11.746548770195954
    - type: nauc_ndcg_at_3_diff1
      value: 64.87900935713822
    - type: nauc_ndcg_at_3_max
      value: 28.953157521204403
    - type: nauc_ndcg_at_3_std
      value: -13.639947228880942
    - type: nauc_ndcg_at_5_diff1
      value: 64.61466953479034
    - type: nauc_ndcg_at_5_max
      value: 29.01899321868392
    - type: nauc_ndcg_at_5_std
      value: -12.85356404799802
    - type: nauc_precision_at_1000_diff1
      value: 48.85481417002382
    - type: nauc_precision_at_1000_max
      value: 57.129837326696375
    - type: nauc_precision_at_1000_std
      value: 37.889524999906435
    - type: nauc_precision_at_100_diff1
      value: 53.374672326788264
    - type: nauc_precision_at_100_max
      value: 43.819333062207974
    - type: nauc_precision_at_100_std
      value: 21.387064885769362
    - type: nauc_precision_at_10_diff1
      value: 57.66571169774445
    - type: nauc_precision_at_10_max
      value: 31.779694837242033
    - type: nauc_precision_at_10_std
      value: -6.6248399147180255
    - type: nauc_precision_at_1_diff1
      value: 69.31275711813979
    - type: nauc_precision_at_1_max
      value: 26.386708520283758
    - type: nauc_precision_at_1_std
      value: -14.434616447245464
    - type: nauc_precision_at_20_diff1
      value: 55.93570036001682
    - type: nauc_precision_at_20_max
      value: 34.98640173388743
    - type: nauc_precision_at_20_std
      value: -0.36518465159326174
    - type: nauc_precision_at_3_diff1
      value: 60.94100093991508
    - type: nauc_precision_at_3_max
      value: 31.422239034357673
    - type: nauc_precision_at_3_std
      value: -12.72576556537896
    - type: nauc_precision_at_5_diff1
      value: 59.450505195434054
    - type: nauc_precision_at_5_max
      value: 32.07638712418377
    - type: nauc_precision_at_5_std
      value: -10.024459103498598
    - type: nauc_recall_at_1000_diff1
      value: 48.854814170024184
    - type: nauc_recall_at_1000_max
      value: 57.129837326697164
    - type: nauc_recall_at_1000_std
      value: 37.88952499990672
    - type: nauc_recall_at_100_diff1
      value: 53.37467232678822
    - type: nauc_recall_at_100_max
      value: 43.8193330622079
    - type: nauc_recall_at_100_std
      value: 21.387064885769398
    - type: nauc_recall_at_10_diff1
      value: 57.66571169774447
    - type: nauc_recall_at_10_max
      value: 31.779694837242133
    - type: nauc_recall_at_10_std
      value: -6.62483991471789
    - type: nauc_recall_at_1_diff1
      value: 69.31275711813979
    - type: nauc_recall_at_1_max
      value: 26.386708520283758
    - type: nauc_recall_at_1_std
      value: -14.434616447245464
    - type: nauc_recall_at_20_diff1
      value: 55.93570036001682
    - type: nauc_recall_at_20_max
      value: 34.986401733887554
    - type: nauc_recall_at_20_std
      value: -0.3651846515931506
    - type: nauc_recall_at_3_diff1
      value: 60.94100093991499
    - type: nauc_recall_at_3_max
      value: 31.422239034357606
    - type: nauc_recall_at_3_std
      value: -12.725765565378966
    - type: nauc_recall_at_5_diff1
      value: 59.450505195434125
    - type: nauc_recall_at_5_max
      value: 32.07638712418387
    - type: nauc_recall_at_5_std
      value: -10.024459103498472
    - type: ndcg_at_1
      value: 55.97
    - type: ndcg_at_10
      value: 70.00999999999999
    - type: ndcg_at_100
      value: 72.20100000000001
    - type: ndcg_at_1000
      value: 72.65599999999999
    - type: ndcg_at_20
      value: 71.068
    - type: ndcg_at_3
      value: 66.228
    - type: ndcg_at_5
      value: 68.191
    - type: precision_at_1
      value: 55.97
    - type: precision_at_10
      value: 8.373999999999999
    - type: precision_at_100
      value: 0.9390000000000001
    - type: precision_at_1000
      value: 0.097
    - type: precision_at_20
      value: 4.3950000000000005
    - type: precision_at_3
      value: 24.46
    - type: precision_at_5
      value: 15.626000000000001
    - type: recall_at_1
      value: 55.97
    - type: recall_at_10
      value: 83.74000000000001
    - type: recall_at_100
      value: 93.87
    - type: recall_at_1000
      value: 97.49
    - type: recall_at_20
      value: 87.89
    - type: recall_at_3
      value: 73.38
    - type: recall_at_5
      value: 78.13
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB RuBQReranking (default)
      revision: 2e96b8f098fa4b0950fc58eacadeb31c0d0c7fa2
      split: test
      type: ai-forever/rubq-reranking
    metrics:
    - type: main_score
      value: 71.44929565043827
    - type: map
      value: 71.44929565043827
    - type: mrr
      value: 77.78391820945014
    - type: nAUC_map_diff1
      value: 38.140840668080244
    - type: nAUC_map_max
      value: 27.54328688105381
    - type: nAUC_map_std
      value: 16.81572082284672
    - type: nAUC_mrr_diff1
      value: 44.51350415961509
    - type: nAUC_mrr_max
      value: 36.491182016669754
    - type: nAUC_mrr_std
      value: 22.47139593052269
    task:
      type: Reranking
  - dataset:
      config: default
      name: MTEB RuBQRetrieval (default)
      revision: e19b6ffa60b3bc248e0b41f4cc37c26a55c2a67b
      split: test
      type: ai-forever/rubq-retrieval
    metrics:
    - type: main_score
      value: 68.529
    - type: map_at_1
      value: 42.529
    - type: map_at_10
      value: 60.864
    - type: map_at_100
      value: 61.868
    - type: map_at_1000
      value: 61.907000000000004
    - type: map_at_20
      value: 61.596
    - type: map_at_3
      value: 55.701
    - type: map_at_5
      value: 58.78
    - type: mrr_at_1
      value: 60.57919621749409
    - type: mrr_at_10
      value: 70.55614188149649
    - type: mrr_at_100
      value: 70.88383816664494
    - type: mrr_at_1000
      value: 70.89719252668833
    - type: mrr_at_20
      value: 70.79839750105347
    - type: mrr_at_3
      value: 68.4594168636722
    - type: mrr_at_5
      value: 69.67100078802214
    - type: nauc_map_at_1000_diff1
      value: 40.67438785660885
    - type: nauc_map_at_1000_max
      value: 32.79981738507424
    - type: nauc_map_at_1000_std
      value: -6.873402600044831
    - type: nauc_map_at_100_diff1
      value: 40.65643664443284
    - type: nauc_map_at_100_max
      value: 32.81594799919249
    - type: nauc_map_at_100_std
      value: -6.8473246794498195
    - type: nauc_map_at_10_diff1
      value: 40.39048268484908
    - type: nauc_map_at_10_max
      value: 32.403242161479525
    - type: nauc_map_at_10_std
      value: -7.344413799841244
    - type: nauc_map_at_1_diff1
      value: 44.36306892906905
    - type: nauc_map_at_1_max
      value: 25.61348630699028
    - type: nauc_map_at_1_std
      value: -8.713074613333902
    - type: nauc_map_at_20_diff1
      value: 40.530326570124615
    - type: nauc_map_at_20_max
      value: 32.74028319323205
    - type: nauc_map_at_20_std
      value: -7.008180779820569
    - type: nauc_map_at_3_diff1
      value: 40.764924859364044
    - type: nauc_map_at_3_max
      value: 29.809671682025336
    - type: nauc_map_at_3_std
      value: -9.205620202725564
    - type: nauc_map_at_5_diff1
      value: 40.88599496021476
    - type: nauc_map_at_5_max
      value: 32.1701894666848
    - type: nauc_map_at_5_std
      value: -7.801251849010623
    - type: nauc_mrr_at_1000_diff1
      value: 48.64181373540728
    - type: nauc_mrr_at_1000_max
      value: 40.136947990653546
    - type: nauc_mrr_at_1000_std
      value: -7.250260497468805
    - type: nauc_mrr_at_100_diff1
      value: 48.63349902496212
    - type: nauc_mrr_at_100_max
      value: 40.14510559704008
    - type: nauc_mrr_at_100_std
      value: -7.228702374801103
    - type: nauc_mrr_at_10_diff1
      value: 48.58580560194813
    - type: nauc_mrr_at_10_max
      value: 40.15075599433366
    - type: nauc_mrr_at_10_std
      value: -7.267928771548688
    - type: nauc_mrr_at_1_diff1
      value: 51.47535097164919
    - type: nauc_mrr_at_1_max
      value: 38.23579750430856
    - type: nauc_mrr_at_1_std
      value: -9.187785187137633
    - type: nauc_mrr_at_20_diff1
      value: 48.58688378336222
    - type: nauc_mrr_at_20_max
      value: 40.13408744088299
    - type: nauc_mrr_at_20_std
      value: -7.283132775160146
    - type: nauc_mrr_at_3_diff1
      value: 48.66833005454742
    - type: nauc_mrr_at_3_max
      value: 40.07987333638038
    - type: nauc_mrr_at_3_std
      value: -7.738819947521418
    - type: nauc_mrr_at_5_diff1
      value: 48.76536305941537
    - type: nauc_mrr_at_5_max
      value: 40.381929739522185
    - type: nauc_mrr_at_5_std
      value: -7.592858318378928
    - type: nauc_ndcg_at_1000_diff1
      value: 41.67304442004693
    - type: nauc_ndcg_at_1000_max
      value: 35.84126926253235
    - type: nauc_ndcg_at_1000_std
      value: -4.78971011604655
    - type: nauc_ndcg_at_100_diff1
      value: 41.16918850185783
    - type: nauc_ndcg_at_100_max
      value: 36.082461962326505
    - type: nauc_ndcg_at_100_std
      value: -4.092442251697269
    - type: nauc_ndcg_at_10_diff1
      value: 40.300065598615205
    - type: nauc_ndcg_at_10_max
      value: 34.87866296788365
    - type: nauc_ndcg_at_10_std
      value: -5.866529277842453
    - type: nauc_ndcg_at_1_diff1
      value: 51.74612915209495
    - type: nauc_ndcg_at_1_max
      value: 37.71907067970078
    - type: nauc_ndcg_at_1_std
      value: -9.064124266098696
    - type: nauc_ndcg_at_20_diff1
      value: 40.493949850214584
    - type: nauc_ndcg_at_20_max
      value: 35.69331503650286
    - type: nauc_ndcg_at_20_std
      value: -4.995310342975443
    - type: nauc_ndcg_at_3_diff1
      value: 41.269443212112364
    - type: nauc_ndcg_at_3_max
      value: 32.572844460953334
    - type: nauc_ndcg_at_3_std
      value: -9.063015396458791
    - type: nauc_ndcg_at_5_diff1
      value: 41.37039652522888
    - type: nauc_ndcg_at_5_max
      value: 34.67416011393571
    - type: nauc_ndcg_at_5_std
      value: -7.106845569862319
    - type: nauc_precision_at_1000_diff1
      value: -9.571769961090155
    - type: nauc_precision_at_1000_max
      value: 5.574782583417188
    - type: nauc_precision_at_1000_std
      value: 7.28333847923847
    - type: nauc_precision_at_100_diff1
      value: -7.7405012003383735
    - type: nauc_precision_at_100_max
      value: 9.67745355070353
    - type: nauc_precision_at_100_std
      value: 9.327890294080992
    - type: nauc_precision_at_10_diff1
      value: -1.006879647532931
    - type: nauc_precision_at_10_max
      value: 15.899825481231064
    - type: nauc_precision_at_10_std
      value: 4.2284084852153105
    - type: nauc_precision_at_1_diff1
      value: 51.74612915209495
    - type: nauc_precision_at_1_max
      value: 37.71907067970078
    - type: nauc_precision_at_1_std
      value: -9.064124266098696
    - type: nauc_precision_at_20_diff1
      value: -4.982301544401409
    - type: nauc_precision_at_20_max
      value: 13.241674471380568
    - type: nauc_precision_at_20_std
      value: 7.052280133821539
    - type: nauc_precision_at_3_diff1
      value: 15.442614376387374
    - type: nauc_precision_at_3_max
      value: 25.12695418083
    - type: nauc_precision_at_3_std
      value: -3.1150066697920638
    - type: nauc_precision_at_5_diff1
      value: 8.381026072692444
    - type: nauc_precision_at_5_max
      value: 22.839056540604822
    - type: nauc_precision_at_5_std
      value: 1.5126905486524331
    - type: nauc_recall_at_1000_diff1
      value: -0.8869709920433502
    - type: nauc_recall_at_1000_max
      value: 45.092324433377264
    - type: nauc_recall_at_1000_std
      value: 62.21264093315108
    - type: nauc_recall_at_100_diff1
      value: 16.036715011075714
    - type: nauc_recall_at_100_max
      value: 39.79963411771158
    - type: nauc_recall_at_100_std
      value: 28.41850069503361
    - type: nauc_recall_at_10_diff1
      value: 25.189622794479998
    - type: nauc_recall_at_10_max
      value: 30.82355277039427
    - type: nauc_recall_at_10_std
      value: 0.0964544736531047
    - type: nauc_recall_at_1_diff1
      value: 44.36306892906905
    - type: nauc_recall_at_1_max
      value: 25.61348630699028
    - type: nauc_recall_at_1_std
      value: -8.713074613333902
    - type: nauc_recall_at_20_diff1
      value: 20.43424504746087
    - type: nauc_recall_at_20_max
      value: 33.96010554649377
    - type: nauc_recall_at_20_std
      value: 6.900984030301936
    - type: nauc_recall_at_3_diff1
      value: 33.86531858793492
    - type: nauc_recall_at_3_max
      value: 27.725692256711188
    - type: nauc_recall_at_3_std
      value: -8.533124289305709
    - type: nauc_recall_at_5_diff1
      value: 32.006964557701686
    - type: nauc_recall_at_5_max
      value: 31.493370659289806
    - type: nauc_recall_at_5_std
      value: -4.8639793547793255
    - type: ndcg_at_1
      value: 60.461
    - type: ndcg_at_10
      value: 68.529
    - type: ndcg_at_100
      value: 71.664
    - type: ndcg_at_1000
      value: 72.396
    - type: ndcg_at_20
      value: 70.344
    - type: ndcg_at_3
      value: 61.550000000000004
    - type: ndcg_at_5
      value: 64.948
    - type: precision_at_1
      value: 60.461
    - type: precision_at_10
      value: 13.28
    - type: precision_at_100
      value: 1.555
    - type: precision_at_1000
      value: 0.164
    - type: precision_at_20
      value: 7.216
    - type: precision_at_3
      value: 33.077
    - type: precision_at_5
      value: 23.014000000000003
    - type: recall_at_1
      value: 42.529
    - type: recall_at_10
      value: 81.169
    - type: recall_at_100
      value: 93.154
    - type: recall_at_1000
      value: 98.18299999999999
    - type: recall_at_20
      value: 87.132
    - type: recall_at_3
      value: 63.905
    - type: recall_at_5
      value: 71.967
    task:
      type: Retrieval
  - dataset:
      config: default
      name: MTEB RuReviewsClassification (default)
      revision: f6d2c31f4dc6b88f468552750bfec05b4b41b05a
      split: test
      type: ai-forever/ru-reviews-classification
    metrics:
    - type: accuracy
      value: 61.17675781250001
    - type: f1
      value: 60.354535346041374
    - type: f1_weighted
      value: 60.35437313166116
    - type: main_score
      value: 61.17675781250001
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB RuSTSBenchmarkSTS (default)
      revision: 7cf24f325c6da6195df55bef3d86b5e0616f3018
      split: test
      type: ai-forever/ru-stsbenchmark-sts
    metrics:
    - type: cosine_pearson
      value: 78.1301041727274
    - type: cosine_spearman
      value: 78.08238025421747
    - type: euclidean_pearson
      value: 77.35224254583635
    - type: euclidean_spearman
      value: 78.08235336582496
    - type: main_score
      value: 78.08238025421747
    - type: manhattan_pearson
      value: 77.24138550052075
    - type: manhattan_spearman
      value: 77.98199107904142
    - type: pearson
      value: 78.1301041727274
    - type: spearman
      value: 78.08238025421747
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB RuSciBenchGRNTIClassification (default)
      revision: 673a610d6d3dd91a547a0d57ae1b56f37ebbf6a1
      split: test
      type: ai-forever/ru-scibench-grnti-classification
    metrics:
    - type: accuracy
      value: 54.990234375
    - type: f1
      value: 53.537019057131374
    - type: f1_weighted
      value: 53.552745354520766
    - type: main_score
      value: 54.990234375
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB RuSciBenchGRNTIClusteringP2P (default)
      revision: 673a610d6d3dd91a547a0d57ae1b56f37ebbf6a1
      split: test
      type: ai-forever/ru-scibench-grnti-classification
    metrics:
    - type: main_score
      value: 50.775228895355106
    - type: v_measure
      value: 50.775228895355106
    - type: v_measure_std
      value: 0.9533571150165796
    task:
      type: Clustering
  - dataset:
      config: default
      name: MTEB RuSciBenchOECDClassification (default)
      revision: 26c88e99dcaba32bb45d0e1bfc21902337f6d471
      split: test
      type: ai-forever/ru-scibench-oecd-classification
    metrics:
    - type: accuracy
      value: 41.71875
    - type: f1
      value: 39.289100975858304
    - type: f1_weighted
      value: 39.29257829217775
    - type: main_score
      value: 41.71875
    task:
      type: Classification
  - dataset:
      config: default
      name: MTEB RuSciBenchOECDClusteringP2P (default)
      revision: 26c88e99dcaba32bb45d0e1bfc21902337f6d471
      split: test
      type: ai-forever/ru-scibench-oecd-classification
    metrics:
    - type: main_score
      value: 45.10904808834516
    - type: v_measure
      value: 45.10904808834516
    - type: v_measure_std
      value: 1.0572643410157534
    task:
      type: Clustering
  - dataset:
      config: rus_Cyrl
      name: MTEB SIB200Classification (rus_Cyrl)
      revision: a74d7350ea12af010cfb1c21e34f1f81fd2e615b
      split: test
      type: mteb/sib200
    metrics:
    - type: accuracy
      value: 66.36363636363637
    - type: f1
      value: 64.6940336621617
    - type: f1_weighted
      value: 66.43317771876966
    - type: main_score
      value: 66.36363636363637
    task:
      type: Classification
  - dataset:
      config: rus_Cyrl
      name: MTEB SIB200ClusteringS2S (rus_Cyrl)
      revision: a74d7350ea12af010cfb1c21e34f1f81fd2e615b
      split: test
      type: mteb/sib200
    metrics:
    - type: main_score
      value: 33.99178497314711
    - type: v_measure
      value: 33.99178497314711
    - type: v_measure_std
      value: 4.036337464043786
    task:
      type: Clustering
  - dataset:
      config: ru
      name: MTEB STS22.v2 (ru)
      revision: d31f33a128469b20e357535c39b82fb3c3f6f2bd
      split: test
      type: mteb/sts22-crosslingual-sts
    metrics:
    - type: cosine_pearson
      value: 50.724322379215934
    - type: cosine_spearman
      value: 59.90449732164651
    - type: euclidean_pearson
      value: 50.227545226784024
    - type: euclidean_spearman
      value: 59.898906527601085
    - type: main_score
      value: 59.90449732164651
    - type: manhattan_pearson
      value: 50.21762139819405
    - type: manhattan_spearman
      value: 59.761039813759
    - type: pearson
      value: 50.724322379215934
    - type: spearman
      value: 59.90449732164651
    task:
      type: STS
  - dataset:
      config: ru
      name: MTEB STSBenchmarkMultilingualSTS (ru)
      revision: 29afa2569dcedaaa2fe6a3dcfebab33d28b82e8c
      split: dev
      type: mteb/stsb_multi_mt
    metrics:
    - type: cosine_pearson
      value: 78.43928769569945
    - type: cosine_spearman
      value: 78.23961768018884
    - type: euclidean_pearson
      value: 77.4718694027985
    - type: euclidean_spearman
      value: 78.23887044760475
    - type: main_score
      value: 78.23961768018884
    - type: manhattan_pearson
      value: 77.34517128089547
    - type: manhattan_spearman
      value: 78.1146477340426
    - type: pearson
      value: 78.43928769569945
    - type: spearman
      value: 78.23961768018884
    task:
      type: STS
  - dataset:
      config: default
      name: MTEB SensitiveTopicsClassification (default)
      revision: 416b34a802308eac30e4192afc0ff99bb8dcc7f2
      split: test
      type: ai-forever/sensitive-topics-classification
    metrics:
    - type: accuracy
      value: 22.8125
    - type: f1
      value: 17.31969589593409
    - type: lrap
      value: 33.82412380642287
    - type: main_score
      value: 22.8125
    task:
      type: MultilabelClassification
  - dataset:
      config: default
      name: MTEB TERRa (default)
      revision: 7b58f24536063837d644aab9a023c62199b2a612
      split: dev
      type: ai-forever/terra-pairclassification
    metrics:
    - type: cosine_accuracy
      value: 57.32899022801303
    - type: cosine_accuracy_threshold
      value: 85.32201051712036
    - type: cosine_ap
      value: 55.14264553720072
    - type: cosine_f1
      value: 66.83544303797468
    - type: cosine_f1_threshold
      value: 85.32201051712036
    - type: cosine_precision
      value: 54.54545454545454
    - type: cosine_recall
      value: 86.27450980392157
    - type: dot_accuracy
      value: 57.32899022801303
    - type: dot_accuracy_threshold
      value: 85.32201051712036
    - type: dot_ap
      value: 55.14264553720072
    - type: dot_f1
      value: 66.83544303797468
    - type: dot_f1_threshold
      value: 85.32201051712036
    - type: dot_precision
      value: 54.54545454545454
    - type: dot_recall
      value: 86.27450980392157
    - type: euclidean_accuracy
      value: 57.32899022801303
    - type: euclidean_accuracy_threshold
      value: 54.18117046356201
    - type: euclidean_ap
      value: 55.14264553720072
    - type: euclidean_f1
      value: 66.83544303797468
    - type: euclidean_f1_threshold
      value: 54.18117046356201
    - type: euclidean_precision
      value: 54.54545454545454
    - type: euclidean_recall
      value: 86.27450980392157
    - type: main_score
      value: 55.14264553720072
    - type: manhattan_accuracy
      value: 57.32899022801303
    - type: manhattan_accuracy_threshold
      value: 828.8480758666992
    - type: manhattan_ap
      value: 55.077974053622555
    - type: manhattan_f1
      value: 66.82352941176471
    - type: manhattan_f1_threshold
      value: 885.6784820556641
    - type: manhattan_precision
      value: 52.20588235294118
    - type: manhattan_recall
      value: 92.81045751633987
    - type: max_ap
      value: 55.14264553720072
    - type: max_f1
      value: 66.83544303797468
    - type: max_precision
      value: 54.54545454545454
    - type: max_recall
      value: 92.81045751633987
    - type: similarity_accuracy
      value: 57.32899022801303
    - type: similarity_accuracy_threshold
      value: 85.32201051712036
    - type: similarity_ap
      value: 55.14264553720072
    - type: similarity_f1
      value: 66.83544303797468
    - type: similarity_f1_threshold
      value: 85.32201051712036
    - type: similarity_precision
      value: 54.54545454545454
    - type: similarity_recall
      value: 86.27450980392157
    task:
      type: PairClassification
  - dataset:
      config: ru
      name: MTEB XNLI (ru)
      revision: 09698e0180d87dc247ca447d3a1248b931ac0cdb
      split: test
      type: mteb/xnli
    metrics:
    - type: cosine_accuracy
      value: 67.6923076923077
    - type: cosine_accuracy_threshold
      value: 87.6681923866272
    - type: cosine_ap
      value: 73.18693800863593
    - type: cosine_f1
      value: 70.40641099026904
    - type: cosine_f1_threshold
      value: 85.09706258773804
    - type: cosine_precision
      value: 57.74647887323944
    - type: cosine_recall
      value: 90.17595307917888
    - type: dot_accuracy
      value: 67.6923076923077
    - type: dot_accuracy_threshold
      value: 87.66818642616272
    - type: dot_ap
      value: 73.18693800863593
    - type: dot_f1
      value: 70.40641099026904
    - type: dot_f1_threshold
      value: 85.09706258773804
    - type: dot_precision
      value: 57.74647887323944
    - type: dot_recall
      value: 90.17595307917888
    - type: euclidean_accuracy
      value: 67.6923076923077
    - type: euclidean_accuracy_threshold
      value: 49.662476778030396
    - type: euclidean_ap
      value: 73.18693800863593
    - type: euclidean_f1
      value: 70.40641099026904
    - type: euclidean_f1_threshold
      value: 54.59475517272949
    - type: euclidean_precision
      value: 57.74647887323944
    - type: euclidean_recall
      value: 90.17595307917888
    - type: main_score
      value: 73.18693800863593
    - type: manhattan_accuracy
      value: 67.54578754578755
    - type: manhattan_accuracy_threshold
      value: 777.1001815795898
    - type: manhattan_ap
      value: 72.98861474758783
    - type: manhattan_f1
      value: 70.6842435655995
    - type: manhattan_f1_threshold
      value: 810.3782653808594
    - type: manhattan_precision
      value: 61.80021953896817
    - type: manhattan_recall
      value: 82.55131964809385
    - type: max_ap
      value: 73.18693800863593
    - type: max_f1
      value: 70.6842435655995
    - type: max_precision
      value: 61.80021953896817
    - type: max_recall
      value: 90.17595307917888
    - type: similarity_accuracy
      value: 67.6923076923077
    - type: similarity_accuracy_threshold
      value: 87.6681923866272
    - type: similarity_ap
      value: 73.18693800863593
    - type: similarity_f1
      value: 70.40641099026904
    - type: similarity_f1_threshold
      value: 85.09706258773804
    - type: similarity_precision
      value: 57.74647887323944
    - type: similarity_recall
      value: 90.17595307917888
    task:
      type: PairClassification
  - dataset:
      config: russian
      name: MTEB XNLIV2 (russian)
      revision: 5b7d477a8c62cdd18e2fed7e015497c20b4371ad
      split: test
      type: mteb/xnli2.0-multi-pair
    metrics:
    - type: cosine_accuracy
      value: 68.35164835164835
    - type: cosine_accuracy_threshold
      value: 88.48621845245361
    - type: cosine_ap
      value: 73.10205506215699
    - type: cosine_f1
      value: 71.28712871287128
    - type: cosine_f1_threshold
      value: 87.00399398803711
    - type: cosine_precision
      value: 61.67023554603854
    - type: cosine_recall
      value: 84.4574780058651
    - type: dot_accuracy
      value: 68.35164835164835
    - type: dot_accuracy_threshold
      value: 88.48622441291809
    - type: dot_ap
      value: 73.10191110714706
    - type: dot_f1
      value: 71.28712871287128
    - type: dot_f1_threshold
      value: 87.00399398803711
    - type: dot_precision
      value: 61.67023554603854
    - type: dot_recall
      value: 84.4574780058651
    - type: euclidean_accuracy
      value: 68.35164835164835
    - type: euclidean_accuracy_threshold
      value: 47.98704385757446
    - type: euclidean_ap
      value: 73.10205506215699
    - type: euclidean_f1
      value: 71.28712871287128
    - type: euclidean_f1_threshold
      value: 50.982362031936646
    - type: euclidean_precision
      value: 61.67023554603854
    - type: euclidean_recall
      value: 84.4574780058651
    - type: main_score
      value: 73.10205506215699
    - type: manhattan_accuracy
      value: 67.91208791208791
    - type: manhattan_accuracy_threshold
      value: 746.1360931396484
    - type: manhattan_ap
      value: 72.8954736175069
    - type: manhattan_f1
      value: 71.1297071129707
    - type: manhattan_f1_threshold
      value: 808.0789566040039
    - type: manhattan_precision
      value: 60.04036326942482
    - type: manhattan_recall
      value: 87.2434017595308
    - type: max_ap
      value: 73.10205506215699
    - type: max_f1
      value: 71.28712871287128
    - type: max_precision
      value: 61.67023554603854
    - type: max_recall
      value: 87.2434017595308
    - type: similarity_accuracy
      value: 68.35164835164835
    - type: similarity_accuracy_threshold
      value: 88.48621845245361
    - type: similarity_ap
      value: 73.10205506215699
    - type: similarity_f1
      value: 71.28712871287128
    - type: similarity_f1_threshold
      value: 87.00399398803711
    - type: similarity_precision
      value: 61.67023554603854
    - type: similarity_recall
      value: 84.4574780058651
    task:
      type: PairClassification
  - dataset:
      config: ru
      name: MTEB XQuADRetrieval (ru)
      revision: 51adfef1c1287aab1d2d91b5bead9bcfb9c68583
      split: validation
      type: google/xquad
    metrics:
    - type: main_score
      value: 95.705
    - type: map_at_1
      value: 90.802
    - type: map_at_10
      value: 94.427
    - type: map_at_100
      value: 94.451
    - type: map_at_1000
      value: 94.451
    - type: map_at_20
      value: 94.446
    - type: map_at_3
      value: 94.121
    - type: map_at_5
      value: 94.34
    - type: mrr_at_1
      value: 90.80168776371308
    - type: mrr_at_10
      value: 94.42659567343111
    - type: mrr_at_100
      value: 94.45099347521871
    - type: mrr_at_1000
      value: 94.45099347521871
    - type: mrr_at_20
      value: 94.44574530017569
    - type: mrr_at_3
      value: 94.12095639943743
    - type: mrr_at_5
      value: 94.34036568213786
    - type: nauc_map_at_1000_diff1
      value: 87.40573202946949
    - type: nauc_map_at_1000_max
      value: 65.56220344468791
    - type: nauc_map_at_1000_std
      value: 8.865583291735863
    - type: nauc_map_at_100_diff1
      value: 87.40573202946949
    - type: nauc_map_at_100_max
      value: 65.56220344468791
    - type: nauc_map_at_100_std
      value: 8.865583291735863
    - type: nauc_map_at_10_diff1
      value: 87.43657080570291
    - type: nauc_map_at_10_max
      value: 65.71295628534446
    - type: nauc_map_at_10_std
      value: 9.055399339099655
    - type: nauc_map_at_1_diff1
      value: 88.08395824560428
    - type: nauc_map_at_1_max
      value: 62.92813192908893
    - type: nauc_map_at_1_std
      value: 6.738987385482432
    - type: nauc_map_at_20_diff1
      value: 87.40979818966589
    - type: nauc_map_at_20_max
      value: 65.59474346926105
    - type: nauc_map_at_20_std
      value: 8.944420599300914
    - type: nauc_map_at_3_diff1
      value: 86.97771892161035
    - type: nauc_map_at_3_max
      value: 66.14330030122467
    - type: nauc_map_at_3_std
      value: 8.62516327793521
    - type: nauc_map_at_5_diff1
      value: 87.30273362211798
    - type: nauc_map_at_5_max
      value: 66.1522476584607
    - type: nauc_map_at_5_std
      value: 9.780940862679724
    - type: nauc_mrr_at_1000_diff1
      value: 87.40573202946949
    - type: nauc_mrr_at_1000_max
      value: 65.56220344468791
    - type: nauc_mrr_at_1000_std
      value: 8.865583291735863
    - type: nauc_mrr_at_100_diff1
      value: 87.40573202946949
    - type: nauc_mrr_at_100_max
      value: 65.56220344468791
    - type: nauc_mrr_at_100_std
      value: 8.865583291735863
    - type: nauc_mrr_at_10_diff1
      value: 87.43657080570291
    - type: nauc_mrr_at_10_max
      value: 65.71295628534446
    - type: nauc_mrr_at_10_std
      value: 9.055399339099655
    - type: nauc_mrr_at_1_diff1
      value: 88.08395824560428
    - type: nauc_mrr_at_1_max
      value: 62.92813192908893
    - type: nauc_mrr_at_1_std
      value: 6.738987385482432
    - type: nauc_mrr_at_20_diff1
      value: 87.40979818966589
    - type: nauc_mrr_at_20_max
      value: 65.59474346926105
    - type: nauc_mrr_at_20_std
      value: 8.944420599300914
    - type: nauc_mrr_at_3_diff1
      value: 86.97771892161035
    - type: nauc_mrr_at_3_max
      value: 66.14330030122467
    - type: nauc_mrr_at_3_std
      value: 8.62516327793521
    - type: nauc_mrr_at_5_diff1
      value: 87.30273362211798
    - type: nauc_mrr_at_5_max
      value: 66.1522476584607
    - type: nauc_mrr_at_5_std
      value: 9.780940862679724
    - type: nauc_ndcg_at_1000_diff1
      value: 87.37823158814116
    - type: nauc_ndcg_at_1000_max
      value: 66.00874244792789
    - type: nauc_ndcg_at_1000_std
      value: 9.479929342875067
    - type: nauc_ndcg_at_100_diff1
      value: 87.37823158814116
    - type: nauc_ndcg_at_100_max
      value: 66.00874244792789
    - type: nauc_ndcg_at_100_std
      value: 9.479929342875067
    - type: nauc_ndcg_at_10_diff1
      value: 87.54508467181488
    - type: nauc_ndcg_at_10_max
      value: 66.88756470312894
    - type: nauc_ndcg_at_10_std
      value: 10.812624405397022
    - type: nauc_ndcg_at_1_diff1
      value: 88.08395824560428
    - type: nauc_ndcg_at_1_max
      value: 62.92813192908893
    - type: nauc_ndcg_at_1_std
      value: 6.738987385482432
    - type: nauc_ndcg_at_20_diff1
      value: 87.42097894104597
    - type: nauc_ndcg_at_20_max
      value: 66.37031898778943
    - type: nauc_ndcg_at_20_std
      value: 10.34862538094813
    - type: nauc_ndcg_at_3_diff1
      value: 86.50039907157999
    - type: nauc_ndcg_at_3_max
      value: 67.97798288917929
    - type: nauc_ndcg_at_3_std
      value: 10.162410286746852
    - type: nauc_ndcg_at_5_diff1
      value: 87.13322094568531
    - type: nauc_ndcg_at_5_max
      value: 68.08576118683821
    - type: nauc_ndcg_at_5_std
      value: 12.639637379592855
    - type: nauc_precision_at_1000_diff1
      value: 100.0
    - type: nauc_precision_at_1000_max
      value: 100.0
    - type: nauc_precision_at_1000_std
      value: 100.0
    - type: nauc_precision_at_100_diff1
      value: 100.0
    - type: nauc_precision_at_100_max
      value: 100.0
    - type: nauc_precision_at_100_std
      value: 100.0
    - type: nauc_precision_at_10_diff1
      value: 93.46711505595813
    - type: nauc_precision_at_10_max
      value: 100.0
    - type: nauc_precision_at_10_std
      value: 65.42573557179935
    - type: nauc_precision_at_1_diff1
      value: 88.08395824560428
    - type: nauc_precision_at_1_max
      value: 62.92813192908893
    - type: nauc_precision_at_1_std
      value: 6.738987385482432
    - type: nauc_precision_at_20_diff1
      value: 91.28948674127133
    - type: nauc_precision_at_20_max
      value: 100.0
    - type: nauc_precision_at_20_std
      value: 90.74278258632364
    - type: nauc_precision_at_3_diff1
      value: 82.64606115071832
    - type: nauc_precision_at_3_max
      value: 83.26201582412921
    - type: nauc_precision_at_3_std
      value: 23.334013491433762
    - type: nauc_precision_at_5_diff1
      value: 85.0867539350284
    - type: nauc_precision_at_5_max
      value: 96.57011448655484
    - type: nauc_precision_at_5_std
      value: 56.46869543426768
    - type: nauc_recall_at_1000_diff1
      value: .nan
    - type: nauc_recall_at_1000_max
      value: .nan
    - type: nauc_recall_at_1000_std
      value: .nan
    - type: nauc_recall_at_100_diff1
      value: .nan
    - type: nauc_recall_at_100_max
      value: .nan
    - type: nauc_recall_at_100_std
      value: .nan
    - type: nauc_recall_at_10_diff1
      value: 93.46711505595623
    - type: nauc_recall_at_10_max
      value: 100.0
    - type: nauc_recall_at_10_std
      value: 65.42573557180279
    - type: nauc_recall_at_1_diff1
      value: 88.08395824560428
    - type: nauc_recall_at_1_max
      value: 62.92813192908893
    - type: nauc_recall_at_1_std
      value: 6.738987385482432
    - type: nauc_recall_at_20_diff1
      value: 91.28948674127474
    - type: nauc_recall_at_20_max
      value: 100.0
    - type: nauc_recall_at_20_std
      value: 90.74278258632704
    - type: nauc_recall_at_3_diff1
      value: 82.64606115071967
    - type: nauc_recall_at_3_max
      value: 83.26201582413023
    - type: nauc_recall_at_3_std
      value: 23.334013491434007
    - type: nauc_recall_at_5_diff1
      value: 85.08675393502854
    - type: nauc_recall_at_5_max
      value: 96.57011448655487
    - type: nauc_recall_at_5_std
      value: 56.46869543426658
    - type: ndcg_at_1
      value: 90.802
    - type: ndcg_at_10
      value: 95.705
    - type: ndcg_at_100
      value: 95.816
    - type: ndcg_at_1000
      value: 95.816
    - type: ndcg_at_20
      value: 95.771
    - type: ndcg_at_3
      value: 95.11699999999999
    - type: ndcg_at_5
      value: 95.506
    - type: precision_at_1
      value: 90.802
    - type: precision_at_10
      value: 9.949
    - type: precision_at_100
      value: 1.0
    - type: precision_at_1000
      value: 0.1
    - type: precision_at_20
      value: 4.987
    - type: precision_at_3
      value: 32.658
    - type: precision_at_5
      value: 19.781000000000002
    - type: recall_at_1
      value: 90.802
    - type: recall_at_10
      value: 99.494
    - type: recall_at_100
      value: 100.0
    - type: recall_at_1000
      value: 100.0
    - type: recall_at_20
      value: 99.747
    - type: recall_at_3
      value: 97.975
    - type: recall_at_5
      value: 98.90299999999999
    task:
      type: Retrieval
tags:
- mteb
- Sentence Transformers
- sentence-similarity
- sentence-transformers
---


## Multilingual-E5-small

[Multilingual E5 Text Embeddings: A Technical Report](https://arxiv.org/pdf/2402.05672).
Liang Wang, Nan Yang, Xiaolong Huang, Linjun Yang, Rangan Majumder, Furu Wei, arXiv 2024

This model has 12 layers and the embedding size is 384.

## Usage

Below is an example to encode queries and passages from the MS-MARCO passage ranking dataset.

```python
import torch.nn.functional as F

from torch import Tensor
from transformers import AutoTokenizer, AutoModel


def average_pool(last_hidden_states: Tensor,
                 attention_mask: Tensor) -> Tensor:
    last_hidden = last_hidden_states.masked_fill(~attention_mask[..., None].bool(), 0.0)
    return last_hidden.sum(dim=1) / attention_mask.sum(dim=1)[..., None]


# Each input text should start with "query: " or "passage: ", even for non-English texts.
# For tasks other than retrieval, you can simply use the "query: " prefix.
input_texts = ['query: how much protein should a female eat',
               'query: 南瓜的家常做法',
               "passage: As a general guideline, the CDC's average requirement of protein for women ages 19 to 70 is 46 grams per day. But, as you can see from this chart, you'll need to increase that if you're expecting or training for a marathon. Check out the chart below to see how much protein you should be eating each day.",
               "passage: 1.清炒南瓜丝 原料:嫩南瓜半个 调料:葱、盐、白糖、鸡精 做法: 1、南瓜用刀薄薄的削去表面一层皮,用勺子刮去瓤 2、擦成细丝(没有擦菜板就用刀慢慢切成细丝) 3、锅烧热放油,入葱花煸出香味 4、入南瓜丝快速翻炒一分钟左右,放盐、一点白糖和鸡精调味出锅 2.香葱炒南瓜 原料:南瓜1只 调料:香葱、蒜末、橄榄油、盐 做法: 1、将南瓜去皮,切成片 2、油锅8成热后,将蒜末放入爆香 3、爆香后,将南瓜片放入,翻炒 4、在翻炒的同时,可以不时地往锅里加水,但不要太多 5、放入盐,炒匀 6、南瓜差不多软和绵了之后,就可以关火 7、撒入香葱,即可出锅"]

tokenizer = AutoTokenizer.from_pretrained('intfloat/multilingual-e5-small')
model = AutoModel.from_pretrained('intfloat/multilingual-e5-small')

# Tokenize the input texts
batch_dict = tokenizer(input_texts, max_length=512, padding=True, truncation=True, return_tensors='pt')

outputs = model(**batch_dict)
embeddings = average_pool(outputs.last_hidden_state, batch_dict['attention_mask'])

# normalize embeddings
embeddings = F.normalize(embeddings, p=2, dim=1)
scores = (embeddings[:2] @ embeddings[2:].T) * 100
print(scores.tolist())
```

## Supported Languages

This model is initialized from [microsoft/Multilingual-MiniLM-L12-H384](https://huggingface.co/microsoft/Multilingual-MiniLM-L12-H384)
and continually trained on a mixture of multilingual datasets.
It supports 100 languages from xlm-roberta,
but low-resource languages may see performance degradation.

## Training Details

**Initialization**: [microsoft/Multilingual-MiniLM-L12-H384](https://huggingface.co/microsoft/Multilingual-MiniLM-L12-H384)

**First stage**: contrastive pre-training with weak supervision

| Dataset                                                                                                | Weak supervision                      | # of text pairs |
|--------------------------------------------------------------------------------------------------------|---------------------------------------|-----------------|
| Filtered [mC4](https://huggingface.co/datasets/mc4)                                                    | (title, page content)                 | 1B              |
| [CC News](https://huggingface.co/datasets/intfloat/multilingual_cc_news)                               | (title, news content)                 | 400M            |
| [NLLB](https://huggingface.co/datasets/allenai/nllb)                                                   | translation pairs                     | 2.4B            |
| [Wikipedia](https://huggingface.co/datasets/intfloat/wikipedia)                                        | (hierarchical section title, passage) | 150M            |
| Filtered [Reddit](https://www.reddit.com/)                                                             | (comment, response)                   | 800M            |
| [S2ORC](https://github.com/allenai/s2orc)                                                              | (title, abstract) and citation pairs  | 100M            |
| [Stackexchange](https://stackexchange.com/)                                                            | (question, answer)                    | 50M             |
| [xP3](https://huggingface.co/datasets/bigscience/xP3)                                                  | (input prompt, response)              | 80M             |
| [Miscellaneous unsupervised SBERT data](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2) | -                                     | 10M             |

**Second stage**: supervised fine-tuning

| Dataset                                                                                | Language     | # of text pairs |
|----------------------------------------------------------------------------------------|--------------|-----------------|
| [MS MARCO](https://microsoft.github.io/msmarco/)                                       | English      | 500k            |
| [NQ](https://github.com/facebookresearch/DPR)                                          | English      | 70k             |
| [Trivia QA](https://github.com/facebookresearch/DPR)                                   | English      | 60k             |
| [NLI from SimCSE](https://github.com/princeton-nlp/SimCSE)                             | English      | <300k           |
| [ELI5](https://huggingface.co/datasets/eli5)                                           | English      | 500k            |
| [DuReader Retrieval](https://github.com/baidu/DuReader/tree/master/DuReader-Retrieval) | Chinese      | 86k             |
| [KILT Fever](https://huggingface.co/datasets/kilt_tasks)                               | English      | 70k             |
| [KILT HotpotQA](https://huggingface.co/datasets/kilt_tasks)                            | English      | 70k             |
| [SQuAD](https://huggingface.co/datasets/squad)                                         | English      | 87k             |
| [Quora](https://huggingface.co/datasets/quora)                                         | English      | 150k            |
| [Mr. TyDi](https://huggingface.co/datasets/castorini/mr-tydi)                                                                           | 11 languages | 50k             |
| [MIRACL](https://huggingface.co/datasets/miracl/miracl)                                                                             | 16 languages | 40k             |

For all labeled datasets, we only use its training set for fine-tuning.

For other training details, please refer to our paper at [https://arxiv.org/pdf/2402.05672](https://arxiv.org/pdf/2402.05672).

## Benchmark Results on [Mr. TyDi](https://arxiv.org/abs/2108.08787)

| Model                 | Avg MRR@10 |       | ar   | bn | en | fi | id | ja | ko | ru | sw   | te | th |
|-----------------------|------------|-------|------| --- | --- | --- | --- | --- | --- | --- |------| --- | --- |
| BM25                  | 33.3       | | 36.7 | 41.3 | 15.1 | 28.8 | 38.2 | 21.7 | 28.1 | 32.9 | 39.6 | 42.4 | 41.7 |
| mDPR                  | 16.7       | | 26.0 | 25.8  | 16.2 | 11.3 | 14.6 | 18.1 | 21.9 | 18.5 | 7.3 | 10.6 | 13.5 |
| BM25 + mDPR           | 41.7       | | 49.1 | 53.5 | 28.4 | 36.5 | 45.5 | 35.5 | 36.2 | 42.7 | 40.5 | 42.0 | 49.2 |
|                       |            |
| multilingual-e5-small | 64.4       | | 71.5 | 66.3 | 54.5 | 57.7 | 63.2 | 55.4 | 54.3 | 60.8 | 65.4 | 89.1 | 70.1 |
| multilingual-e5-base  | 65.9       | | 72.3 | 65.0 | 58.5 | 60.8 | 64.9 | 56.6 | 55.8 | 62.7 | 69.0 | 86.6 | 72.7 |
| multilingual-e5-large | **70.5**   | | 77.5 | 73.2 | 60.8 | 66.8 | 68.5 | 62.5 | 61.6 | 65.8 | 72.7 | 90.2 | 76.2 |

## MTEB Benchmark Evaluation

Check out [unilm/e5](https://github.com/microsoft/unilm/tree/master/e5) to reproduce evaluation results
on the [BEIR](https://arxiv.org/abs/2104.08663) and [MTEB benchmark](https://arxiv.org/abs/2210.07316).

## Support for Sentence Transformers

Below is an example for usage with sentence_transformers.
```python
from sentence_transformers import SentenceTransformer
model = SentenceTransformer('intfloat/multilingual-e5-small')
input_texts = [
    'query: how much protein should a female eat',
    'query: 南瓜的家常做法',
    "passage: As a general guideline, the CDC's average requirement of protein for women ages 19 to 70 i     s 46 grams per day. But, as you can see from this chart, you'll need to increase that if you're expecting or traini     ng for a marathon. Check out the chart below to see how much protein you should be eating each day.",
    "passage: 1.清炒南瓜丝 原料:嫩南瓜半个 调料:葱、盐、白糖、鸡精 做法: 1、南瓜用刀薄薄的削去表面一层皮     ,用勺子刮去瓤 2、擦成细丝(没有擦菜板就用刀慢慢切成细丝) 3、锅烧热放油,入葱花煸出香味 4、入南瓜丝快速翻炒一分钟左右,     放盐、一点白糖和鸡精调味出锅 2.香葱炒南瓜 原料:南瓜1只 调料:香葱、蒜末、橄榄油、盐 做法: 1、将南瓜去皮,切成片 2、油     锅8成热后,将蒜末放入爆香 3、爆香后,将南瓜片放入,翻炒 4、在翻炒的同时,可以不时地往锅里加水,但不要太多 5、放入盐,炒匀      6、南瓜差不多软和绵了之后,就可以关火 7、撒入香葱,即可出锅"
]
embeddings = model.encode(input_texts, normalize_embeddings=True)
```

Package requirements

`pip install sentence_transformers~=2.2.2`

Contributors: [michaelfeil](https://huggingface.co/michaelfeil)

## FAQ

**1. Do I need to add the prefix "query: " and "passage: " to input texts?**

Yes, this is how the model is trained, otherwise you will see a performance degradation.

Here are some rules of thumb:
- Use "query: " and "passage: " correspondingly for asymmetric tasks such as passage retrieval in open QA, ad-hoc information retrieval.

- Use "query: " prefix for symmetric tasks such as semantic similarity, bitext mining, paraphrase retrieval.

- Use "query: " prefix if you want to use embeddings as features, such as linear probing classification, clustering.

**2. Why are my reproduced results slightly different from reported in the model card?**

Different versions of `transformers` and `pytorch` could cause negligible but non-zero performance differences.

**3. Why does the cosine similarity scores distribute around 0.7 to 1.0?**

This is a known and expected behavior as we use a low temperature 0.01 for InfoNCE contrastive loss.

For text embedding tasks like text retrieval or semantic similarity,
what matters is the relative order of the scores instead of the absolute values,
so this should not be an issue.

## Citation

If you find our paper or models helpful, please consider cite as follows:

```
@article{wang2024multilingual,
  title={Multilingual E5 Text Embeddings: A Technical Report},
  author={Wang, Liang and Yang, Nan and Huang, Xiaolong and Yang, Linjun and Majumder, Rangan and Wei, Furu},
  journal={arXiv preprint arXiv:2402.05672},
  year={2024}
}
```

## Limitations

Long texts will be truncated to at most 512 tokens.

