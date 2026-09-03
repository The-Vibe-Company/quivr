import {TransitionSeries, linearTiming} from '@remotion/transitions';
import {fade} from '@remotion/transitions/fade';
import {PipelineScene, PluginScene} from './QuivrExplainer';

export const QuivrReadmeLoop = () => {
  return (
    <TransitionSeries>
      <TransitionSeries.Sequence durationInFrames={140} premountFor={20}>
        <PipelineScene durationSeconds={7} compact />
      </TransitionSeries.Sequence>
      <TransitionSeries.Transition presentation={fade()} timing={linearTiming({durationInFrames: 20})} />
      <TransitionSeries.Sequence durationInFrames={120} premountFor={20}>
        <PluginScene durationSeconds={6} compact />
      </TransitionSeries.Sequence>
    </TransitionSeries>
  );
};
