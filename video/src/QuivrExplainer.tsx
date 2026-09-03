import {TransitionSeries, linearTiming} from '@remotion/transitions';
import {fade} from '@remotion/transitions/fade';
import {slide} from '@remotion/transitions/slide';
import {
  AbsoluteFill,
  Easing,
  interpolate,
  spring,
  useCurrentFrame,
  useVideoConfig,
} from 'remotion';
import {
  Background,
  Brand,
  FlowLine,
  Kicker,
  palette,
  Payload,
  reveal,
  sceneStyle,
  smooth,
  StageCard,
  Title,
} from './visuals';

const clamp = {
  extrapolateLeft: 'clamp' as const,
  extrapolateRight: 'clamp' as const,
};

type PipelineSceneProps = {
  durationSeconds: number;
  compact?: boolean;
};

const stageDefinitions = [
  {label: 'Un contenu arrive', detail: 'Texte · image · audio · vidéo', color: palette.source, icon: 'content', badge: 'multimodal'},
  {label: 'API Quivr', detail: 'Accepte sans doublon', color: palette.core, icon: 'api', badge: 'accepted'},
  {label: 'Workflow durable', detail: 'Temporal reprend et retente', color: palette.core, icon: 'workflow', badge: 'durable'},
  {label: 'Plugin actif', detail: 'OCR · transcript · règles', color: palette.plugin, icon: 'plugin', badge: 'extensible'},
  {label: 'Vérité durable', detail: 'PostgreSQL + S3', color: palette.data, icon: 'store', badge: 'canonique'},
  {label: 'Index recherche', detail: 'Weaviate reconstruisible', color: palette.data, icon: 'search', badge: 'searchable'},
  {label: 'Valeur immédiate', detail: 'Recherche · veille · alertes', color: palette.output, icon: 'output', badge: 'utile'},
] as const;

const statusLabels = ['reçu', 'accepté', 'orchestré', 'enrichi', 'persisté', 'indexé', 'disponible'];

export const PipelineScene = ({durationSeconds, compact = false}: PipelineSceneProps) => {
  const frame = useCurrentFrame();
  const {fps, width, height} = useVideoConfig();
  const titleIn = reveal(frame, fps, 0.05);
  const margin = compact ? 34 : 64;
  const cardWidth = compact ? 132 : 210;
  const cardHeight = compact ? 220 : 230;
  const available = width - margin * 2 - cardWidth;
  const step = available / (stageDefinitions.length - 1);
  const y = compact ? 245 : 405;
  const centers = stageDefinitions.map((_, index) => margin + cardWidth / 2 + index * step);
  const travelStart = compact ? 0.9 * fps : 1.65 * fps;
  const travelEnd = (durationSeconds - (compact ? 0.8 : 1.25)) * fps;
  const routePosition = interpolate(frame, [travelStart, travelEnd], [0, stageDefinitions.length - 1], {
    ...clamp,
    easing: Easing.inOut(Easing.cubic),
  });
  const segment = Math.min(stageDefinitions.length - 2, Math.max(0, Math.floor(routePosition)));
  const segmentProgress = routePosition - segment;
  const payloadX = centers[segment] + (centers[segment + 1] - centers[segment]) * segmentProgress;
  const activeIndex = Math.min(stageDefinitions.length - 1, Math.round(routePosition));
  const lineY = y + cardHeight / 2;
  const statusOpacity = interpolate(frame, [travelStart, travelStart + 0.35 * fps], [0, 1], clamp);
  const enriched = routePosition >= 3;

  return (
    <AbsoluteFill style={sceneStyle}>
      <Background accent={palette.source} />
      {!compact ? <Brand /> : null}
      <div
        style={{
          position: 'relative',
          zIndex: 3,
          opacity: titleIn,
          transform: `translateY(${(1 - titleIn) * 20}px)`,
          textAlign: 'center',
        }}
      >
        <Kicker color={palette.source}>Un seul parcours</Kicker>
        <div style={{marginTop: compact ? 14 : 22, display: 'flex', justifyContent: 'center'}}>
          <Title size={compact ? 39 : 65}>Du contenu brut à une information utile.</Title>
        </div>
        {!compact ? (
          <div style={{fontSize: 24, color: palette.muted, marginTop: 16}}>
            Quivr rend le contenu searchable, puis l’enrichit progressivement.
          </div>
        ) : null}
      </div>

      {centers.slice(0, -1).map((center, index) => (
        <FlowLine
          key={`line-${index}`}
          x1={center + cardWidth / 2 - 2}
          x2={centers[index + 1] - cardWidth / 2 + 2}
          y={lineY}
          color={index < routePosition ? stageDefinitions[Math.min(index + 1, 6)].color : palette.line}
        />
      ))}

      {stageDefinitions.map((stage, index) => (
        <StageCard
          key={stage.label}
          x={centers[index] - cardWidth / 2}
          y={y}
          width={cardWidth}
          height={cardHeight}
          color={stage.color}
          icon={stage.icon}
          label={stage.label}
          detail={compact ? '' : stage.detail}
          entranceSeconds={0.35 + index * 0.07}
          active={activeIndex === index}
          badge={compact ? undefined : stage.badge}
        />
      ))}

      {frame >= travelStart ? <Payload x={payloadX} y={lineY} enriched={enriched} /> : null}

      <div
        style={{
          position: 'absolute',
          left: 0,
          right: 0,
          top: y + cardHeight + (compact ? 28 : 48),
          textAlign: 'center',
          opacity: statusOpacity,
          fontSize: compact ? 18 : 25,
          fontWeight: 720,
          color: stageDefinitions[activeIndex].color,
          letterSpacing: '-0.02em',
          zIndex: 6,
        }}
      >
        {statusLabels[activeIndex]}
        {enriched && activeIndex <= 3 ? ' · + transcript' : ''}
      </div>

      {!compact ? (
        <div
          style={{
            position: 'absolute',
            left: 64,
            right: 64,
            bottom: 54,
            display: 'flex',
            justifyContent: 'center',
            gap: 28,
            color: palette.muted,
            fontSize: 17,
            zIndex: 4,
          }}
        >
          <span>● <b style={{color: palette.core}}>Cœur Quivr</b></span>
          <span>● <b style={{color: palette.plugin}}>Plugins</b></span>
          <span>● <b style={{color: palette.data}}>Données</b></span>
          <span>● <b style={{color: palette.output}}>Usages</b></span>
        </div>
      ) : null}
    </AbsoluteFill>
  );
};

type PluginSceneProps = {
  durationSeconds: number;
  compact?: boolean;
};

export const PluginScene = ({durationSeconds, compact = false}: PluginSceneProps) => {
  const frame = useCurrentFrame();
  const {fps, width} = useVideoConfig();
  const titleIn = reveal(frame, fps, 0.05);
  const packageIn = reveal(frame, fps, durationSeconds * 0.2);
  const connection = smooth(
    frame,
    [durationSeconds * 0.42 * fps, durationSeconds * 0.58 * fps],
    [0, 1],
  );
  const activation = reveal(frame, fps, durationSeconds * 0.58);
  const coreX = compact ? 70 : 220;
  const coreY = compact ? 250 : 330;
  const coreWidth = compact ? 430 : 650;
  const coreHeight = compact ? 250 : 390;
  const pluginWidth = compact ? 360 : 520;
  const pluginHeight = compact ? 250 : 390;
  const pluginX = width - coreX - pluginWidth;
  const pluginY = coreY;
  const lineY = coreY + coreHeight / 2;
  const packetPhase = smooth(
    frame,
    [durationSeconds * 0.61 * fps, durationSeconds * 0.9 * fps],
    [0, 2],
  );
  const packetX =
    packetPhase <= 1
      ? coreX + coreWidth * 0.7 + (pluginX + pluginWidth * 0.3 - (coreX + coreWidth * 0.7)) * packetPhase
      : pluginX + pluginWidth * 0.3 + (coreX + coreWidth * 0.78 - (pluginX + pluginWidth * 0.3)) * (packetPhase - 1);
  const showPacket = frame >= durationSeconds * 0.6 * fps;
  const statusOpacity = interpolate(
    frame,
    [durationSeconds * 0.78 * fps, durationSeconds * 0.9 * fps],
    [0, 1],
    clamp,
  );

  return (
    <AbsoluteFill style={sceneStyle}>
      <Background accent={palette.plugin} />
      {!compact ? <Brand /> : null}
      <div
        style={{
          position: 'relative',
          zIndex: 4,
          opacity: titleIn,
          transform: `translateY(${(1 - titleIn) * 20}px)`,
          textAlign: 'center',
        }}
      >
        <Kicker color={palette.plugin}>Plugin-first</Kicker>
        <div style={{marginTop: compact ? 14 : 22, display: 'flex', justifyContent: 'center'}}>
          <Title size={compact ? 39 : 64}>Ajoutez une capacité. Gardez le cœur stable.</Title>
        </div>
      </div>

      <div
        style={{
          position: 'absolute',
          left: coreX,
          top: coreY,
          width: coreWidth,
          height: coreHeight,
          borderRadius: compact ? 28 : 36,
          border: `1px solid ${palette.core}77`,
          background: `linear-gradient(145deg, ${palette.core}24, ${palette.panel} 68%)`,
          boxShadow: `0 24px 80px ${palette.core}1E`,
          padding: compact ? 24 : 34,
          boxSizing: 'border-box',
          zIndex: 4,
        }}
      >
        <div style={{fontSize: compact ? 23 : 34, fontWeight: 820}}>Cœur Quivr V2</div>
        <div style={{color: palette.muted, fontSize: compact ? 15 : 20, marginTop: 7}}>API + Temporal + contrats stables</div>
        <div style={{display: 'flex', gap: compact ? 10 : 16, marginTop: compact ? 28 : 54}}>
          {['Accepter', 'Orchestrer', 'Persister'].map((label, index) => (
            <div
              key={label}
              style={{
                flex: 1,
                padding: compact ? '17px 10px' : '25px 16px',
                borderRadius: 17,
                background: `${palette.core}${index === 1 ? '30' : '16'}`,
                border: `1px solid ${palette.core}44`,
                textAlign: 'center',
                fontSize: compact ? 14 : 18,
                fontWeight: 720,
              }}
            >
              {label}
            </div>
          ))}
        </div>
      </div>

      <FlowLine
        x1={coreX + coreWidth}
        x2={pluginX}
        y={lineY}
        color={palette.plugin}
        progress={connection}
      />

      <div
        style={{
          position: 'absolute',
          left: pluginX + (1 - packageIn) * 90,
          top: pluginY,
          width: pluginWidth,
          height: pluginHeight,
          borderRadius: compact ? 28 : 36,
          border: `1px solid ${palette.plugin}`,
          background: `linear-gradient(145deg, ${palette.plugin}28, ${palette.panel} 68%)`,
          boxShadow: `0 20px 90px ${palette.plugin}2E`,
          padding: compact ? 24 : 34,
          boxSizing: 'border-box',
          opacity: packageIn,
          transform: `scale(${0.93 + packageIn * 0.07})`,
          zIndex: 4,
        }}
      >
        <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
          <div>
            <div style={{color: palette.plugin, fontSize: compact ? 14 : 17, fontWeight: 820, letterSpacing: '0.1em'}}>NOUVEAU PLUGIN</div>
            <div style={{fontSize: compact ? 25 : 36, fontWeight: 820, marginTop: 7}}>Transcription vidéo</div>
          </div>
          <div style={{fontSize: compact ? 40 : 58, color: palette.plugin}}>＋</div>
        </div>
        <div style={{display: 'flex', flexWrap: 'wrap', gap: 10, marginTop: compact ? 24 : 42}}>
          {['Package OCI', 'SemVer ✓', 'Worker externe'].map((item, index) => {
            const checkIn = reveal(frame, fps, durationSeconds * (0.3 + index * 0.055));
            return (
              <span
                key={item}
                style={{
                  padding: compact ? '9px 11px' : '12px 15px',
                  borderRadius: 999,
                  background: `${palette.plugin}17`,
                  border: `1px solid ${palette.plugin}44`,
                  color: palette.plugin,
                  fontSize: compact ? 13 : 16,
                  fontWeight: 760,
                  opacity: checkIn,
                  transform: `scale(${0.9 + checkIn * 0.1})`,
                }}
              >
                {item}
              </span>
            );
          })}
        </div>
        <div style={{marginTop: compact ? 26 : 46, color: palette.muted, fontSize: compact ? 15 : 20, lineHeight: 1.45}}>
          Reçoit le document, produit un transcript, puis rend le résultat au workflow.
        </div>
      </div>

      {showPacket && connection > 0.95 ? <Payload x={packetX} y={lineY} enriched={packetPhase > 1} /> : null}

      <div
        style={{
          position: 'absolute',
          left: 0,
          right: 0,
          bottom: compact ? 34 : 66,
          textAlign: 'center',
          opacity: statusOpacity * activation,
          color: palette.plugin,
          fontSize: compact ? 19 : 28,
          fontWeight: 780,
          zIndex: 8,
        }}
      >
        Plugin actif · génération v2 · aucun arrêt global
      </div>
    </AbsoluteFill>
  );
};

const IntroScene = () => {
  const frame = useCurrentFrame();
  const {fps} = useVideoConfig();
  const inProgress = reveal(frame, fps, 0.1);
  const words = ['Texte', 'Image', 'Audio', 'Vidéo'];

  return (
    <AbsoluteFill style={{...sceneStyle, display: 'flex', alignItems: 'center', justifyContent: 'center'}}>
      <Background accent={palette.core} />
      <Brand />
      <div style={{position: 'relative', zIndex: 3, textAlign: 'center', opacity: inProgress}}>
        <Kicker color={palette.core}>Backend multimodal open source</Kicker>
        <div style={{marginTop: 30}}>
          <Title size={94} maxWidth={1500}>Toute votre information.<br /><span style={{color: palette.core}}>Un seul flux.</span></Title>
        </div>
        <div style={{display: 'flex', gap: 14, justifyContent: 'center', marginTop: 44}}>
          {words.map((word, index) => {
            const itemIn = reveal(frame, fps, 0.55 + index * 0.11);
            return (
              <div
                key={word}
                style={{
                  padding: '13px 21px',
                  borderRadius: 999,
                  border: `1px solid ${palette.source}44`,
                  background: `${palette.source}11`,
                  color: palette.source,
                  fontSize: 21,
                  fontWeight: 720,
                  opacity: itemIn,
                  transform: `translateY(${(1 - itemIn) * 18}px)`,
                }}
              >
                {word}
              </div>
            );
          })}
        </div>
      </div>
    </AbsoluteFill>
  );
};

const OutroScene = () => {
  const frame = useCurrentFrame();
  const {fps} = useVideoConfig();
  const inProgress = spring({frame, fps, config: {damping: 200}, durationInFrames: 0.9 * fps});
  const underline = interpolate(frame, [0.55 * fps, 1.6 * fps], [0, 1], {
    ...clamp,
    easing: Easing.out(Easing.cubic),
  });

  return (
    <AbsoluteFill style={{...sceneStyle, display: 'flex', alignItems: 'center', justifyContent: 'center'}}>
      <Background accent={palette.plugin} />
      <Brand />
      <div
        style={{
          position: 'relative',
          zIndex: 3,
          textAlign: 'center',
          opacity: inProgress,
          transform: `scale(${0.94 + inProgress * 0.06})`,
        }}
      >
        <Title size={86}>Un cœur stable.<br /><span style={{color: palette.plugin}}>Des pipelines à composer.</span></Title>
        <div
          style={{
            width: 420 * underline,
            height: 5,
            borderRadius: 99,
            background: `linear-gradient(90deg, ${palette.core}, ${palette.plugin})`,
            margin: '38px auto 30px',
          }}
        />
        <div style={{fontSize: 24, color: palette.muted}}>Open source · Multimodal · Plugin-first</div>
      </div>
    </AbsoluteFill>
  );
};

export const QuivrExplainer = () => {
  return (
    <TransitionSeries>
      <TransitionSeries.Sequence durationInFrames={90} premountFor={30}>
        <IntroScene />
      </TransitionSeries.Sequence>
      <TransitionSeries.Transition presentation={fade()} timing={linearTiming({durationInFrames: 15})} />
      <TransitionSeries.Sequence durationInFrames={390} premountFor={30}>
        <PipelineScene durationSeconds={13} />
      </TransitionSeries.Sequence>
      <TransitionSeries.Transition presentation={slide({direction: 'from-right'})} timing={linearTiming({durationInFrames: 15})} />
      <TransitionSeries.Sequence durationInFrames={240} premountFor={30}>
        <PluginScene durationSeconds={8} />
      </TransitionSeries.Sequence>
      <TransitionSeries.Transition presentation={fade()} timing={linearTiming({durationInFrames: 15})} />
      <TransitionSeries.Sequence durationInFrames={90} premountFor={30}>
        <OutroScene />
      </TransitionSeries.Sequence>
    </TransitionSeries>
  );
};
