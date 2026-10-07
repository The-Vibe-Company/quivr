"""Import the remote reference path at image build time, without model/data downloads.

Run with HF_HUB_OFFLINE=1 and TRANSFORMERS_OFFLINE=1. Import concrete model
classes as well as sentence-transformers: Transformers lazily loads dependencies.
"""
import os
import oss_modal
import oss_bakeoff
import numpy
import pyarrow.parquet
from scipy.stats import ttest_rel
from sentence_transformers import SentenceTransformer
from transformers import AutoTokenizer, BertModel, PreTrainedModel


if os.environ.get('QUIVR_GEMMA_IMAGE') == '1':
    # EmbeddingGemma2Processor needs these even for text-only use; fail the build, not the paid run.
    import PIL
    import torchvision
    from transformers.utils import is_torchvision_available, is_vision_available
    assert is_vision_available() and is_torchvision_available()


if __name__ == '__main__':
    print('Remote reference imports passed; no weights or dataset loaded.', flush=True)
