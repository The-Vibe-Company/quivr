import type {CSSProperties, ReactNode} from 'react';
import {AbsoluteFill, Easing, interpolate, spring, useCurrentFrame, useVideoConfig} from 'remotion';
import {Background, Icon, fontFamily, palette} from './visuals';

const clamp = {
  extrapolateLeft: 'clamp' as const,
  extrapolateRight: 'clamp' as const,
};

type Point = {x: number; y: number};

const pointOnRoute = (points: Point[], progress: number): Point => {
  const position = Math.min(1, Math.max(0, progress)) * (points.length - 1);
  const index = Math.min(points.length - 2, Math.floor(position));
  const local = position - index;
  return {
    x: points[index].x + (points[index + 1].x - points[index].x) * local,
    y: points[index].y + (points[index + 1].y - points[index].y) * local,
  };
};

const GraphCard = ({
  x,
  y,
  width,
  height,
  color,
  icon,
  title,
  children,
  active,
  entrance,
}: {
  x: number;
  y: number;
  width: number;
  height: number;
  color: string;
  icon: string;
  title: string;
  children: ReactNode;
  active: boolean;
  entrance: number;
}) => {
  const frame = useCurrentFrame();
  const {fps} = useVideoConfig();
  const reveal = spring({
    frame: frame - entrance * fps,
    fps,
    config: {damping: 200},
    durationInFrames: 0.55 * fps,
  });

  return (
    <div
      style={{
        position: 'absolute',
        left: x,
        top: y + (1 - reveal) * 20,
        width,
        height,
        boxSizing: 'border-box',
        padding: '17px 18px',
        borderRadius: 20,
        border: `1.5px solid ${active ? color : `${color}66`}`,
        background: active ? `${color}20` : 'rgba(15, 21, 38, 0.94)',
        boxShadow: active ? `0 0 0 4px ${color}12, 0 18px 48px ${color}20` : '0 16px 42px rgba(0,0,0,.18)',
        opacity: reveal,
        transform: `scale(${0.96 + reveal * 0.04})`,
        zIndex: 4,
      }}
    >
      <div style={{display: 'flex', alignItems: 'center', gap: 12}}>
        <div style={{width: 34, height: 34, overflow: 'hidden'}}>
          <div style={{transform: 'scale(.63)', transformOrigin: 'top left'}}>
            <Icon name={icon} color={color} />
          </div>
        </div>
        <div style={{fontSize: 19, fontWeight: 790, letterSpacing: '-0.025em'}}>{title}</div>
      </div>
      <div style={{fontSize: 13, lineHeight: 1.35, color: palette.muted, marginTop: 11}}>{children}</div>
    </div>
  );
};

const Connector = ({d, color, progress = 1, muted = false}: {d: string; color: string; progress?: number; muted?: boolean}) => (
  <path
    d={d}
    fill="none"
    stroke={color}
    strokeWidth={muted ? 2 : 3}
    strokeLinecap="round"
    pathLength={1}
    strokeDasharray="1"
    strokeDashoffset={1 - progress}
    opacity={muted ? 0.3 : 0.82}
  />
);

const Payload = ({point, color, label}: {point: Point; color: string; label: string}) => (
  <div
    style={{
      position: 'absolute',
      left: point.x - 25,
      top: point.y - 25,
      width: 50,
      height: 50,
      borderRadius: 16,
      display: 'grid',
      placeItems: 'center',
      background: color,
      color: '#061019',
      fontSize: 11,
      fontWeight: 850,
      boxShadow: `0 0 0 7px ${color}1F, 0 10px 30px ${color}55`,
      zIndex: 8,
    }}
  >
    {label}
  </div>
);

const groupLabel: CSSProperties = {
  position: 'absolute',
  top: 136,
  color: palette.muted,
  fontSize: 12,
  fontWeight: 800,
  letterSpacing: '0.14em',
};

export const QuivrReadmeLoop = () => {
  const frame = useCurrentFrame();
  const {fps} = useVideoConfig();
  const pluginPhase = frame >= 116;
  const titleBlend = interpolate(frame, [108, 124], [0, 1], {...clamp, easing: Easing.inOut(Easing.cubic)});
  const pluginReveal = spring({
    frame: frame - 122,
    fps,
    config: {damping: 200},
    durationInFrames: 0.7 * fps,
  });
  const pluginLine = interpolate(frame, [132, 158], [0, 1], {...clamp, easing: Easing.inOut(Easing.cubic)});
  const directProgress = interpolate(frame, [22, 104], [0, 1], {...clamp, easing: Easing.inOut(Easing.cubic)});
  const pluginProgress = interpolate(frame, [158, 232], [0, 1], {...clamp, easing: Easing.inOut(Easing.cubic)});
  const directRoute = [
    {x: 144, y: 294},
    {x: 344, y: 294},
    {x: 552, y: 294},
    {x: 756, y: 294},
    {x: 1052, y: 294},
  ];
  const pluginRoute = [
    {x: 552, y: 294},
    {x: 612, y: 505},
    {x: 756, y: 294},
    {x: 1052, y: 294},
  ];
  const payloadPoint = pluginPhase
    ? pointOnRoute(pluginRoute, pluginProgress)
    : pointOnRoute(directRoute, directProgress);
  const activeDirect = Math.round(directProgress * 4);
  const activePlugin = Math.round(pluginProgress * 3);
  const coreOpacity = interpolate(pluginReveal, [0, 1], [1, 0.72]);

  return (
    <AbsoluteFill style={{fontFamily, color: palette.text, overflow: 'hidden'}}>
      <Background accent={pluginPhase ? palette.plugin : palette.core} />

      <div style={{position: 'absolute', top: 35, left: 44, zIndex: 10, display: 'flex', alignItems: 'center', gap: 13}}>
        <div style={{width: 38, height: 38, borderRadius: 12, display: 'grid', placeItems: 'center', background: palette.core, fontWeight: 900}}>Q</div>
        <div style={{fontSize: 22, fontWeight: 830, letterSpacing: '-0.035em'}}>Quivr V2</div>
      </div>
      <div style={{position: 'absolute', top: 42, right: 45, color: palette.muted, fontSize: 12, fontWeight: 760, letterSpacing: '.11em'}}>
        MULTIMODAL · PLUGIN-FIRST
      </div>

      <div style={{position: 'absolute', top: 82, left: 0, right: 0, height: 45, textAlign: 'center', zIndex: 5}}>
        <div style={{position: 'absolute', inset: 0, opacity: 1 - titleBlend, fontSize: 27, fontWeight: 820, letterSpacing: '-0.035em'}}>
          Un contenu devient utile, étape par étape.
        </div>
        <div style={{position: 'absolute', inset: 0, opacity: titleBlend, fontSize: 27, fontWeight: 820, letterSpacing: '-0.035em', color: palette.plugin}}>
          Ajoutez une capacité. Le cœur ne change pas.
        </div>
      </div>

      <div style={{...groupLabel, left: 55}}>CONTENUS</div>
      <div style={{...groupLabel, left: 286}}>CŒUR QUIVR</div>
      <div style={{...groupLabel, left: 955}}>USAGES</div>

      <div
        style={{
          position: 'absolute',
          left: 270,
          top: 161,
          width: 595,
          height: 266,
          borderRadius: 28,
          border: `1px solid ${palette.core}55`,
          background: `${palette.core}0A`,
          opacity: coreOpacity,
          zIndex: 1,
        }}
      />

      <svg width="1200" height="675" style={{position: 'absolute', inset: 0, zIndex: 2}}>
        <Connector d="M222 294 H280" color={palette.source} muted={pluginPhase} />
        <Connector d="M408 294 H482" color={palette.core} muted={pluginPhase} />
        <Connector d="M622 294 H680" color={palette.core} muted={pluginPhase} />
        <Connector d="M832 294 H942" color={palette.output} muted={pluginPhase} />
        <Connector d="M552 356 C552 420 570 455 612 472" color={palette.plugin} progress={pluginLine} />
        <Connector d="M750 505 C790 480 792 425 756 368" color={palette.plugin} progress={pluginLine} />
      </svg>

      <GraphCard x={48} y={220} width={174} height={148} color={palette.source} icon="content" title="Entrées" active={!pluginPhase && activeDirect === 0} entrance={0.1}>
        Texte · image<br />Audio · vidéo
      </GraphCard>
      <GraphCard x={280} y={232} width={128} height={124} color={palette.core} icon="api" title="API" active={!pluginPhase && activeDirect === 1} entrance={0.25}>
        Accepte<br />Déduplique
      </GraphCard>
      <GraphCard x={482} y={232} width={140} height={124} color={palette.core} icon="workflow" title="Temporal" active={pluginPhase ? activePlugin === 0 : activeDirect === 2} entrance={0.38}>
        Orchestre<br />Reprend
      </GraphCard>
      <GraphCard x={680} y={220} width={152} height={148} color={palette.data} icon="search" title="Données" active={pluginPhase ? activePlugin === 2 : activeDirect === 3} entrance={0.51}>
        PostgreSQL · S3<br />Recherche hybride
      </GraphCard>
      <GraphCard x={942} y={220} width={212} height={148} color={palette.output} icon="output" title="Résultats" active={pluginPhase ? activePlugin === 3 : activeDirect === 4} entrance={0.64}>
        Recherche · veille<br />Alertes
      </GraphCard>

      <div style={{opacity: pluginReveal}}>
        <GraphCard x={470} y={472} width={292} height={118} color={palette.plugin} icon="plugin" title="Plugin · Transcription" active={pluginPhase && activePlugin === 1} entrance={6.1}>
          Worker externe · OCI · SemVer
        </GraphCard>
        <div style={{position: 'absolute', left: 782, top: 513, color: palette.plugin, fontSize: 12, fontWeight: 800, letterSpacing: '.08em', zIndex: 5}}>
          NOUVELLE CAPACITÉ
        </div>
      </div>

      <Payload point={payloadPoint} color={pluginPhase ? palette.plugin : palette.source} label={pluginPhase ? '+TXT' : 'DOC'} />

      <div style={{position: 'absolute', bottom: 28, left: 0, right: 0, textAlign: 'center', color: palette.muted, fontSize: 13, zIndex: 5}}>
        Le document reste le même. Les plugins ajoutent des capacités autour d’un contrat stable.
      </div>
    </AbsoluteFill>
  );
};
