import {Composition, Folder} from 'remotion';
import {QuivrExplainer} from './QuivrExplainer';
import {QuivrReadmeLoop} from './QuivrReadmeLoop';

export const RemotionRoot = () => {
  return (
    <Folder name="Quivr-V2">
      <Composition
        id="QuivrExplainer"
        component={QuivrExplainer}
        durationInFrames={765}
        fps={30}
        width={1920}
        height={1080}
      />
      <Composition
        id="QuivrReadmeLoop"
        component={QuivrReadmeLoop}
        durationInFrames={240}
        fps={20}
        width={1200}
        height={675}
      />
    </Folder>
  );
};
