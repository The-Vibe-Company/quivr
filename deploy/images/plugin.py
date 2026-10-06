"""Run the installed first-party plugin module selected at image build."""
import os
import runpy

runpy.run_module(os.environ['QUIVR_PLUGIN_MODULE'].replace('-', '_'), run_name='__main__')
